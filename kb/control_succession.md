# Sequencer control succession — safe controller change

> **SPEC — proposed, not yet implemented.** A two-stage grant/claim protocol
> for transferring control of a sequencer chain from one holder ID to another
> without risking a bricked chain. Written to be implemented from; the ledger
> (EasyFL) needs no change, the work is in `sequencer/` + `proxi node seq`.
> **Reviewed against `develop` on 2026-09-23**: every ledger-side claim
> verified; the node-side sections (§5–§7, §9, §11, §12) were amended with what
> the code actually does today. Implement on `develop` first, then merge to
> `master` and `develop-take1`.

## 1. Problem

A sequencer chain is controlled by a **holder ID**: its chain output carries
`sigLock(controllerHolderID)`, and every milestone is a chain transition that
consumes the predecessor and must therefore be **signed by the controller's
private key** (`buildSequencerAndStemOutputs` re-emits the predecessor lock and
does `PutSignatureUnlock(0)` + `SignED25519(controllerKey)`). So today:

- **control = the sigLock holder = the private key the node runs with.**
- The privileged tag-along commands `withdraw` and `set-params` authorize by
  `o.SenderID == HolderIDFromPublicKey(configKey)` — the same key. (`askstop`
  is not a controller command: it authorizes against the delegation's master.)

Changing the controller means flipping that `sigLock` to a new holder ID. The
danger: if the controller flips the lock directly to a wrong/dead holder ID (a
typo, a key nobody holds), **the very next milestone can never be signed — the
chain is bricked**, permanently, with whatever balance it holds.

The succession protocol makes the change a safe two-phase handshake where
control only actually moves once the recipient has **proven it exists and holds
the key**.

## 2. Protocol overview

Two stages, both delivered as authenticated tag-along request outputs (the same
transport as the existing sequencer commands; `SenderID` is ledger-pinned to the
producing-tx signer by the `tagAlong` constraint, hence unspoofable).

### Stage 1 — GRANT (by the current controller)

The current controller sends a **grant** request naming a successor holder ID.
The sequencer records it in its on-chain seq-data:

```
"successor": "<successor holder ID, 32-byte hex>"
```

A previous `successor`, if any, is **replaced**. **Real control does not move** —
the chain output stays `sigLock(currentController)`, milestones are still signed
by the current key. GRANT only declares intent and is freely reversible (re-grant
to a different ID, or to the current controller / empty to revoke).

Authorization: `SenderID == currentControllerHolderID`. Same gate as `set-params`.

### Stage 2 — CLAIM (by the successor)

The successor sends a **claim** request from a wallet that holds the successor
key. The sequencer checks `SenderID == seqData.successor`; if it does not match
(or no successor is set) the request is **not authorized and fails**. On success
the sequencer produces a **handoff milestone** in which:

1. the chain output lock becomes `sigLock(successorHolderID)`, and
2. the `successor` key is **deleted** from seq-data.

The handoff milestone is still built and **signed by the current (old) key** —
the predecessor is `sigLock(old)`, so only the old key can transition it. This is
the last milestone the old key can produce.

Authorization: `SenderID == seqData.successor`.

## 3. Why this is safe

- **A CLAIM cannot be forged.** `SenderID` is pinned by the `tagAlong`
  constraint to the signer of the transaction that produced the request output.
  So a valid CLAIM proves the sender **possesses the successor private key**.
- **A CLAIM cannot be produced by a non-existent / unfunded holder.** Emitting
  any tag-along needs tokens (the fee) and the *sender-must-be-known* rule
  refuses the first transaction from a holder ID that owns nothing in the LRB
  (`core/resilience.md`). So a holder ID that does not exist, or a fat-fingered
  address nobody controls, **can never claim**. A GRANT to the wrong ID is inert:
  control stays with the current controller, who simply re-grants.
- **Control moves atomically and exactly once**, at the handoff milestone, under
  the current controller's signature — never before a live successor claims.
- This is a **safety/usability** property, not a trustless boundary against a
  *malicious current controller*: the current controller already has full
  control and could brick or drain the chain regardless. The protocol prevents
  *accidents*, and gives an honest handoff a clean, auditable path.

## 4. No ledger (EasyFL) change

The `chain` constraint enforces ChainID preservation, the inflation cumulatives
and the counters — it does **not** pin the output lock across a transit. So a
milestone whose chain output lock differs from the input lock is already valid,
provided the transaction unlocks the predecessor (signs with the old key). The
sequencer constraint marks the chain as a sequencer and reads the output's
balance/frozen-coverage, not its lock. Therefore succession is enforced entirely
in the sequencer's Go command handling and seq-data; **no constraint, no
hardfork.**

Verified on `develop` (2026-09-23), so the handoff disturbs nothing else:

- **Branches survive the key change.** The stem's VRF proof is verified under
  the *transaction signer's* key (`vrfVerify(signaturePublicKey(txSignatureData),
  ...)` in `lock_stem.easyfl`), per branch, not under a key inherited from the
  predecessor stem. The successor's first branch proves under its own key.
- **Delegations are unaffected.** `delegateLock` addresses its target by chain
  ID (index value 1), not by the controller's holder ID.
- **The tag-along backlog is unaffected.** It listens on the chain-lock account
  of the seqID (`ListenToControllerAccount(ChainLockFromChainID(seqID))`), and
  the tag-along lock's target unlock path only requires the target chain's
  transition in the same transaction — any signer, so the old key's milestone
  consumes the CLAIM.

## 5. On-chain state — `SequencerData.Successor`

Add one field to `sequencer/seqdata/seqdata.go`:

```go
Successor string `json:"successor,omitempty"` // 32-byte holder ID, hex; empty = none
```

- Add `"successor"` to `knownKeys`.
- Add `Successor()`, `SetSuccessor(holderID)`, `ClearSuccessor()` helpers.
- The field is **carried forward across milestones** like every other seq-data
  field (the milestone builder re-emits `nextSeqData`). It is mutated **only** by
  GRANT (set/replace) and CLAIM (clear).
- **`set-params` must preserve it, enforced on the sequencer side.** Today
  `SetSequencerDataTxBuilderCommand.Apply` replaces the seq-data wholesale
  (`txb.nextSeqData = c.SequencerData.Clone()`), and `proxi node seq set-params
  --json` can carry any key — including `successor`, and a wallet built before
  this feature would re-emit it verbatim through `extra`. So the rule cannot be
  left to the wallet: `Apply` must take the new data from the request and then
  copy `Successor` from the *pending* `txb.nextSeqData`, whatever the request
  carried. Only GRANT and CLAIM touch the field. Taking it from the pending
  `nextSeqData` rather than from the chain input also settles the order when a
  GRANT and a set-params land in the same milestone.

## 6. Commands

Two new request codes in `sequencer/txbuilder_seq/` (0=noop, 1=withdraw,
2=set-seq-data, 3=askstop are taken on `develop`; **4 is the delegation top-up
on `develop-take1`** — `kb/delegation_topup.md` — and must stay free on
`develop` so the merge is clean):

| Code | Name | File | Auth | Effect |
|------|------|------|------|--------|
| 5 | `RequestCodeGrantSuccessor` | `req_grant.go` | `SenderID == currentController` | set `seqData.successor` = param (replace/clear) |
| 6 | `RequestCodeClaimSuccession` | `req_claim.go` | `SenderID == seqData.successor` (must be set) | flip chain output lock → `sigLock(successor)`, clear `seqData.successor` |

Dispatch: add both to `_cmdParsers` in `sequencer/txbuilder_seq/parse.go`.

**At most one succession command per milestone.** Backlog iteration order is
not fixed, so a GRANT and a CLAIM (or two GRANTs) in one milestone would give an
order-dependent chain. The first succession command applied marks the builder;
a second one returns `valid=true` with an error, which the proposer treats as
*temporarily skipped* (no blacklist), so it is retried in a later milestone —
where, after a handoff, the new controller's node rejects a stale GRANT as
unauthorised and the sender reclaims it. CLAIM authorization reads
`txb.origSeqData.Successor` (the chain input's data), never a grant applied in
the same milestone.

### GRANT (`req_grant.go`)
- Request params: one field carrying the 32-byte successor holder ID (a field
  key, e.g. `'s'`). An empty value = **revoke** (clear any pending grant).
- Parser: reject if `NumElements != 4`; require `SenderID == HolderIDFromPublicKey(txb.signatureType, txb.publicKey)`; validate the holder-ID length (32) if non-empty; **reject a non-empty successor equal to the current controller** (§12 — self-grant is a likely mistake; revoke is the empty grant).
- `Apply`: consume the tag-along as a fee (like `set-params`); set
  `txb.nextSeqData = current.Clone(SetSuccessor / ClearSuccessor)`.
- Adds no extra outputs.

### CLAIM (`req_claim.go`)
- Request params: none needed — the claimer is the tag-along `SenderID`.
- Parser: reject if no `successor` set (`err = "no pending grant"`); require
  `o.SenderID == seqData.successor` else **authorization failure**.
- `Apply`: consume the tag-along; instruct the milestone builder to (a) set the
  chain output lock to `sigLock(successor)` and (b) clear `successor` on
  `nextSeqData`. Concretely, add a `txb.nextControllerHolderID *base.HolderID`
  the builder consults in `buildSequencerAndStemOutputs` in place of the default
  `o.PutLock(txb.chainInput.Output.Lock())` re-emit.
- The CLAIM lives as long as any tag-along: the target may consume it only
  within `constTagAlongSlots` (30 slots) of its creation. If every handoff loses
  its fork for 30 slots the claim expires; the successor reclaims it after the
  window and sends a new one. The backlog drops it as "missed tag-along window".

## 7. Operational handoff (node lifecycle)

This is the part the operators must get right; the node must handle it, not
crash-loop — and, crucially, **must not give up on the first handoff milestone.**

### Why the naive "stop after emitting the handoff" is wrong

The tangle forks. When the old node processes the CLAIM it emits a handoff
milestone `M_h` (chain lock → `sigLock(successor)`) consuming the current chain
tip `T0`. But another milestone can consume the same `T0` and keep
`sigLock(old)` — `M_h` and that milestone **conflict** (both spend `T0`'s chain
output), so the chain forks and only one lineage survives in the LRB. Two
outcomes:

- `M_h`'s lineage wins the LRB → control genuinely transferred.
- `M_h` is **orphaned** → on the winning lineage the chain tip is still
  `sigLock(old)` (`T0` or a sibling), and the CLAIM tag-along it consumed is
  **unspent again**.

If the old node stops the instant it emits `M_h` and `M_h` is then orphaned,
nobody is left who can extend the surviving `sigLock(old)` tip — the successor
cannot (no old key), the old node has quit — and **the chain stalls with its
balance stuck.** So the give-up trigger cannot be "I produced a handoff"; it must
be "the handoff is **confirmed on the LRB**."

### The correct policy — give up only on LRB change

The LRB is the single source of truth for who controls the chain. The sequencer
already extends the LRB lineage; make the controller decision follow from it:

- **Old node keeps sequencing normally while the LRB chain tip is
  `sigLock(old)`.** Each milestone it builds on that tip; whenever the CLAIM
  tag-along is present and unspent on the LRB it emits a handoff (consuming it).
  If a handoff is orphaned, the tag-along re-appears on the new LRB tip and the
  next milestone re-emits the handoff. The old node thus **keeps the chain alive
  and keeps attempting the handoff** until one sticks — no stall, no premature
  quit.
- **Give-up condition (required new behavior):** the old node stops sequencing
  **only when the LRB's chain tip for this seqID is locked to a holder ID that is
  not its own** — i.e. the handoff is reliably confirmed. Equivalently: it stops
  when it can no longer build a milestone extending the LRB because it lacks the
  tip's key. It logs the transfer (see §8) and does not retry (retrying against a
  `sigLock(successor)` LRB tip would just fail validation). The operator then
  **stops the old node** — it can never be this sequencer again.
- **Predecessor gate (required new behavior, the other half of the rule).**
  Today nothing on the proposal path compares the predecessor's lock with the
  node's key; the only such check is `checkSequencerStartOutput`, at startup.
  After the old node emits `M_h`, its own latest milestone *is* `M_h`, the
  factory extends it, the built transaction fails full validation in `makeTx`
  (the milestone is validated before submission) and this repeats every pulse
  with a warning until the LRB moves. So `txbuilder_seq.New` must refuse a
  predecessor whose lock is not controlled by the configured key — the single
  choke point every proposer goes through. With `M_h` refused, the factory's
  branch-anchored fallback (phase 2 of `extend_endorse.go`) supplies an older
  own-locked tip when the handoff lineage is not the one being extended, which
  is exactly the re-emit path above; when every candidate descends from `M_h`
  the node produces nothing until the LRB either confirms it (stop) or drops it
  (resume). Note the fork in this scenario is the old node's own doing: it
  re-anchors to the LRB lineage when its latest milestones are not in it, and a
  re-anchored milestone on `T0` conflicts with `M_h`.
- **Where the stop lives.** `doSequencerSlot` returns false and the loop exits,
  exactly as the `MaxBranches` limit does today ("reached max limit of branch
  milestones -> stopping"). The check runs at the top of `doSequencerSlot`:
  read the chain output from the LRB state, compare its lock with the node's
  holder ID. The node process keeps running as an access node; `OnExitOnce`
  hooks fire. There is no other runtime give-up in the loop today (a missing
  chain output only yields `ErrNoProposals`, and the loop keeps ticking).
- **New node — running with the new key before the claim.** The successor runs a
  node with the successor private key and the **same** seqID, synced, before
  claiming. While the LRB tip is `sigLock(old)` it is idle (it cannot extend a
  tip whose key it lacks — the mirror of the old node's give-up check). The
  moment the LRB tip becomes `sigLock(successor)` its key matches and it **takes
  over**.
- **Waiting mode (required new behavior).** Today this idle state does not
  exist: `ensureFirstMilestone` → `checkSequencerStartOutput` refuses to start
  when the key does not match the chain output's lock, the sequencer goroutine
  exits after an error log, and the node runs on without a sequencer. The
  startup check must gain one branch: when the lock does not match **and the
  chain output's `successor` equals this node's own holder ID**, log once
  (§8) and poll the LRB chain output once per slot until its lock matches, then
  proceed with the confirmed handoff milestone as the start output. Any other
  mismatch keeps refusing as today. (The existing error line reads "provided
  private key does match sequencer lock"; fix the missing "not" in passing.)

Both nodes decide purely from the LRB tip's lock holder, so at any confirmed
state exactly one of them can extend it. Both may run at once during the
transition; only the one matching the current LRB tip produces. There is a brief
pause between the confirming handoff and the successor's first milestone — keep
it short by having the successor node synced and running before the claim. This
is a planned, operator-coordinated transition, not an automatic failover.

**The old sequencer node with the old key must be stopped, and a new node
started with the new key** — but the *software* decides *when* it has lost
control from the LRB, so an orphaned handoff never bricks or stalls the chain.

## 8. Logging (auditability)

The controller change must be plainly visible in the sequencer log:

- On GRANT: `SUCCESSION: granted successor <holderID> (was <prev|none>)`.
- On the successor node entering the waiting mode: `SUCCESSION: this node's key <holder> is the designated successor of chain <seqID>; waiting for the handoff to be confirmed on the LRB`.
- On emitting a handoff milestone: `SUCCESSION: handoff milestone <txid> flips control -> <successor> (pending LRB confirmation)`.
- On LRB-confirmed transfer (old node giving up): `SUCCESSION: LRB tip of chain <seqID> now controlled by <successor>; this node's key <old> can no longer sequence; stopping sequencer`.

