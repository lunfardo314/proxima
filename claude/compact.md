# Wallet compaction — scan, categories, paced parallel rounds, auto mode

> **LIVE** — spec for the enhanced `proxi node compact`: a scan mode that counts
> what is compactable by category, category-selective compaction, several
> compacting transactions in flight at once, and a long-running auto mode.
>
> **Shipped so far: the scan (§5), as `proxi node compact scan`, plus the two
> API parameters it needed — `lrb_depth` on `get_outputs` (§5.1) and
> `count_only` on `get_cleanable_outputs` (§5.3).** Everything that *builds
> transactions* — category-selective compaction, paced parallel batches, rounds,
> auto mode — is **not built**: the per-category subcommands exist in the command
> tree and report NOT IMPLEMENTED. `proxi node compact [N]` is still the
> single-transaction sweep of §2.
>
> The scan counts **both** pools an account can consume: what is indexed under
> it, and the publicly-abandoned dust anyone may take (§5.3). Reporting only the
> first is what made a wallet look empty while `utxo-cleanup` was busy consuming
> thousands of outputs.
>
> Companion: [`claude/state_scan_paging.md`](state_scan_paging.md) — the paged /
> cursored state scan this command would use once it exists. Compaction is
> deliberately specified to work *without* it.

Date: 2026-08-26, scan shipped 2026-08-27

---

## 1. Why

`proxi node compact` today builds exactly **one** transaction: it asks the node
for up to 256 spendable UTXOs, keeps the 100 largest, sweeps them into one
sigLock output and exits. Adequate for a wallet holding tens of UTXOs; not for
the accounts that actually accumulate them — a miner's payout account, a faucet,
a spammer's account, a delegation owner collecting tag-along change — which reach
hundreds or thousands and drain one transaction per invocation, each costing a
full inclusion wait.

Three things are missing:

- **You cannot see what you have.** The breakdown printed today is computed over
  the ≤ 100 outputs already selected for the sweep, so it describes the
  transaction about to be built, not the account. There is no way to ask "how
  much of this is reclaimable tag-along fee, and how much of *that* has fallen
  into the public window where anyone can take it".
- **You cannot compact selectively.** Category urgency differs by orders of
  magnitude (§3) — one of them loses tokens if left alone — yet they all go into
  one batch ordered by amount.
- **Draining is serial.** 1000 UTXOs is 10 transactions at 100 inputs each, with
  disjoint inputs and no ordering constraint between them, paying ten inclusion
  waits instead of one.

### 1.1 The governing principle: compaction repeats

Every design choice below follows from one fact: **a compacting run does not have
to be complete.** Whatever it misses — because a result set was capped, a batch
failed a gate, a transaction was dropped or lost a conflict — is still there next
round, and the next scan finds it. Compaction is idempotent and convergent.

So this spec deliberately prefers a simple capped mechanism over a complete
complex one, everywhere the choice arises: it does not add a paging API (§4), it
does not merge or rebalance batches to squeeze out the last UTXO (§6.1), and it
does not retry failed transactions (§6.4). The one place where "good enough" is
*not* acceptable is transaction pace (§4), because violating it fails silently.

---

## 2. What exists today (the baseline this extends)

`proxi/node_cmd/compact.go`, one pass:

1. `glb.GetLedgerTimeNow().Slot` → `targetSlot`, used as both the classification
   slot and the transaction's target slot.
2. `client.GetSpendableOutputs(...)` — a wrapper over `get_outputs` with
   `spendable=true&target_slot=N`, filtered **server-side** by
   `isSpendableForAccount`, reading the **LRB**.
3. Sort descending by amount, truncate to `maxNumberOfInputs` (default 100,
   max 256).
4. Re-classify wallet-side with `txbuildercore.ClassifySpendable` →
   `SpendSimple` / `SpendNeedsReturn` / `SpendUnknown`; only `SpendSimple` is
   swept.
5. Bucket the swept set with `Library.ClassifyLock` for the printed breakdown.
6. Attachment-cost gate, prompt, `txbuildercore.MakeCompactTransaction`, submit,
   `glb.TrackTxInclusion`.

Steps 4–6 are reused; the transaction builder needs one small addition (§4.3).

**Constants this spec leans on** (values as of `develop`):

| Constant | Value | Where | Bearing |
|----------|-------|-------|---------|
| `TransactionPace` | 12 ticks | ledger | **sender pace gate (§4)** |
| `TicksPerSlot` | 128 | `ledger/base` | 10 paced txs per slot per wallet |
| `TickDuration` | 80 ms | ledger | slot ≈ 10.24 s |
| `concentrationTolerance` | 1 | `txinput_queue` | zero tolerance for close timestamps |
| `keepTimestamps` | 4 | `txinput_queue` | ring depth of the per-sender check |
| `maxSlotsInTheFuture` | 6 | `txinput_queue` | ceiling on staggering ahead |
| `TagAlongSlots` | 30 (~5 min) | ledger | sender may reclaim its fee from here |
| `TagAlongReclaimSlots` | 390 (~1 h 5 min) | ledger | **anyone** may take it from here |
| `AttachmentCostBudget` | 550 | ledger | caps inputs + produced outputs per tx |
| max inputs per tx | 256 | ledger | structural limit |
| `GetOutputsIterationCap` | 2000 | `api` | server stops walking the trie here |

