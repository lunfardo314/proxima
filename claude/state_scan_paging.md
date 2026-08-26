# Paged state scan — cursors, IDs-only fetch, and pinned-snapshot sessions

> **LIVE** — spec for reading more of the ledger state than one API call can
> return: a resumable cursor over a controller's UTXOs, an IDs-only listing with
> paged fetch of the bytes, and (only if needed) a server-side scan session
> holding a pinned state reader.
> **Not implemented yet.** Nothing here exists; every state-read endpoint today
> is single-shot and capped.
>
> Companion: [`claude/compact.md`](compact.md) — the first consumer. Compaction
> is specified to work *without* this and to get faster if it arrives, so nothing
> here is on that critical path.

Date: 2026-08-26

---

## 1. The problem

Every state read is single-shot, capped, and starts from the beginning.

`get_outputs` walks the controller's trie partition, collecting hits until
`api.GetOutputsIterationCap = 2000`, then sets `LimitExceeded` and stops:

```go
err1 := rdr.IterateUTXOsForController(indexValue, func(oid base.OutputID, odata []byte) bool {
    if len(hits) >= api.GetOutputsIterationCap {
        resp.LimitExceeded = true
        return false
    }
    hits = append(hits, rawHit{oid: oid, odata: odata})
    return true
})
```

Three consequences:

- **You cannot see past 2000.** An account with 5000 UTXOs has 3000 that no API
  call can reach. Not "slow to reach" — unreachable.
- **Repeated calls re-walk from the start.** There is no cursor. Draining an
  account by repeated call is quadratic in the number of UTXOs, and every call
  re-serialises the same first 2000 outputs.
- **The snapshot moves.** Each call independently resolves the LRB (or, with the
  `lrb_depth` parameter proposed in `compact.md` §5.1, a branch N back). Two
  calls can land on different branches, so a client assembling a picture across
  calls is stitching together inconsistent snapshots.

Full output bytes make all of this worse: `get_outputs` returns hex-encoded
output data, so a page of 256 outputs is far larger than a page of 256 output
IDs, and the client frequently only needs the IDs to decide what to fetch.

**Who needs this.** Compaction of very large accounts (`compact.md`), any future
wallet UI listing a large account, and analytics or audit tooling walking the
state. None of them needs it *urgently* — compaction converges without it (see
`compact.md` §1.1) — which is why this is a separate document and not a blocker.

---

## 2. Design principle: stateless first

There are two ways to page a scan, and they differ enormously in cost.

**Stateless** — the client sends everything needed to resume: which snapshot
(a root or branch ID) and where it stopped (the last key seen). The server holds
nothing between calls. Idempotent, restartable, survives a node restart or a load
balancer moving the client to a different node that has the same branch, and adds
no DoS surface beyond what `get_outputs` already has.

**Stateful (session)** — the server holds a pinned state reader and a live
iterator behind a session ID. Strictly more capable: it can guarantee a stable
snapshot even if the branch would otherwise be pruned, and can hold a partially
consumed iterator. But it is the **first stateful object in an otherwise
stateless API**, and it brings all of: TTL and expiry, session limits per client
and globally, memory held per session, a pinned DB snapshot that blocks
reclamation, cleanup on client disappearance, and behaviour on node restart.

**The recommendation is stateless (§3–§4).** It covers every use case identified
so far, including compaction's. §5 specifies the session design because the user
asked for it and because there is one scenario it genuinely serves — but it is
proposed as a *later* addition, gated on a demonstrated need, not as part of the
first implementation.

---

## 3. Stateless cursor over a controller's UTXOs

### 3.1 The key insight

Iteration is already ordered. `Readable.IterateUTXOIDsForController` walks a trie
prefix:

```go
accountPrefix := common.Concat(TriePartitionControllers, byte(len(controller)), controller)
r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
    oid, err = base.OutputIDFromBytes(k[len(accountPrefix):])
    ...
})
```

Keys under the prefix are output IDs in trie order — a total order, stable for a
given root. So "resume after output ID K" is a well-defined position, and a
cursor is just that ID. No server state: the client sends the last ID it saw, the
server seeks past it and continues.

**Verification needed before implementing.** This rests on `trie.Iterator(prefix)`
supporting a seek — starting at a key ≥ some value rather than only at the prefix
start. If unitrie's iterator has no seek, there are two fallbacks: add one
(`unitrie` is our dependency, `github.com/lunfardo314/unitrie`), or iterate from
the prefix start and skip keys ≤ cursor, which is correct but leaves the walk
quadratic even though the *serialisation* stops being quadratic. A seek is worth
having; confirm before committing to this design.

