# Delegation scalability, coverage dips, and the fixed freeze grid

Status: **model measured against nothing; the changes in §8 and §9 are IMPLEMENTED**
(2026-08-15), landing at the next testnet reset. Written for the fair-launch plan, which
assumes mined capital gets delegated. Parameters are read from the code and the conclusions
are arithmetic on them — none of the load figures has been observed on a running network,
and the measurements to take are listed in §10.

The ledger changes are breaking (LibraryHash + genesis), which is acceptable: the reset is
happening anyway for other pending ledger fixes.

**Terminology note.** Throughout, the fixed freeze depth is **60 epochs** (≈ 4.3 days), not
60 slots. Epochs are 600 slots each; a 60-*slot* freeze would be shorter than one epoch and
incoherent with the epoch grid.

---

## 1. Parameters in force (before the change)

| Symbol | Value | Where |
|---|---|---|
| τ | 10.24 s/slot | ledger |
| `S_e` epoch length | **600 slots** = 1.707 h (range [500, 2000]) | `constDelegationEpochSlots`, `sequencer.easyfl` |
| `N` max frozen epochs | **35** (min 8), genesis uses the max | `constDelegationMaxFrozenEpochsMax/Min` |
| max freeze duration | 35 × 1.707 h ≈ 59.7 h (2.5 days) | derived |
| `W` safe revocation window | **60 slots** = 10.2 min | `constDelegationSafeRevocationSlots` |
| `C_max` frozen delegations per epoch, per sequencer | **300** | `defaultMaxFrozenDelegations` — node config, not consensus |
| attachment cost budget | 550 per tx (> 256 in + 256 out) | `defaultAttachmentCostBudget` |
| storage deposit | `size ≥ 100 → size × 250 000 − 20 000 000` motes | `storageDeposit`, `def_constants0.json` |
| epoch offset | `targetChainID[0:3] mod S_e` | `delegationEpochOffset`, `lock_delegate.easyfl` |

---

## 2. The epoch grid is per **sequencer**, not per delegation

`lock_delegate.easyfl` anchors every epoch computation on `_selfTargetChainID` —
`_successorEpoch`, `_selfLastSlotInLastFrozenEpoch`, `_txEpoch` all use it, and
`sequencer/task/proposal.go:335` calls `EpochFromSlotDirect(p.SequencerID(), …)`.

So **all delegations pointing at one sequencer share one phase and unfreeze together.**
Load is clustered per sequencer and decorrelated across sequencers (offsets are
`targetChainID[0:3] mod 600`, effectively pseudorandom per sequencer).

This is the opposite of the smooth per-delegation spreading an earlier draft of this note
assumed, and it is what makes §3 a real question rather than a non-issue.

---

## 3. Coverage dips

A sequencer's re-freezing is a square wave, not a trickle:

- **period** — one epoch, 600 slots = **1.71 h**;
- **duration** — at least the safe revocation window `W` = 60 slots = **10.2 min**. This is
  structural: `lock_delegate.easyfl:317` forbids the target from unlocking a delegation
  inside the window (`delegation_cannot_be_unlocked_by_the_target_in_safe_revocation_window`),
  so the sequencer *cannot* re-freeze early even if it wants to;
- **duty cycle** — `W / S_e` = **10 %**;
- **depth per sequencer** — the delegations expiring at that boundary, ≈ `1/N` of the
  sequencer's delegated capital in steady state.

During the window that capital is unfrozen and **contributes no coverage**.

### Amplitude across the network

With `n_s` sequencers of comparable size and uniform phases, the number simultaneously
in-window is `K ~ Binomial(n_s, 0.1)`, and the unfrozen fraction of *delegated* capital is
`(K / n_s) × (1/N)`:

| `n_s` | N=35 mean | N=35 tail | N=60 mean | N=60 tail |
|---|---|---|---|---|
| 100 | 0.29 % | 0.54 % (K=19, +3σ) | 0.17 % | 0.32 % |
| 10 | 0.29 % | ~0.86 % (K=3, p≈0.07) | 0.17 % | ~0.50 % |
| 5 | 0.29 % | ~1.14 % (K=2, p≈0.07) | 0.17 % | ~0.67 % |

The dip on **total coverage** is smaller still, scaled by the delegated share of coverage —
the bootstrap capital and sequencers' own capital do not dip.

**Answer to the question: dips are real, structural and periodic, but under ~1 %.** Fewer
sequencers means deeper dips (each is a larger share of the total), not shallower.