---

## 3. The categories

The taxonomy is not invented here — `ClassifySpendable` + `ClassifyLock` already
distinguish every case. This spec names the buckets, makes them **disjoint** so
counts sum, and orders them by urgency.

| Name | Classifier | Window | Urgency |
|------|-----------|--------|---------|
| `swd-accept` | `LockKindSWDTargetSig`, Δ < `acceptanceSlots` | **closing** | **1 — tokens are lost.** The wallet is the sigLock target of a `sendWithDeadline`. Once Δ reaches `acceptanceSlots` the window shuts and the master reclaims. Not compacting forfeits the payment. |
| `tagalong-cleanup` | `LockKindTagAlongSender`, Δ ≥ `TagAlongReclaimSlots` | public | **2 — tokens are at risk.** The wallet's own prepaid fee, never taken by the sequencer, now consumable by any signer. Still the wallet's own output, so compact keeps claiming it — but a cleaner may get there first. |
| `tagalong-reclaim` | `LockKindTagAlongSender`, `TagAlongSlots` ≤ Δ < `TagAlongReclaimSlots` | exclusive | 3 — safe. The sequencer's exclusive claim lapsed; nobody else can take it yet. |
| `swd-reclaim` | `LockKindSWDMaster`, Δ ≥ `acceptanceSlots` | open forever | 4 — safe. Wallet is master of an SWD the target never accepted. `returnToSender` is a noop when the master signs. |
| `siglock` | `LockKindSig`, 3-element output | none | 5 — safe. Pure UTXO-count reduction. |

Two report-only categories, never swept:

| Name | Classifier | Why not |
|------|-----------|---------|
| `needs-return` | `SpendNeedsReturn` | SWD accept-as-target carrying `returnToSender`: claiming it obliges a return receipt to the master in the same transaction, which the compact builder does not produce. **Counted and warned about** — these are `swd-accept`-urgent, the window closes on them too, and the wallet needs to know it is losing them. |
| `unknown` | `SpendUnknown` | Lock-level claim exists but the output carries unrecognised constraints. Refused, not consumed. |

Notes that matter for correctness:

- `tagalong-cleanup` means **the wallet's own** fees decayed into the public
  window. It is *not* `proxi node utxo-cleanup`, which sweeps dust abandoned by
  anybody. The two commands differ in what they look for — compact claims only
  outputs this wallet has a role in — but the output *sets* do intersect exactly
  here: once the wallet's own fee goes public it is simultaneously fair game for
  every cleaner. That intersection is the overlap the scan subtracts (§5.3).
- Tag-along **target** side and **chainLock**-target SWD are excluded by the
  classifier itself — both need a chain input in the same transaction.
- Compacting *creates* one tag-along output per transaction. Normally the
  sequencer takes it as its fee; when it does not, it reappears as
  `tagalong-reclaim` after 30 slots. A drain loop therefore has a small,
  self-limiting tail rather than converging to exactly one UTXO. Scan says so.

### 3.1 Window drift

Categories are decided at `targetSlot`, but a transaction is validated at its own
timestamp, which is ≥ `targetSlot`. Δ can therefore be larger on chain than at
classification.

Harmless for four of five categories — their conditions are all `Δ ≥ something`,
monotone in Δ. `tagalong-reclaim` drifting into the public window is still
claimable by the sender.

`swd-accept` is the exception: its condition is `Δ < acceptanceSlots`, so drift
invalidates it and fails the whole transaction. This matters more here than
today, because staggering (§4.2) pushes later batches deliberately into the
future. Two rules:

1. **Margin.** Exclude any `swd-accept` output whose remaining window
   (`acceptanceSlots − Δ`) is below `swdAcceptMarginSlots` (proposed: 2, measured
   from *that batch's* timestamp, not the round's base). Surfaced in scan as
   "expiring, too close to batch safely" so it can be claimed with a targeted
   single-input transaction.
2. **Post-build re-check.** After the timestamp is fixed, re-run
   `ClassifySpendable` for every `swd-accept` member at the actual `ts.Slot`;
   drop any that no longer qualifies and rebuild that batch once. Cheap (pure
   byte parses), and turns a whole-transaction failure into the loss of one
   input.

---

## 4. Transaction pace — the hard constraint

**This is the constraint that shapes parallel mode, and it fails silently.**

`core/core_modules/txinput_queue.checkSenderPace` runs on every transaction
entering a node from the API or a peer. It keys on `tx.HolderID()` — **the
wallet**, not the input, not the connection — and feeds the timestamp into a ring
buffer:

```go
seen.nonSequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPace))
```

`tsRingBuffer.addTs` keeps the last `keepTimestamps = 4` timestamps for that
holder and passes only if **fewer than `concentrationTolerance = 1`** of them lie
within `TransactionPace = 12` ticks. Tolerance 1 means *zero*: **no two
transactions from the same wallet may have timestamps closer than 12 ticks
apart**, compared against the last four seen.

### 4.1 Why this is dangerous rather than merely limiting

A transaction failing this check is **dropped, not rejected**. The API submit
path (`SubmitTxBytesFromAPI` → `TxBytesInFromAPIQueued`) is asynchronous:
`checkSenderPace` runs later, in `processValidated`, long after the HTTP response
went back. So the wallet sees a **successful submit** for a transaction that then
silently never exists. On the node it is a `rate_control` warning and a txlog
line ("timestamp is too close to another tx from the same sender → IGNORED"); to
the client, nothing.

The naive parallel design — build P batches from one snapshot, all with
`compactTimestamp` = `max(T(targetSlot, 10), newestInput + pace)` — gives every
batch the **same timestamp**. Exactly one would survive; the rest would vanish
without an error, and a client that only checked submit status would report
success for all of them. Hence:

- staggering is mandatory (§4.2), and
- the parallel tracker must treat "submitted but never included by timeout" as a
  first-class outcome and name pace as its likely cause (§6.3).

Because `addTs` compares **absolute differences of ledger timestamps**, not
arrival times, correctly staggered transactions pass on every node regardless of
the order they arrive in or which peer gossips them. The fix is entirely in what
timestamps we choose.

### 4.2 The staggering rule

Batch *i* of a round gets

```
ts_i = base + i * TransactionPace          (i = 0, 1, 2, …)
```

where `base` is the current `compactTimestamp` result for that batch, and every
`ts_i` must still satisfy the existing per-input rules: `ts_i ≥ newestInput_i +
TransactionPace`, and `ts_i` is not a slot boundary (reserved for branches). If
raising `ts_i` for the per-input rule collides with `ts_{i+1}`, the whole tail
shifts up.

Consequences, all of which bound `--parallel`:

- **10 transactions per slot per wallet.** Ticks 1..127 spaced by 12 gives ~10
  usable positions per slot.
- **~1 second of added latency per batch** (12 ticks × 80 ms = 0.96 s). Ten
  batches span ~1 slot (~10 s).
- **Hard ceiling ~64.** `maxSlotsInTheFuture = 6` rejects timestamps beyond 6
  slots ahead; 6 × 128 / 12 ≈ 64. Well beyond any sensible `--parallel`.
- **Future-dated transactions wait.** A node holds a transaction whose ledger
  time has not arrived (`proxima_general_gauge_wait`) rather than dropping it, so
  staggering trades latency for throughput — it does not lose transactions.

So parallelism is not free after all: it costs ~1 s per additional transaction.
That is still overwhelmingly better than serial compaction, which costs a full
**inclusion wait** (several slots, tens of seconds) per transaction. Ten batches
paced across one slot and confirmed in one wait beats ten sequential waits by
roughly an order of magnitude.

**`--parallel` default 4, max 10** — one slot's worth, no future-dating beyond
the current slot in the common case.

### 4.3 What this needs from the builder

`compactTimestamp` currently derives the timestamp internally, so a caller cannot
stagger. Add an optional explicit timestamp to `CompactParams`:

```go
type CompactParams struct {
    ...
    // Timestamp, when non-zero, is used verbatim instead of the derived
    // compactTimestamp. The builder still asserts the invariants the derived
    // value guarantees: ts >= newestInput + TransactionPace, and ts is not a
    // slot boundary. Callers issuing several transactions from one wallet must
    // space timestamps by at least TransactionPace — the receiving node's
    // per-sender pace gate drops the ones that are too close, silently.
    Timestamp base.LedgerTime
}
```

Zero keeps today's behaviour exactly. Pace *policy* (how to stagger, how many)
stays in the wallet; the builder only enforces the invariants. That split matches
`feedback_constraint_layer_authoritative` in spirit: the layer that can check
does the checking, the layer that decides does the deciding.

### 4.4 Pace is a whole-wallet budget

The gate is per **holder ID**, so every transaction from this wallet contends for
the same 12-tick spacing — a concurrent `proxi node send`, a running miner
payout, a spammer on the same key. Auto mode (§7) is the realistic collision
risk, since it runs unattended for days.

This spec does not add cross-process coordination (there is no shared state
between `proxi` invocations to coordinate through). It does two cheaper things:
`--parallel` defaults low, leaving headroom in the slot; and a transaction that
is submitted but never included is reported with pace named as a likely cause,
so the failure is legible instead of mysterious.

---

## 5. Scan mode — SHIPPED