Handoff re-attempts after an orphaned handoff are ordinary milestone activity
and are not logged as succession events (avoid noise); only the emit and the
final LRB-confirmed transfer are.

These are `Infof`-level, one line each, no trace tags — a control change is a
rare, important event.

## 9. Edge cases

| Case | Behavior |
|------|----------|
| GRANT to a typo / dead holder ID | inert; nobody can claim; control stays; re-grant to fix |
| Re-GRANT before a claim | replaces the pending successor |
| Revoke (GRANT empty) | clears the pending successor |
| CLAIM with no grant set | rejected (`no pending grant`) |
| CLAIM by a holder ≠ granted successor | rejected (authorization failure) |
| GRANT to self (current controller) | rejected — likely a mistake; revoke is the empty grant (§12) |
| Handoff milestone orphaned by a fork | CLAIM tag-along re-appears unspent on the new LRB tip; old node re-emits the handoff on its next milestone; nothing stalls (§7) |
| Successor claims but its node isn't ready | old node keeps sequencing on the `sigLock(old)` LRB tip until it can hand off; once the handoff is on the LRB and the successor node is up, it takes over; chain pauses only in the gap |
| `set-params` issued while a grant is pending | `successor` preserved untouched, enforced in the sequencer's `Apply` (§5); `--json` cannot set or clear it |
| Two different holders try to claim | only the one matching `seqData.successor` succeeds |
| GRANT and CLAIM (or two GRANTs) in one milestone | the first applied wins, the second is temporarily skipped and retried in a later milestone (§6) |
| Handoffs keep losing forks for 30 slots | the CLAIM leaves the tag-along window and is dropped from the backlog; the successor reclaims it and sends a new one (§6) |
| Successor node started before the grant, or with a key that is not the designated successor | refused at startup as today; the waiting mode (§7) applies only when `successor` equals the node's own holder ID |