### Where it does matter

At the 5/12 and 7/12 crossings the network is by definition operating near the health
boundary. A periodic sub-1 % dip there can cause intermittent branch failures exactly at the
handover. **This argues for crossing the thresholds with margin rather than barely**, and it
belongs in the launch narrative, not just here.

---

## 4. Per-sequencer capacity

Each delegation occupies exactly one unfreeze epoch in `[txEpoch, txEpoch + N − 1]`;
`latestArgminUnderCap` (`sequencer/task/proposal.go`) places it in the latest least-loaded
epoch under the cap.

    concurrent capacity   D_max = C_max × N        = 300 × 35 = 10 500   (60 → 18 000)
    servicing rate        C_max per epoch          = 300 / 1.71 h
    burst                 C_max transitions per epoch boundary, inside a 60-slot window

The burst is the number that matters, not the average: **300 transitions inside 60 slots =
5 per slot**, then nothing for 540 slots. Against a per-milestone ceiling of ~250 transitions
(256-output limit, 550 cost budget), 5/slot is still ~2 % of capacity, so delegation work is
not transaction-bound even in burst.

`C_max` is **node config, not consensus** — raising it raises capacity linearly with no fork.

---

## 5. Demand: how many delegations do miners create?

900 000 transits over the emission, `n_m` miners.

| Miner policy | Delegations | Per sequencer at `n_s`=100 | % of capacity (N=35) |
|---|---|---|---|
| A — delegate every payout | up to 900 000 | 9 000 | 86 % |
| B — delegate every 100 transits | 9 000 | 90 | 0.9 % |
| C — cap 20 per miner, add to existing | `n_m` × 20 = **2 000** | 20 | **0.2 %** |

Policy A needs ≥ 86 sequencers just to fit and leaves no margin. B and C are far inside
capacity. The per-miner cap is therefore **not needed for throughput** — its value is state
size (§7) and robustness.

### 5.2 Post-mining is the harder case, and the one that actually binds

Mining is a 14-month episode. Delegation is permanent, and after the mine chain is dead it
is the **only** way an ordinary holder participates in consensus. Demand then scales with the
holder population, not with a miner count bounded at ~100 — and the network does not merely
tolerate broad delegation, it **requires** it: more than 7/12 of supply must be actively
participating or branches stop being produced.

Take ~1 B PROX supply, so ~583 M PROX must participate. Capacity is
`n_s × C_max × N` = 100 × 300 × 60 = **1.8 M delegations**:

| Average delegation | Delegations needed | vs capacity |
|---|---|---|
| 10 000 PROX | 58 000 | 3 % |
| 1 000 PROX | 583 000 | 32 % |
| 100 PROX | 5.8 M | **exceeds capacity 3×** |

**This is the first place anything in this document actually binds.** During mining, capacity
is used at 0.2 %; post-mining, a participant base of many small holders runs into the ceiling.

Note the coupling with §8.4, because it cuts both ways: the storage deposit sets a floor on
delegation size, and that floor *protects* capacity by pricing out the smallest delegations.
Lowering it — good for participation breadth, and good for the 7/12 requirement — increases
the delegation count and pushes on the capacity ceiling instead. The encoding change moves
the pressure from the deposit floor to the capacity ceiling; it does not remove it.

Levers, in order of preference: more sequencers (`n_s`, permissionless and self-scaling),
higher `C_max` (node config, no fork, but raises the per-boundary burst), larger `N` (now
cheap, §8.3). Raising the minimum delegation size is a lever too, and the wrong one — it
excludes small holders from consensus and works against the participation floor.

---

## 6. Refusal is self-correcting

When every reachable epoch is at `C_max`, `latestArgminUnderCap` returns not-ok and the
freeze is refused; the delegation stays unfrozen and is retried later.

Refusal pushes the delegator to another sequencer, which **evens the distribution** — it is
negative feedback, not a runaway. That downgrades concentration from a headline failure mode
to a client requirement:

- the delegator's client must **detect refusal and retry elsewhere** (not believed to be
  implemented in `proxi node mine` today);
- the reference miner's choice of a **random** alive sequencer is load-balancing, and should
  be documented as such rather than left looking like a placeholder.

The residual risk is only the case where refusals are widespread enough that unfrozen
capital drops total coverage below 7/12 before the rebalancing completes.

---

## 7. State growth