`proxi node compact scan [--target <account>] [--lrb-depth N]` reports and
builds nothing. It is also the first phase of every other mode, so there is one
code path: `scanForCompaction` in `proxi/node_cmd/compact_scan.go`, which the
compacting modes will call rather than reimplement.

`--target` makes scanning *someone else's* account work, and neither classifier
needs a private key — only a holder ID — so any sigLock account can be scanned.
A chainLock target is refused: its controller ID is a 24-byte chain ID, not a
holder.

Two buckets beyond what §3 lists turned out to be worth reporting, both
report-only:

- `pending` — the account has a role but its window has not opened yet (a
  tag-along fee inside the sequencer's exclusive window, a `sendWithDeadline`
  whose master reclaim is still ahead). Reported with how many slots until the
  first one opens. This is what makes a scan showing "nothing compactable"
  legible: it says whether that is permanent or a matter of waiting.
- `on chains` — delegations, foundries and sequencer chains. They must be split
  off *before* the spendable classifier sees them: a delegation carries a
  sigLock the account matches, so it would otherwise land in `unknown` and make
  an ordinary wallet look malformed.

### 5.1 Reading at LRB depth N — SHIPPED

Scan reads state at **`--lrb-depth N`**, not necessarily at the LRB tip.

**The default shipped as 0, not the 1 this section argues for.** `--compact` is
a mode of `balance`, and a read-only report answering "what do I have" should
answer it about the newest state a user can see; silently reporting one branch
back would be surprising in a way it is not for a command that builds
transactions. The argument below stands for the *compacting* modes, which build
unattended — depth 1 belongs on those, where it is a decision about what to
build on rather than about what to display.

The LRB is the latest *reliable* branch, but "reliable" is not "final": its
lineage can still be superseded. Outputs read at the tip may sit on a branch that
loses, so a transaction built on them can be invalidated wholesale. One branch
back is materially more settled at a cost of one slot's visibility — and since
compaction repeats (§1.1), losing sight of the newest slot's outputs costs
nothing but a round.

This matters most in **auto mode**, which builds transactions unattended, with
nobody reading the error. Depth 1 as the default is the conservative choice for
the mode that runs by itself; `--lrb-depth 0` restores tip reads for an
interactive user who wants to sweep something they just received.

`lrb_depth` and `target_slot` are **independent** and must stay so:

- `lrb_depth` selects *which state snapshot to read*;
- `target_slot` is *when the transaction will land*, and is what the Δ window
  checks in §3 are evaluated against.

Reading a 1-deep snapshot does not mean classifying at that branch's slot — the
windows must be evaluated at the transaction's slot, which is now-ish.

**Server support.** `get_outputs` used to hardcode `srv.withLRB(...)`. It now
takes an `lrb_depth=N` query parameter and reads through `withLRBAtDepth`, which
walks back N branches with `multistate.IterateBranchChainBack` — the same
mechanism, and the same notion of depth, as `Workflow.CheckTransactionInLRB`.
`N = 0` is the old behaviour, so the parameter is backward-compatible by
omission, and it benefits every state read rather than only compaction.

Two properties worth keeping if this is ever touched:

- **A short chain is an error, not a fallback.** Fewer branches available than
  requested (pruned state, a node just restored from a snapshot) fails the
  request. Quietly answering from a shallower branch would hand settled-state
  callers the tip under a settled-state name.
- **The client checks the echo.** The response carries `lrb_depth`, and
  `GetOutputsForControllerID` errors if it does not match what was asked. A node
  predating the parameter ignores unknown query parameters and answers from the
  LRB, so without the echo an old node looks exactly like a deep read. Verified
  against the testnet, which still runs pre-parameter nodes: the client refuses
  rather than reporting tip state as depth-2 state.

The response's `lrbid` is the branch actually read — the LRB itself at depth 0,
its N-th predecessor otherwise.

### 5.2 Result-set size

Scan uses `GetOutputsForControllerID` directly, with
`MaxOutputs: api.GetOutputsIterationCap` (2000) — the 256 ceiling is a
client-side clamp in `GetSpendableOutputs`, not a server limit. The server stops
walking the trie at 2000 and sets `LimitExceeded`. `AvailableAmount` is summed
server-side over the filtered set *before* the `max_outputs` truncation, so the
token total is exact up to the cap.

Above 2000 the scan is a **lower bound and says so**:

```
NOTE: the node stopped iterating at 2000 UTXOs — this account holds more.
      Counts below are a lower bound. Each round rescans, so draining still
      converges; it just takes more rounds.
```

Per §1.1 this is accepted rather than fixed. Fixing it properly means paging, and
paging is a large enough design — pinned snapshots, cursors, IDs-only fetch,
resource limits — to be its own document:
[`claude/state_scan_paging.md`](state_scan_paging.md). Compaction is specified to
work correctly without it and to get faster if it arrives.

### 5.3 The second pool: publicly-abandoned dust — SHIPPED