### 3.2 Pinning the snapshot

The cursor is only meaningful against a fixed root: trie order is stable within a
root, and outputs appear and disappear between branches. So the resume call must
name the snapshot, not re-resolve "latest".

The first call resolves the snapshot (LRB, or `lrb_depth` back per `compact.md`
§5.1) and **echoes the branch ID and root** it used. Subsequent calls pass that
branch ID back. The server serves from exactly that branch, or returns a distinct
error if it no longer has it (pruned, or never had it), which the client handles
by restarting the scan from a fresh snapshot.

This is what makes the paging consistent without any server state: the *client*
carries the snapshot identity, and the server merely refuses to guess.

### 3.3 The endpoints

Two, both additive.

**A. IDs-only listing** — the point-6 primitive:

```
GET /api/v1/get_output_ids?index_value=<hex>
      [&lrb_depth=N] [&branch=<txid hex>]
      [&after=<output id hex>] [&max=N]
```

```json
{
  "branch_id": "…", "root": "…", "slot": 41203,
  "ids": ["…", "…"],
  "next_after": "…",
  "exhausted": false
}
```

The state reader already has the primitive: `GetUTXOIDsForController` returns
`[]base.OutputID` and `IterateUTXOIDsForController` streams them — neither
touches the ledger-state partition to fetch output bytes, so a page of IDs costs
one trie walk and no output hydration.

An output ID is 33 bytes (66 hex chars), against output data that routinely runs
to hundreds of bytes. A 1000-ID page is small; a 1000-output page is not. That
asymmetry is the whole argument for splitting listing from fetching: the client
can enumerate an entire large account cheaply, decide what it actually wants, and
pull only that.

**B. Fetch by IDs, from a named branch:**

```
GET /api/v1/get_outputs_by_ids?ids=<hex,hex,…>&branch=<txid hex>
POST /api/v1/get_outputs_by_ids     (body: {"branch": "…", "ids": [...]})
```

```json
{ "branch_id": "…", "outputs": [{"id": "…", "data": "…"}], "missing": ["…"] }
```

`branch` is mandatory here — fetching against a different snapshot than the one
enumerated is exactly the inconsistency this design exists to prevent. `missing`
lists IDs absent from that branch (spent, or never there); it is normal, not an
error, and the client treats those as gone.

POST matters because a URL carrying 256 hex output IDs is ~17 KB, past what some
proxies accept. GET stays for small batches and manual use.

**Caps.** `max` on listing and `len(ids)` on fetch both cap at a fixed server
limit (proposed: 1000 IDs per listing page, 256 outputs per fetch — the latter
matching the 256-input transaction limit, since that is the natural consumer
batch). Exceeding it is an error, not a silent truncation — silent truncation is
what makes the current cap hard to reason about.

**`get_outputs` keeps its current shape**, gaining only `after` for symmetry. The
existing filters (`lock_type`, `chained`, `spendable`, `for_amount`, sorting) are
server-side post-filters over the walked set and interact awkwardly with paging:
a cursor over *filtered* results is not a trie position. Rather than resolve that,
the IDs-only path deliberately offers **no filtering** — it is a raw ordered
enumeration, and filtering is the client's job once it fetches the bytes it
chose. That keeps the cursor honest.

### 3.4 What a client does

```
ids, branch, cursor = get_output_ids(account, lrb_depth=1, max=1000)
while not exhausted:
    for page in chunks(ids, 256):
        outs = get_outputs_by_ids(page, branch)      # same snapshot throughout
        ... classify, plan, act ...
    ids, cursor = get_output_ids(account, branch=branch, after=cursor, max=1000)
```

The snapshot is fixed for the whole walk; the cursor is a 33-byte token the
client can persist, log, or resume from tomorrow (as long as the branch survives).

---

## 4. Sorting, and why the cursor does not do it

`get_outputs` sorts by amount or timestamp. A cursor cannot: trie order is by
output ID, and a sort is a property of the whole result set, not a resumable
position. Paging a sorted view means either materialising the full set per page
(the current cost, unchanged) or an index that does not exist.

So: **paged listing is unsorted (trie order); sorted views stay single-shot and
capped.** A client that wants "the 100 largest" uses today's `get_outputs`, which
already does that within the cap. A client that wants *everything* pages in trie
order and sorts locally — which it can, because it has all of it.

For compaction this is a non-issue: batching wants urgency-category ordering,
computed wallet-side from output bytes, not server-side amount ordering.

---

## 5. Scan sessions (later, if needed)