Estimated, not measured. A delegation UTXO carries amounts, index-values (masterID 32 B +
targetChainID 24 B), the delegate lock with args, the chain constraint, and the lock state —
plus the frozen-coverage vector (§8.2). Effective size ≈ 490 B at N=35.

| Delegations | UTXO data | With trie overhead (2–4×) |
|---|---|---|
| 2 000 (policy C) | ~1 MB | ~2–4 MB |
| 9 000 (policy B) | ~4.4 MB | ~9–18 MB |
| 900 000 (policy A) | ~440 MB | **~1–1.8 GB** |

Bearable at the top end, but not free — and this, more than capacity, is the argument for
**add-to-delegation**: it buys two to three orders of magnitude of permanent state.

---

## 8. Spec: fix the freeze grid, remove the dials

**Decision.** Remove delegation-parameter configurability entirely. Fix globally:

    constDelegationEpochSlots      = 600   (unchanged, now non-negotiable)
    constDelegationMaxFrozenEpochs = 60    (replaces the Min=8 / Max=35 range)

Breaking ledger change (LibraryHash + genesis). Targets the next testnet reset.

### 8.1 Why

- **Capacity +71 %** — `C_max × N` = 300 × 60 = 18 000 per sequencer.
- **Dip amplitude −42 %** — `1/60` vs `1/35` (§3).
- **Kills an unpriced common-pool dial.** A delegator choosing `N = 8` for agility consumed
  4.4× more network capacity per delegation than one choosing 35, while the servicing rate
  stayed pinned at `C_max` per epoch. Nothing priced this. Fixing `N` internalizes it by
  fiat instead of building a pricing mechanism.
- **Removes a griefing vector** — mass short-depth delegation as a capacity squeeze.
- **Simplifies** the lock, the sequencer constraint, the builders, the wallet and the CLI.

Cost to delegators: max lock 4.3 days instead of 2.5, with `askstop` as the escape hatch —
which returns an unearned advance rather than charging a fee, see §9.

Phases stay decorrelated across sequencers even with a globally fixed period, because the
offset is `targetChainID[0:3] mod 600`.

### 8.2 The storage-deposit consequence — on **both** sides

The frozen-coverage vector occupies amounts indices `AmountIndexFrozenCoverage … +N−1`. It
sits on the sequencer's chain output *and* on the delegation output — `req_askstop.go` reads
`oProduce.Amounts()`, the produced delegation output, for the negative unfreeze deltas. So
raising N charges every delegation, not just every sequencer.

Under the **current** encoding a max-depth frozen delegation carries a run of N identical
non-zero entries, and an askstop-produced output carries a run of N identical *negative*
deltas. Neither compresses, because only trailing **zeros** are stripped. At ~7 B per entry
that is ~420 B of vector at N=60, ≈ **105 PROX** of extra storage deposit per delegation.

§8.3 removes this. The rest of this subsection assumes the encoding change lands with it;
without it, raising N to 60 is a real cost on every delegation and the case for 60 is much
weaker.

### 8.3 Decouple the logical vector from its encoding — the last-element-repeats rule

**The fix is an encoding change, not a model change.** Keep the logical model exactly as it
is — a length-N vector addressed from EasyFL at fixed indices, summed uniformly at the
transaction layer. Change only the serialization rule:

> **The value of the last encoded element repeats to the end of the vector, and an empty
> vector means the all-zero vector.**

This is a strict generalization of what already exists. `NewAmounts` already strips trailing
zeros, and `amountAt` already returns `u64/0` past the end of the tuple — i.e. the current
rule is *"the tail is implicitly zero"*, the special case where the repeated value is 0. The
second clause is what makes the first well-defined: an empty vector has no last element, so
its implied tail must be named, and 0 keeps today's behaviour.

**Scope the rule to the frozen-coverage sub-vector, not the whole amounts tuple.** The
amounts tuple is really two scalars followed by a vector — `[0]` balance, `[1]` inflation,
`[2…]` frozen coverage — and the repeat belongs only to the third part:

    index 0, 1     past the end ⇒ 0                       (unchanged)
    index ≥ 2      past the end ⇒ last encoded element,
                                  or 0 if the tuple has no cell at index ≥ 2

Applying it to the whole tuple would be simpler to state but would break every ordinary
output: `[balance]` would mean inflation = balance, and `[balance, inflation]` would mean
frozen coverage = inflation in every epoch. Both wrong, so each would need a padding `0` —
**+1 cell on essentially every UTXO in the ledger**, permanently, against a saving that only
accrues to delegation and sequencer outputs. Scoping costs one comparison; the delegation
encodings below are identical either way.