An account can consume two disjoint sets, found in two different ways:

- **Indexed under it** — its own sigLock outputs and the conditional locks it
  has a role in. Index lookup: cheap, exact, and what §3 classifies.
- **Abandoned by anybody**, having decayed into the public window of its
  conditional lock, where any signer may take it. Nobody's in particular, so
  *nothing indexes it under this account*. Found by walking old state; this is
  what `proxi node utxo-cleanup` sweeps.

Reporting only the first is what makes a wallet look empty while a cleaner
consumes thousands of outputs — on the testnet, an account showing 2 UTXOs while
**7,772** publicly-claimable outputs sat there for the taking. Both are counted.

**Overlap.** An output of the account's own that has decayed into a public
window is in *both* pools — still the account's to claim, no longer exclusively.
It is detected by running `ClassifyCleanable` over the account's own outputs
(so it catches any conditional lock, not just tag-along), reported per entry,
and subtracted once from the grand total.

**Counting needed a server change (`count_only=true`).** The obvious
implementation — page `get_cleanable_outputs` and add up — **cannot work, and
fails silently**: that scan cuts at `max_outputs` and resumes at the same slot
chunk, with no within-chunk cursor, so a reader gets the *same batch back
forever*. Measured: 40 rounds returned byte-identical output sets while
`next_chunk` sat at 156. A cleaner escapes this because it *consumes* what it
was handed, so the next scan sees different state; a report cannot. So the node
tallies instead — walking its whole chunk budget, aggregating by lock symbol,
returning no outputs. A complete testnet walk takes ~1.2 s, which is why the
scan counts the whole pool by default; `--public-rounds N` bounds it and the
result is then labelled a lower bound.

Keyed by lock symbol rather than a fixed enum, so a conditional lock added later
appears in the report without touching the tally code. As with `lrb_depth`, the
response echoes `count_only` and the client refuses a node that ignored it —
otherwise an old node reports "no dust" indistinguishably from an empty pool.

### 5.4 Output

Disjoint counts that sum, urgency order, empty categories omitted. Real output,
from a node restored from a testnet snapshot, for an account that owns 84 of the
publicly-claimable outputs:

```
COMPACTION SCAN of a/ee95436cf419d68be04170c543532f4ae04fc9692edf23dbb1be900328d67246
    state read on the LRB: s73984-0-012641496bb6..
    windows evaluated at slot 73985; 88 UTXO(s) indexed under this account

    YOURS, COMPACTABLE   count               tokens
      tagalong-cleanup        84              840_000   public — anyone may claim these
      siglock                  4          880_100_000
                         ------- --------------------
      subtotal                88          880_940_000

    PUBLIC, ABANDONED BY ANYBODY — any signer may claim these, including you
      tagAlong              7388           73_880_000
                         ------- --------------------
      subtotal              7388           73_880_000
      scan reached the oldest state: this pool is complete.
      This pool is not specific to the account: it is the same for everybody,
      it is a race, and 'proxi node utxo-cleanup' is what sweeps it.

    TOTAL CONSUMABLE     count               tokens
      total                 7392          953_980_000
      84 of your own output(s) (840_000) sit in a public window and are counted in
      both sections above; the total counts them once.
```

`swd-accept` carries how many slots are left on the tightest window,
`tagalong-cleanup` says the outputs are public, `pending` says how long until
the first claim opens. With `-v`, every category expands to output IDs, amounts
and remaining window.

The closing line estimates a full drain in transactions at the default 100
inputs each. It is arithmetic over the compactable count, not a plan: when the
planner of §6.1 exists this should come from it instead, so the estimate cannot
drift from what the compacting modes actually do.

---

## 6. Compacting by category, and in parallel

### 6.1 Categories and the planner

`--category <list>` (default `all`) and `--exclude <list>` filter by the §3
names. Filtering is **purely wallet-side** — the server's `spendable=true` filter
already returns the eligible set and `ClassifyLock` buckets it. No API change.

Outputs are sorted by **(urgency rank, then amount descending)**. Urgency first
is what makes a partial drain take the outputs that matter first — and since
every drain is partial (§1.1), that ordering is the point of the category work.
This replaces today's pure amount ordering.

The planner is pure and unit-testable:

```go
// planBatches deals urgency-ordered outputs into at most maxParallel batches of
// at most inputsPerTx inputs, assigns each batch a paced timestamp, and drops
// what does not fit. Leftovers are expected and are not an error: the next
// round rescans and picks them up.
func planBatches(outs []classified, inputsPerTx, maxParallel int, base base.LedgerTime, gates batchGates) ([]batch, []leftover)
```

Dealing is **round-robin across batches**. Sequential fill would put the large
outputs in batch 0 and leave the last batch pure dust, which then fails the
storage-deposit gate. Round-robin gives every batch a comparable amount profile
for free. It is a cheap heuristic, not a guarantee — a batch that still fails a
gate is simply dropped (§6.2).