## 10. proxi CLI — `proxi node seq`

Two new subcommands under `proxi/node_cmd/seq_cmd/` (siblings of
`withdraw`, `set-params`, `info`):

- **`proxi node seq grant <successor-holder-id>`** — run by the **current
  controller** from a wallet holding the controller key. Builds a GRANT tag-along
  to the sequencer (target = seqID, sender = controller). `--revoke` (or an empty
  arg) clears the pending grant. Prints the resulting pending successor.
  - Follows the wasm-wallet rules (CLAUDE.md): build via `glb.GetTxLibrary()` /
    `glb.GetLedgerConstants()`, submit via `glb.SubmitAndDisplay`. Model on
    `set.go` / `withdraw.go`.
- **`proxi node seq claim <sequencer-id>`** — run by the **successor** from a
  wallet holding the successor key. Builds a CLAIM tag-along to the sequencer
  (target = seqID, sender = the successor = the wallet's own holder ID), at the
  ordinary tag-along min-fee. **Requires an explicit confirmation** ("this
  transfers control of sequencer <id> to this wallet"), skippable with
  `--force`/`-y`, and warns that the successor's node must already be running
  with this key and the same seqID before the claim settles.

Both surface the transaction and its outcome; `seq info` should additionally
show a pending `successor` when present.

## 11. Implementation checklist

- `sequencer/seqdata/seqdata.go`: `Successor` field, `knownKeys`, get/set/clear.
- `sequencer/txbuilder_seq/req_grant.go`: `RequestCodeGrantSuccessor`, parser
  (auth = current controller), `Apply` (set/clear successor), `NewGrantRequestOutput`.
- `sequencer/txbuilder_seq/req_claim.go`: `RequestCodeClaimSuccession`, parser
  (auth = seqData.successor), `Apply` (flip lock + clear successor),
  `NewClaimRequestOutput`.
- `sequencer/txbuilder_seq/parse.go`: register codes 5 and 6 in `_cmdParsers`
  (4 stays free for the take 1 top-up).
- `sequencer/txbuilder_seq/req_seqdata.go`: `Apply` copies `Successor` from the
  pending `nextSeqData` onto the request's data (§5).
- `sequencer/txbuilder_seq/txbuilder_seq.go`: `nextControllerHolderID` override
  in `buildSequencerAndStemOutputs`; `nextSeqData` already carries every field
  forward by default (`ret.nextSeqData = ret.origSeqData.Clone()` in `New`);
  the one-succession-command-per-milestone mark (§6); **`New` refuses a
  predecessor not controlled by the configured key** (§7). Every proposer path
  (base, branch, bootstrap, factory) constructs through `New`; the four callers
  in `tests/` all use matching keys.
- `sequencer/sequencer.go`: the LRB give-up check at the top of
  `doSequencerSlot`, returning false like the `MaxBranches` limit; the waiting
  mode in `ensureFirstMilestone` for a node whose key is the designated
  successor (§7); the shared "is this output controlled by holder X" predicate
  reused by `checkSequencerStartOutput`, `New` and both new checks. **Not** an
  eager stop on the first handoff milestone — an orphaned handoff must not stall
  the chain.
- Logging lines (§8).
- `proxi/node_cmd/seq_cmd/grant.go`, `proxi/node_cmd/seq_cmd/claim.go`; register
  in `seq.go`. `info.go` needs no change: `printSequencerData` lists every key
  of the seq-data JSON, so a pending `successor` shows as soon as the field
  exists. Model both on `set.go`; the wallet-side pre-checks (grant: not the
  current controller; claim: the wallet's holder ID equals the chain's
  `successor`) are conveniences, the sequencer's parsers are the authority.
- Tests: grant sets successor; set-params preserves it (including a request
  that carries a foreign `successor` value); claim by wrong sender rejected;
  claim with no grant rejected; successful claim flips the chain lock and clears
  successor; a second succession command in the same milestone is deferred;
  `New` refuses a predecessor locked to another key; old-key milestone against
  the flipped chain is rejected by validation (bricking-avoidance is the whole
  point — assert the *new* key works and the *old* key does not). Builder-level
  tests follow `tests/txbuilder_seq_test.go` (`AddTagAlongInput` +
  `txbtest.BuildAndValidate`); run the `tests/` sequencer tests one at a time
  and the core changes under `-race`.

## 12. Decisions (settled)

- **Self-grant is rejected.** GRANT with a non-empty successor equal to the
  current controller's holder ID is refused with a clear error
  (`grant: successor equals the current controller — to revoke use an empty
  grant`). Rationale: it is never useful (the revoke is the *empty* grant, which
  leaves `successor` cleared rather than set to self), and refusing it catches
  the likely fat-finger of pasting one's own address instead of the successor's
  — squarely the mistake-prevention this protocol is for. Revoke stays the
  explicit empty / `--revoke` grant.
- **CLAIM uses the ordinary tag-along minimum fee** (the sequencer's configured
  `MinFee`), no special higher amount. The two-phase grant plus the
  `SenderID == seqData.successor` auth already make a claim deliberate and
  narrowly authorized; a larger fee would add cost, not safety. Deliberateness
  belongs in the CLI instead: `proxi node seq claim` **requires an explicit
  confirmation** ("this transfers control of sequencer <id> to this wallet"),
  skippable with `--force`/`-y`.
- **The pending successor is public, and `info` shows it.** It lives in the
  on-chain milestone seq-data, so it is already visible to anyone reading the
  chain; hiding it in the CLI would be security-by-obscurity. Surfacing it in
  `proxi node seq info` is a feature: succession is transparent and auditable —
  who is next in line for a piece of infrastructure is something the network
  benefits from seeing.

Settled by the 2026-09-23 review against `develop`:

- **Request codes are 5 (grant) and 6 (claim).** Code 4 is the delegation
  top-up on `develop-take1` and stays free on `develop`.
- **Successor preservation is the sequencer's job, not the wallet's** (§5).
- **One succession command per milestone; the second is deferred** (§6).
- **The predecessor gate lives in `txbuilder_seq.New`** and the LRB stop at the
  top of `doSequencerSlot` (§7). The stop ends the sequencer loop only; the node
  keeps running.
- **The successor node waits only when it is the designated successor** (§7).
  Every other key/lock mismatch is refused at startup, unchanged.
- **Branch order.** Implement on `develop`, merge to `master` (currently at the
  same commit, a fast-forward) and into `develop-take1`, where the top-up
  touched `parse.go`, `seqdata.go` and `txbuilder_seq.go`: with codes 5 and 6
  the overlap is additive lines only.