Effect, exactly as intended:

| Vector | Logical | Encoded now | Encoded with the rule |
|---|---|---|---|
| delegation frozen to max depth | `[A × N]` | **N entries** | **1** |
| askstop-produced negative deltas | `[−A × N]` | **N entries** | **1** |
| delegation frozen to depth `e < N` | `[A × (e+1), 0 …]` | e+1 | e+2 |
| sequencer aggregate (decreasing staircase, ends at 0) | varied | ~N | ~N **+1** |

So the delegator side collapses to a single element in the common cases, and the sequencer
pays exactly one extra element — the explicit trailing `0` needed to stop the last non-zero
value repeating forever.

**Change surface is very small:**

- `ledger/def/amounts.easyfl` — `amountAt` returns the last element instead of `u64/0` when
  the index is past the tuple: `if(isZero(tupleLen($0)), u64/0, atTuple8($0, tupleLen($0)−1))`.
  This is the **only** EasyFL change; every caller keeps fixed-index access unchanged.
- `ledger/amounts.go` — `NewAmounts` strips the trailing *run of equal values* instead of the
  trailing run of zeros; `Amount(i)` mirrors the EasyFL rule past the end.
- Vector summation at the transaction layer — sum over `max(L₁, L₂)` using the rule, then
  re-canonicalize.

**No canonical-form rule is needed.** `[A]` and `[A, A]` decode to the same logical vector,
but the decode rule is total — every byte string maps to exactly one logical vector — so
there is no ambiguity for consensus to resolve. Padding is self-penalizing: more cells means
more bytes means a higher storage deposit, paid by whoever padded. `NewAmounts` will emit the
short form because that is the cheap one, and every other wallet has the same incentive. Do
not add a constraint enforcing it; the economics already do, and summation code is simpler if
it may emit a redundant tail without re-canonicalizing.

One place already fails closed for free: `evalIsInflationAndFrozenCoverageZero` treats
`NumElements ≤ AmountIndexFrozenCoverage` as "frozen coverage is all zero". A padded
non-chained output (`[balance, inflation, 0]`) is rejected by that check even though its
frozen coverage is genuinely zero — a conservative refusal that costs only the padder. That
is enough; the predicate needs no change.

**Consequences for N.** With the delegator side compressed, N is nearly free for delegations
and costs the sequencer ~1 entry per epoch of depth — trivial against a sequencer's balance.
The binding constraint on N stops being storage and becomes **delegator agility** (60 epochs
≈ 4.3 days of maximum lock). 60 is a judgement on that axis, not a storage compromise, and a
larger N would now be affordable if the dip amplitude (§3) or capacity (§4) argued for it.

### 8.4 What it does to the participation floor

The storage deposit is the **minimum viable delegation size** — the smallest stake that can
participate in consensus at all. Rough estimate for a delegation output (base ≈ 240 B
including per-index-value trie cost, plus the vector):

| | vector | effective size | minimum delegation |
|---|---|---|---|
| N=60, current encoding | ~420 B | ~660 B | **~145 PROX** |
| N=60, last-element-repeats | ~7 B | ~247 B | **~42 PROX** |

Roughly a **3–4× lower floor** (the estimate is soft, ±50 %; measuring one real delegation
output settles it). That matters far more after mining ends than during it — see §5.2.

### 8.5 Blast radius

44 non-test files reference `maxFrozenEpochs` / `epochSlots`. The load-bearing ones:

- **EasyFL** — `def/lock_delegate.easyfl` (drop `$0 maxFrozenEpochs`, `$2 epochSlots`,
  `$3 targetMaxFrozenEpochs`; `_selfEpochSlots` / `_selfTargetMaxFrozenEpochs` read the
  global constants), `def/sequencer.easyfl` (drop the two delegation params from the
  sequencer constraint; delete `constDelegationEpochSlotsMin/Max`,
  `constDelegationMaxFrozenEpochsMin/Max` and `_validDelegationParamsBounds`).
- **Ledger Go** — `constants.go`, `genesis.go:38` (`NewSequencerConstraint` loses two args),
  `lock_delegate.go`, `lock_delegate_util.go`, `sequencer.go`, `chain.go`, `output.go`,
  `constraints_serde.go`, `transaction/parse.go`, `amounts.go`.