Batches are **disjoint by construction**, which is what makes parallelism sound:
no two transactions share an input, none consumes another's output, every input
is rooted in the snapshot read, so there is no ordering constraint between them —
only the pace spacing of §4.2.

### 6.2 Per-batch gates

Checked per batch. A batch failing one is **dropped with a reported reason**, not
merged, rebalanced or trimmed — per §1.1 the outputs return next round, likely in
a different mix that passes.

| Gate | Rule | Rationale |
|------|------|-----------|
| input count | ≤ 256 | ledger structural limit |
| attachment cost | `len(inputs) + 2 ≤ 550` | as today (2 produced outputs). Inputs are rooted, so past-cone cost is ~0 and today's warning about non-rooted predecessors does not apply to a batch built from a committed snapshot. |
| tag-along fee | `batchTotal > fee` | else it cannot pay |
| storage deposit | `batchTotal − fee ≥ minStorageDeposit(produced)` | a pure-dust batch cannot produce a valid output. Wallet-side via the existing `minStorageDeposit` helper (`/eval` on `storageDeposit($0)`), one call per run — the produced output shape is identical across batches. |
| pace | `ts_i − ts_{i−1} ≥ 12 ticks` | §4.2. Not droppable — the planner assigns timestamps, so it is satisfied by construction; asserted, not checked. |
| swd drift | §3.1 margin + post-build re-check | window closing |

### 6.3 Submission and tracking

Batches are built from one snapshot at one `targetSlot`, given staggered
timestamps, then submitted concurrently, bounded by `--parallel`.

`glb.SubmitAndDisplay` prints as it goes and would interleave across goroutines.
Parallel mode calls `client.SubmitTransactionWithDetail` directly and renders
each result under a mutex, falling back to the existing `txDisplay` dump only for
failures.

Tracking needs a multi-transaction sibling of `glb.TrackTxInclusion`, beside it
in `proxi/glb/profile.go`:

```go
// TrackTxInclusionSet polls the LRB until every txid reaches the target
// inclusion depth or timeout expires. One poll covers all txids.
func TrackTxInclusionSet(txids []base.TransactionID, poll time.Duration, timeout ...time.Duration) (confirmed, pending []base.TransactionID)
```

Progress is one line per poll:

```
 12 sec. LRB: [a1b2..]  confirmed 3/4, pending 1
 18 sec. LRB: [c3d4..]  confirmed 4/4
```

**Three distinct outcomes per transaction**, and the third is the one §4.1
forces:

| Outcome | Meaning | Reported as |
|---------|---------|-------------|
| confirmed | reached target inclusion depth | swept, counted |
| submit failed | HTTP/validation error | reason printed, failing tx dumped |
| **submitted, never included** | accepted by the API, then dropped | `not included within <timeout> — most likely the per-sender pace gate (another transaction from this wallet within 12 ticks) or a double-spend conflict. Its inputs will reappear in the next scan.` |

Silent loss is the expected failure mode here, so the timeout branch must be
explicit and must name pace. Without it a user staring at a wallet that never
compacts has nothing to go on.

Per-transaction outcomes are independent: one failure does not abort the round,
and there is **no retry** — the state has moved, and rescanning is the correct
way to learn what is left.

### 6.4 Rounds

One round: scan → plan → submit → await inclusion. `--rounds N` caps the count;
`--rounds 0` means "until no further reduction".

A round **must** wait for inclusion before the next, for two reasons beyond
bookkeeping:

1. Round 2 consumes round 1's outputs, and those are only visible to a state read
   — and only rooted — once their transactions are in a branch. Chaining locally
   off unconfirmed outputs would load all of round 1 into round 2's non-rooted
   past cone, against a budget of 550.
2. It disposes of the pace question between rounds: an inclusion wait is many
   slots, far more than 12 ticks.

The loop terminates when a scan finds fewer than 2 compactable outputs, when a
round produces no reduction, or at `--rounds`.

```
round 1: 1014 compactable -> 4 transactions (400 UTXOs), paced ~3 s ... 4/4 confirmed
round 2:  618 compactable -> 4 transactions (400 UTXOs)             ... 3/4 confirmed, 1 not included
round 3:  322 compactable -> 4 transactions                          ... 4/4 confirmed
...
done: 6 rounds, 1010 UTXOs swept, 15,552,544,244 tokens, 23 fees paid (11,500,000)
```

---

## 7. Auto mode

`--auto` runs the round loop permanently: scan, decide whether compacting is
worth it, compact if so, sleep. Until Ctrl-C.

**The decision.** Compact when *either*:

- the compactable count is ≥ `--min-utxos` (default 20) — the count-reduction
  case; or
- **anything** sits in `swd-accept` or `tagalong-cleanup` — the at-risk case,
  whatever the count. One expiring payment is worth a transaction.