The stateless design has one real gap: it cannot **guarantee** the snapshot
outlives the walk. A slow client paging a large account against a branch that
gets pruned gets a "branch unavailable" error and restarts. Correct, but wasted
work, and unbounded restarts if the client is slower than pruning.

A session closes that gap by holding the reader open:

```
POST /api/v1/scan/open    {index_value, lrb_depth}  -> {session_id, branch_id, root, ttl_sec}
GET  /api/v1/scan/next?session_id=…&max=N           -> {ids | outputs, exhausted}
POST /api/v1/scan/close   {session_id}
```

The session holds a `multistate.Readable` for a fixed root plus an iteration
position, refreshes its TTL on each `next`, and is reaped on expiry or `close`.

**Why this is genuinely complex**, and why it should not be built first:

- It pins a DB snapshot, which blocks reclamation of the state it references —
  a slow or abandoned session becomes a storage leak with a client-controlled
  trigger.
- It is a client-controlled server-side allocation, which is a DoS primitive:
  open many sessions, never close them. Needs per-IP and global session caps, a
  short TTL, and a hard ceiling on total pinned snapshots — the same category of
  reasoning as `core/resilience.md`, which is where the gates on the transaction
  path are documented and where any new client-triggered allocation belongs.
- It breaks the API's statelessness assumption: sessions die on node restart, do
  not survive a client being routed to a different node, and need explicit
  client-side fallback for both.
- It duplicates the stateless path rather than replacing it: clients must still
  handle expiry, so they still need the restart logic §3.2 requires.

**Gate on evidence.** Build sessions only when a concrete workload demonstrably
fails with §3 — a real account large enough and a client slow enough that branch
pruning beats it. Until then the cursor is strictly simpler and the failure mode
(restart the scan) is acceptable. If it turns out to be needed, the natural first
step is not a full session but **snapshot pinning alone**: a way to ask a node to
retain a specific branch for a bounded time, with the iteration staying
stateless.

---

## 6. Resource notes

Whatever is built:

- **Caps are errors, not truncations.** Requesting more than the server allows
  returns an error naming the limit. Today's silent `LimitExceeded` is a trap:
  callers that ignore the flag get a wrong answer that looks right.
- **Rate limiting.** IDs-only listing is cheap enough to be attractive to abuse
  (one call can walk a whole account). It walks the trie, so it is not free —
  worth the same treatment as other read endpoints.
- **`lrb_depth` interacts with pruning.** Deeper snapshots are closer to being
  pruned; a deep `lrb_depth` plus a slow walk is the case most likely to hit
  "branch unavailable". Worth stating in the endpoint documentation.
- **No new KV interfaces.** Everything here goes through existing
  `ledger/multistate` readers (`IterateUTXOIDsForController`,
  `GetUTXOIDsForController`, `IterateUTXOsForController`), per CLAUDE.md's rule
  against inventing KV access interfaces.

---

## 7. Implementation order

1. **Verify iterator seek** (§3.1) in `github.com/lunfardo314/unitrie` — this
   gates the whole design. If absent, decide: add it upstream, or accept
   skip-to-cursor.
2. **`get_output_ids`** (§3.3 A) — thin wrapper over
   `IterateUTXOIDsForController` with `after`/`max` and snapshot echo. Standalone
   and useful immediately: it is the first way to learn how many UTXOs an account
   actually has.
3. **`get_outputs_by_ids`** (§3.3 B), GET and POST.
4. **Client helpers** in `api/client` — a paging iterator over the two, so
   callers do not hand-roll the cursor loop.
5. **`after` on `get_outputs`** for symmetry, unsorted mode only (§4).
6. **Sessions** (§5) — only on demonstrated need.

Steps 2–3 alone remove the "unreachable past 2000" problem, which is the part
that actually blocks anything.

---

## 8. Open questions

1. **Does unitrie's iterator support seek?** Gates §3. Verify first.
2. **Should IDs-only listing support `lock_type`?** It is the one filter that is
   cheap on the ID path (it needs output bytes, so actually it is not — it would
   force hydration and defeat the point). Stated here to be explicitly rejected
   unless someone shows a cheap way.
3. **Page-size caps** — 1000 IDs / 256 outputs are proposed, not measured.
4. **Is `lrb_depth` shared or per-endpoint?** `compact.md` §5.1 proposes it for
   `get_outputs`. If several endpoints gain it, it should be one documented
   convention with identical semantics, not three similar parameters.
5. **Sessions at all?** §5 argues for deferring. Worth an explicit decision so it
   does not get half-built.