- **Sequencer** — `txbuilder_seq/txbuilder_seq.go` (`chainMaxFrozenEpochs` → constant),
  `txbuilder_seq/req_askstop.go`, `task/proposal.go` (`ChainDelegationParams()` goes away),
  `delegationpool/delegationpool.go` (per-entry `maxFrozenEpochs` goes away).
- **Wallet / API / CLI** — `txbuildercore/{constants,helpers_delegate,helpers_sequencer,
  output_layout}.go`, `api/{api,client,server,chain_explorer}`, `proxi/node_cmd/delegate/*`,
  `proxi/node_cmd/seq_cmd/init_seq.go`, `proxi/node_cmd/{chain,mine}.go`,
  `proxi/glb/display_chains.go`, `ledger/utxodb/*`, `examples/exhelp/builder.go`.

### 8.6 Also worth doing in the same pass

- Re-derive `defaultMaxFrozenDelegations` (300) against N=60 — capacity is now 18 000 per
  sequencer; whether 300/epoch is still the right self-protection level is a separate
  judgement, and it is config, not consensus.
- Make the miner **retry on refusal against a different sequencer** (§6).

---

## 9. Askstop: the advance and its return

**Implemented** 2026-08-15 (`delegateLockState` arity 2 → 3, freeze-time enforcement in
`lock_delegate.easyfl`, `_projectedCompensation` in `ensure.easyfl`, `AdvanceForShare` and
`SeqTxBuilder.advanceShare`).

### 9.1 What actually happens (it is an advance, not a fee)

`SeqTxBuilder.calcAdvance` — the name is the giveaway — pays the delegator **up front at
freeze time** for the whole frozen period:

```go
projectedInflation = ChainInflationMultiStep(balance, freezeSlot, frozenSlots)
advance            = projectedInflation × share / 1000
share              = delegatorRequirement           if seqdata.IsGreedy()
                     1000 − seqProfitMarginPromille otherwise   (≥ requirement)
```

So an early `askstop` is **not a penalty priced by remaining freeze time**. It returns an
advance the delegator received and has not earned. An earlier draft of this note described it
as a paid operation; that was wrong.

Note a non-greedy sequencer pays *more* than the delegator demanded — sequencers competing on
generosity, consistent with the prediction in §10 that competition pushes the cut toward
delegators.

### 9.2 The current asymmetry

| | formula | share applied |
|---|---|---|
| advance credited at freeze | `I(frozenSlots) × share/1000` | **yes** |
| compensation demanded on askstop | `I(lostSlots)` | **no — full 100 %** |

The delegator receives ~90 % of the projection but returns 100 % of the remainder. Revoking
straight after a freeze hands back ~1.0·I having received ~0.9·I, leaving the sequencer
holding exactly its margin for a period it never serviced, with its frozen-coverage slot
freed for reuse. That is a termination penalty, not an unwind.

### 9.3 Decision: pure unwind

    C = chainInflationMultiStep(balance, txSlot, lostSlots) × pinnedShare / 1000

The delegator returns exactly the unearned part of what it received, at the rate it was paid.
Nothing more.

Both sides then bear their own foregone inflation — the delegator loses the yield on the
remaining period, the sequencer loses its cut on the same period, and neither compensates the
other. That is the symmetric outcome, and the sequencer did no work for the unserved period,
so charging it for foregone profit would encode a contractual termination penalty the ledger
does not need.

**The decisive argument is the refreeze economy.** Any penalty is charged on *every*
add-to-delegation cycle, taxing exactly the loop miners should run routinely (§5, policy C).
Pure unwind leaves that loop costing only tag-along fees.

Rejected along the way: a 50/50 split of the lost cut conditioned on `seqdata.IsGreedy()`.
`Greedy` lives in `sequencer/seqdata/seqdata.go`, is read by **no** EasyFL constraint, and is
settable at any milestone via `proxi node seq set`. Making the exit price depend on it would
have created a bait-and-switch — freeze on generous terms, flip the flag, collect the
penalty — and would have been the design creating the incentive it then had to defend against.
Under a fixed rule, flipping the flag gains nothing.

### 9.4 Why the share must be pinned, not derived

The tempting shortcut is to reuse `RequiredInflationCut`, already on the delegation output and
immutable. **It is exploitable.** A non-greedy sequencer pays `seqTolerance/1000 × I`
regardless of what the delegator required, so a delegator sets `RequiredInflationCut = 0`,
points at a generous sequencer, collects a full advance, revokes immediately and returns
`0 × I = 0`, keeping the lot. Under a penalty rule that self-corrects; under pure unwind it is
clean theft.