Otherwise sleep. That is what "compacts it if that makes sense" means concretely,
and it is why categories are prerequisite to auto mode rather than independent.

**Guards.**

| Guard | Default | Purpose |
|-------|---------|---------|
| `--interval` | 1m | wall-clock between scans; one API call, well under the 30-slot tag-along window |
| `--lrb-depth` | 1 | §5.1 — settled state matters most when nobody is watching |
| `--min-utxos` | 20 | count threshold for the non-urgent case |
| `--parallel` | 4 | §4.2; leaves pace headroom for other wallet activity |
| `--rounds` | 1 per wake | one round per interval; a drain-to-one every minute is rarely right |
| `--max-fee-per-round` | 0 (unlimited) | ceiling on fees per round; trimmed to fit, urgent categories first |

Repeated round failure backs the interval off (×2, capped 30 min), reset on first
success, so a wedged sequencer or a down node does not become a hot loop.

**No prompting.** Auto mode confirms once at startup, showing the policy it will
follow, then never prompts; `-f/--force` skips even that. `--auto` and `--scan`
are mutually exclusive.

**Shutdown.** SIGINT stops scheduling new rounds, waits (bounded) for in-flight
transactions, prints a cumulative summary: rounds, UTXOs swept, tokens, fees,
failures by kind.

Heartbeat keeps a quiet run legible:

```
14:32:10 scan @depth1 [c3d4..]: 8 compactable (siglock 8) — below threshold 20, sleeping
14:33:10 scan @depth1 [e5f6..]: 8 compactable — sleeping
14:34:10 scan @depth1 [a7b8..]: 9 compactable, 1 tagalong-cleanup — compacting (at-risk)
14:34:13 round: 9 UTXOs -> 1 transaction ... confirmed
```

---

## 8. CLI surface

Shipped:

```
proxi node compact scan [flags]

  --target <account>        any sigLock account; default the wallet's own
  --lrb-depth <N>           read state N branches back from the LRB (default 0)
  --public-rounds <N>       cap the walk over old state for public dust
                            (default 0 = until the oldest state)
  --no-public               report only what is indexed under the account
  -v                        expand every category to output IDs and windows
```

Not built — the subcommands exist and report NOT IMPLEMENTED, so the names are
settled and discoverable:

```
proxi node compact [<max inputs per tx>]        # today's single-transaction sweep
proxi node compact siglock [<max inputs>]
proxi node compact swd-accept [<max inputs>]
proxi node compact swd-reclaim [<max inputs>]
proxi node compact tagalong-reclaim [<max inputs>]
proxi node compact tagalong-cleanup [<max inputs>]
proxi node compact auto
```

A subcommand per category, rather than the `--category <list>` flag this spec
first proposed: each one is a different decision with different urgency, and one
of them (`swd-accept`) accepts incoming payments rather than tidying the wallet.
The flags that shape *how* a sweep runs — `--parallel`, `--rounds`, `--interval`,
`--min-utxos`, `--max-fee-per-round` — still belong on those subcommands when
they are built.

**Backward compatibility.** `--parallel 1 --rounds 1 --category all --lrb-depth 0`
is exactly today's behaviour. Two defaults deliberately differ from it —
`--parallel 4` and `--lrb-depth 1` — because both make the *unattended* case
correct and neither changes what an interactive user gets beyond being faster and
reading settled state. Input **ordering** also changes (urgency before amount,
§6.1). See open question 1.

```
proxi node compact --scan                                  # what do I have?
proxi node compact 200 --parallel 8 --rounds 0             # drain it
proxi node compact --category tagalong-cleanup             # reclaim what is at risk
proxi node compact --auto --interval 5m --min-utxos 50     # permanent guard
```

---

## 9. Out of scope

- **Other accounts' abandoned dust** — `proxi node utxo-cleanup`: different scan,
  different endpoint, different economics.
- **`returnToSender` accepts** (`needs-return`) — counted and warned about (§3),
  never built. A dedicated accept-with-receipt flow is the right home.
- **chainLock-target SWD and the tag-along target side** — need a chain input.
- **Paged / cursored state scan** — [`state_scan_paging.md`](state_scan_paging.md).
- **Cross-process pace coordination** — §4.4.
- **The transaction builder**, beyond the optional `Timestamp` field of §4.3.

---

## 10. Implementation

1. ~~**The two server parameters**~~ **DONE** — `lrb_depth=N` on `get_outputs`
   (§5.1), served by `withLRBAtDepth`; and `count_only=true` on
   `get_cleanable_outputs` (§5.3), served by `tallyCleanable`, without which a
   read-only caller cannot total the public pool at all. Both echo the parameter
   back so the client can refuse a node that ignored it.
2. **`ledger/txbuildercore/helpers_compact.go`** — optional `Timestamp` in
   `CompactParams` with the two invariant asserts (§4.3).