Nor can the share be reconstructed at askstop time: neither the freeze slot nor the pre-freeze
balance survives (the advance is mixed into the balance, and `chain`'s `cumInflation` is
lifetime-cumulative, not per-freeze). So it must be written down when it is known.

### 9.5 Where it goes, and how it is enforced

**Where** — `delegateLockState`, the per-delegation mutable state already pinned to the last
tuple position and rewritten on every transition:

    delegateLockState(z32/lastFrozenEpoch, stateMark)            -> arity 2
    delegateLockState(z32/lastFrozenEpoch, stateMark, share)     -> arity 3

Roughly +2–3 bytes, against the ~413 bytes §8.3 returns on the same output.

**How it is enforced at freeze** — the constraint can *see* the advance, because it is the
balance the target added beyond the ordinary one-slot inflation:

    successorBalance = predecessorBalance + selfInflationAmount + advance

(An earlier draft of this note said the advance *was* `selfInflationAmount`. It is not:
`MakeDelegationFreezeOutput` declares `InflationOneSlot()` as the output's inflation and
adds the advance on top of it.) Everything else is already computed in
`lock_delegate.easyfl` — consumed balance, `txSlot`, and `frozenSlots` from
`_selfLastSlotInLastFrozenEpoch` — and `chainInflationMultiStep` is linear
(`N × A / (m0 + s)`). So the frozen produced arm requires:

    share >= RequiredInflationCut        // delegator's floor, already on the output
    predecessorBalance + selfInflationAmount <= successorBalance
    successorBalance - predecessorBalance - selfInflationAmount
        == requiredInflationAdvance(frozenSlots, txSlot, predecessorBalance, share)

Nothing to fake: pin a larger share and the larger advance must actually be paid; pin a
smaller one and the delegator's own required cut rejects it. No `seqdata`, no `greedy`,
nothing advertised — the pinned value is tied to money that moved in the same transaction.

This is a **strengthening**, not only a pin. The advance used to be computed entirely in the
sequencer's Go builder with no constraint checking it at all, and the old rule was an
inequality (`>=`), which let an advance covering a different span pass unnoticed.

### 9.6 Two details to get right

- **Rounding must agree on both sides.** Keep the multiplicative form above rather than
  dividing to recover the share. Settled by having the builder take the *share* and derive
  the advance from it (`DelegationOutput.AdvanceForShare`), so one function mirrors the
  constraint and the sequencer never hands over an absolute amount.
- **Basis.** Both the freeze enforcement and `AdvanceForShare` project from the **consumed**
  balance. The askstop unwind necessarily projects from the current (post-advance) balance
  over the remaining span, since the pre-freeze balance does not survive on the output; the
  pinned share is what makes that recoverable at all.

### 9.7 What the sequencer keeps

Fixing the amount in the ledger removes extortion — the sequencer cannot demand more — without
removing its backstop: it can always decline to include the askstop transaction at all, since
inclusion cannot be forced in a UTXO ledger. That remains the defence against a delegator
cycling freeze/revoke to grief it. `patienceMargin = 6` slots (refuse revocation within ~1 min
of natural unfreeze — "just wait") stays sensible as sequencer policy on top.

`Greedy` reverts to being purely an entry-side advertisement: it sets the advance you are
offered, and once frozen the pinned share governs everything. Flipping it afterwards affects
nothing.

---

## 10. Still open

- **Sequencer/delegator inflation cuts.** `RequiredInflationCut` is promille ≤ 1000; the
  reference miner uses 900. The marginal cost to a sequencer of one re-freeze is one input
  and one output in a milestone it was already producing — near zero — while the benefit is
  frozen coverage, which is what sequencers actually compete on. The prediction is therefore
  that competition drives the cut **up toward 1000**, and the interesting question is what
  stops it. Needs its own pass.
- **Add-to-delegation** (next document). §9 makes an early exit a pure unwind, so the
  askstop → deposit → re-delegate loop costs only tag-along fees. Timing an add at the
  natural unfreeze boundary avoids even the unwind. That shapes the design.
- **Past-cone attachment cost** across a slot's milestones during a re-freeze burst — the
  per-tx budget is checked, the past-cone aggregate is what `checkAttachmentCostBudget`
  guards, and it is not modelled here.
- **Measurements to take on the reset testnet**: realized delegation distribution across
  sequencers, refusal rate at the cap, dip depth in total coverage at epoch boundaries, and
  a real delegation UTXO's effective size (to replace the §7/§8.2 estimates).