3. ~~**The category enum, urgency rank and classification**~~ **DONE** —
   `proxi/node_cmd/compact_scan.go`: the categories, the public-window flag via
   `ClassifyCleanable`, the public-pool walk, the renderer and the `scan`
   subcommand.
   `SWDAcceptanceSlots` was added to `ledger/txbuildercore` so the wallet can
   report how much of an accept window is left. Still to come in the same file:
   `planBatches` with pace-staggered timestamps (§4.2, §6.1) and the gates
   (§6.2).
4. **`proxi/node_cmd/compact.go`** — the per-category subcommands are stubs;
   filling them in means rewiring today's single-transaction path through the
   planner so a single-batch run provably reproduces current behaviour.
5. **`proxi/glb/profile.go`** — `TrackTxInclusionSet` (§6.3).
6. **Parallel submission and rounds** (§6.3, §6.4).
7. **Auto mode** (§7).
8. **Docs** — the user-facing `proxi` guide is on the docs site
   (`participate/proxi`); update it there once the compacting half lands.

New abstractions: `planBatches` and `TrackTxInclusionSet`. Both have a second
caller in sight (`utxo-cleanup` wants the tracker). No ledger change, no
hardfork; the one server change is additive and backward-compatible by omission.

**Testing.** `proxi/node_cmd/compact_scan_test.go` covers the shipped half:
synthetic outputs built with the real ledger library, classified through the
*wallet* library, asserting the bucket each lands in. What it is actually for is
the two decisions the scan makes that neither classifier does — splitting
tag-along reclaims at the public-window boundary, and separating "cannot claim
yet" from "cannot claim ever" — plus the disjointness of the buckets. The
end-to-end path was checked against a standalone node by comparing the scan's
compactable count and token total with what `proxi node compact` then claimed.

The planner, when it exists, wants the same treatment in the same file — it is
pure, so no node is needed:

- **pace**: consecutive batch timestamps are ≥ `TransactionPace` apart; raising
  one batch for its per-input rule shifts the tail; no timestamp is a slot
  boundary; P batches never exceed `maxSlotsInTheFuture`;
- **disjointness**: every output in at most one batch; batches + leftovers =
  input set;
- **gates**: over-256 split; sub-deposit batch dropped with a reason, not
  silently; over-budget batch dropped;
- **ordering**: urgency rank dominates amount; a run trimmed by `--rounds` or the
  fee cap keeps the urgent outputs;
- **round-robin**: with one large and many dust outputs, no batch is all-dust;
- **window drift** (§3.1): an `swd-accept` inside the margin is excluded —
  measured against *its own batch's* staggered timestamp, which is the case a
  naive implementation gets wrong; the post-build re-check drops a member whose
  window closed.

Classification itself is covered by `ledger/tests/spendable_classify_test.go`.
Step 1 touches `api/server` and step 2 touches `ledger/txbuildercore`, neither of
which is core (`core/memdag|attacher|vertex|workflow|sequencer`), so no `-race`
core run is implied; step 2 runs `go test ./ledger/...` per
`feedback_test_scope`.

---

## 11. Open questions

1. **Defaults that differ from today** — `--parallel 4` and `--lrb-depth 1`.
   Both are chosen for the unattended case. The conservative alternative is to
   default them to today's values (1 and 0) and have `--auto` override them
   internally, so an interactive `proxi node compact` behaves byte-for-byte as it
   does now. **Recommendation: keep 4 and 1**, since both are strict improvements
   for the interactive user too — but this is a behaviour change on an existing
   command and is yours to confirm.
   *Half-settled:* `--lrb-depth` shipped defaulting to **0** on the read-only
   scan (§5.1). The question stands for the compacting commands, which is where
   it always mattered.
2. **Default `--rounds`.** Spec'd 1, preserving today. Defaulting to 0 (drain)
   reads better as an intent, but a default that can issue an unbounded number of
   fee-paying transactions is the wrong default. **Recommendation: keep 1.**
3. ~~**`--scan` flag vs `compact scan` subcommand.**~~ **SETTLED** — neither.
   The scan is `proxi node balance --compact`: it is a read-only report about an
   account, which is what `balance` is, and `balance` already has the `--target`
   flag that lets it scan an account other than the wallet's own. `compact`
   points at it from its help text.
4. **`--parallel` max of 10.** One slot's worth. Going higher is possible (up to
   ~64 within the future bound) but future-dates transactions by whole slots.
   Worth a testnet measurement of what one tag-along sequencer absorbs before
   fixing it.
5. **Auto mode and SWD acceptance.** `--auto` with `swd-accept` in scope is an
   auto-accepting wallet daemon — it accepts payments with no human in the loop.
   That is a wallet-policy decision, not a compaction one. Should `swd-accept` be
   excluded from `all` in auto mode unless named explicitly?
   Splitting the categories into subcommands (§8) settles the *manual* half by
   construction — accepting is `compact swd-accept`, which nobody runs by
   accident. `compact auto` still needs the policy decided.
