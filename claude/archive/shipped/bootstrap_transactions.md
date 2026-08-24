# Bootstrap transactions

Status: **implemented 2026-07-31.** §5 (bootstrap transactions) committed as
`57000760`; §3/§4 (the `health_relief` window replacing the boolean flag) done
on top of it. §4's *adaptive* threshold remains rejected — the reasoning is the
point of that section. Tests: `tests/bootstrap_transaction_test.go`,
`global/health_relief_test.go`.

Scope: the sequencer behaviour when no branches arrive from gossip and there is
nothing to endorse — today's "boot proposer". Covers naming, evidence logging,
freezing policy during bootstrap, tick placement, and the analysis of adaptive
healthiness relief (**rejected**, §4).

## 0. Decisions

| # | Decision |
|---|----------|
| 1 | "Bootstrap transaction" becomes the name in code and comments: a **non-branch sequencer transaction with an explicit baseline**. `boot` → `bootstrap` everywhere. |
| 2 | A submitted bootstrap transaction is logged with an explicit `BOOTSTRAP` marker at the **submit** site, not the build site. |
| 3 | **Bootstrap mode is a property of the proposal**, not sequencer state: a proposal is in bootstrap mode iff it is a bootstrap transaction or has one of the same slot in its past cone (§5.1). |
| 4 | A bootstrap-mode proposal carries **no tag-along** and spends its whole attachment budget on **delegation freezing**. |
| 5 | The per-epoch `MaxFrozenDelegations` cap is lifted for bootstrap-mode proposals. |
| 6 | Bootstrap transactions are placed in the **early ticks** of a slot, leaving the rest of the slot for coverage consolidation. |
| 7 | Freezing takes strict priority over tag-along **only in bootstrap mode**; the steady-state order (tag-along first, 2/3 budget) is unchanged. |
| 8 | **Adaptive / gap-relieved healthiness threshold: rejected.** See §4. Healthiness relief stays a manual, coordinated, bounded operator decision. |
| 9 | The boolean `suppress_health_enforcement` is replaced by a ranged, valued `health_relief` window applied at all four health sites, LRB selection included. Implemented; see §3 and §4. |

## 1. What a bootstrap transaction is

A sequencer transaction whose baseline is set **explicitly** rather than derived
from endorsements — `TxExplicitBaseline` in the transaction tuple, readable with
`tx.ExplicitBaseline()` (`ledger/transaction/tx.go:134`). It is built by
`attacher.NewIncrementalAttacherWithExplicitBaseline` against the LRB stem
(`sequencer/task/proposer_boot.go:43`).

Role in the network's life:

1. The network cold-restarts (or a node comes back after everyone stopped).
   No branches arrive from gossip, so there is nothing to endorse and no
   baseline can be derived.
2. Each sequencer issues a bootstrap transaction anchored on the LRB.
3. Sequencers see each other's bootstrap transactions and start
   extending/endorsing them, seeking the biggest coverage.
4. Once the coverage delta exceeds the healthiness limit, sequencers issue
   branches and the network takes off.

The healthiness limit at step 4 is a **convention honest sequencers accept** in
order not to fork; it is not a ledger rule (§3). Without it, sequencers would
branch from a minority of the coverage and produce incompatible lineages.

The hard case is **frozen coverage decay**: after a long outage most delegations
have unfrozen, so their amounts no longer contribute to any coverage delta, and
the honest coverage available at restart may sit below the healthiness limit —
the network cannot bootstrap. §5 is the mitigation; §4 explains why the obvious
alternative (relax the limit adaptively) is not one.

## 2. State before the change (verified 2026-07-31)

Kept as the record of what was wrong; the file and symbol names below are the
pre-change ones.

- `tryBootProposal` (`sequencer/task/proposer_boot.go:18`) fires when the own
  latest milestone is more than one slot stale and the LRB is in a past slot.
- It is **not privileged**: it competes on coverage against
  `tryBaseExtendProposal` and `tryFactoryProposal` in `task.Run`
  (`sequencer/task/task.go:150-170`). The comment there records why — a
  no-branch sequencer is permanently in the boot condition, so a privileged boot
  would mask the factory's extend+endorse re-anchor.
- It logs `Warnf("BootProposer-…: FIRED …")` at **build** time
  (`proposer_boot.go:64`). Because the proposal can lose the coverage
  comparison, that line is evidence of an *intention*, not of a submitted
  transaction. `decideSubmitMilestone` (`sequencer/sequencer.go:809`) logs it as
  an ordinary `SUBMIT SEQ TX` with no marker.
- It **never calls `insertInputs()`**: a bootstrap transaction carried neither
  tag-along nor delegation freezes. This was the main functional gap.
- Naming collisions to resolve alongside the rename:
  - `base.BoostrapSequencerID` — the *genesis sequencer chain*, unrelated.
  - `Sequencer.bootstrapOwnMilestoneOutput()` (`sequencer/sequencer.go:884`) —
    "find own chain output in the LRB", a third sense. Rename it (e.g.
    `ownMilestoneOutputFromLRB`) so "bootstrap" means exactly one thing.

## 3. Where healthiness is enforced

**Not on the ledger.** `healthyCoverageDelta(supply, covDelta)` is defined as an
EasyFL symbol (`ledger/def/def_constants0.json:209`) but no constraint calls it;
its only caller is Go, which compiles it on demand
(`ledger/inflation_fun.go:97-101`). `ledger/def/lock_stem.easyfl:109-116` states
the decision explicitly: a health gate on the immutable branch can deadlock a
restart from an old snapshot once frozen coverage expires. What the ledger does
enforce is the coverage *arithmetic* — the total-coverage halving recurrence,
the supply recurrence, and the coverage-contribution bounds.

Healthiness is therefore node-local policy, at four sites. All four now judge a
branch by the fraction which applies **in that branch's own slot**
(`global.FractionHealthyBranchAt` / `IsHealthyBranchAt`), so a relief window
moves them together:

| Site | Code |
|------|------|
| Issue gate, build | `sequencer/task/proposer.go` |
| Issue gate, submit | `sequencer/sequencer.go` |
| Accept gate (attacher, real-time attachment only) | `core/attacher/wrapup.go` |
| LRB selection | `ledger/multistate/roots.go`, `core/core_modules/branches/branches.go` |

The default fraction is 7/12 (`ledger/def_constants0.go:165`), read through
`global.FractionHealthyBranch()`.

**The finding this fixed.** LRB selection used to read the ledger constant with
no suppression hook, while the other three sites honoured the boolean
`suppress_health_enforcement`. A node with the flag issued and accepted
unhealthy branches but its LRB never advanced onto them — and the LRB is the
bootstrap baseline, the sequencer's start tips, the synced criterion and the
memDAG pruning horizon. The flag was half-wired.

The fix is not a hook into LRB selection but a change of what the parameter is:
a single fraction cannot describe a search that spans slots on both sides of a
window, so `fraction` is no longer passed into the LRB searches at all
(`FindLatestReliableBranch`, `FindLatestHealthySlot`,
`FirstHealthySlotIsNotBefore`, `FindBranchesFromLatestHealthySlot`,
`FindLatestReliableBranchAndNSlotsBack`, `GetMainChain`, `BranchData.IsHealthy`
all lost the parameter). Each branch is judged per its own slot instead.

Known gap: `proxi db` commands read a local DB without the node's config, so
they still evaluate LRB at the ledger fraction. During a relief window
`proxi db lrb` can therefore disagree with the node it inspects.

## 4. Adaptive healthiness relief — rejected

The idea considered: replace the manual flag with a threshold that is a
deterministic function of on-chain data (e.g. relaxed by the branch gap, in the
spirit of the mining pace-relief retarget), so that no operator coordination is
needed and no node can disagree. It does not survive analysis. Two distinct
risks have to be separated.

**Risk A — disagreement.** Nodes evaluate the same branch differently. This is
today's risk with the boolean flag: the node with the flag attaches an unhealthy
branch and extends it, the node without rejects it, lineages diverge. Risk A
*is* solvable by determinism — the gap to the predecessor branch is committed
data, so every node computes the same threshold. This part of the idea is sound.

**Risk B — a minority must not be able to advance consensus alone.** This is
what the threshold is *for*. Above 1/2 of supply, a partition holding a minority
of the coverage cannot construct a healthy branch; 7/12 is 1/2 plus margin.
Relief below 1/2 gives that property away, and determinism does not help:
every node agrees perfectly that both minority branches are healthy, and there
are now two locally-reliable lineages. Agreement on a bad rule is still a fork.

Risk B is not a corner case for this rule — it is exactly its target scenario.
After a long outage the participants that come back first *are* a minority of
supply. Gap-keyed relief would let them advance consensus alone; when the rest
returns it does the same from its side. **The rule manufactures the fork it is
meant to avoid.**

**Gameability.** Any relief keyed on *observed* non-participation (branch gap,
unfrozen fraction, anything else) is gameable by withholding, because "dead" and
"withholding" are indistinguishable on-chain. An adversary able to suppress
branch production is handed a lower quorum precisely when the network is
weakest. Structurally the same defect as the gameable mining retarget
([`mining-bias.md`](../../mining-bias.md)).

**What survives.** A deterministic relief with a hard floor strictly above 1/2 —
7/12 down to at most ~6.1/12 — is unconditionally safe, and too narrow to
rescue a deep-outage restart where the missing coverage is tens of percent of
supply. Safe and nearly useless; not worth a hardfork.

**Conclusion.** Do not lower the quorum; **restore the electorate** (§5). For
the residual case where coverage provably cannot reach the threshold (the
missing supply is delegated to sequencers that are gone), coordination is
unavoidable and should not be disguised as a protocol rule. The improvement
there is to the *shape of the lever*, not to its nature:

- replace the boolean with an explicitly ranged, valued parameter —
  `(from_slot, to_slot, numerator, denominator)`. Operators agree on one triple;
  nodes setting the same values compute the same predicate, so Risk A is
  eliminated by checkable convention, and the relief cannot silently outlive the
  restart;
- apply that fraction at all four sites of §3, LRB selection included;
- log it per slot while active, not once at startup.

**Implemented 2026-07-31.** Config:

```yaml
health_relief:
  from_slot: 8740
  to_slot: 8800
  numerator: 4
  denominator: 12
```

- installed once at node startup (`node.readInHealthRelief` →
  `global.SetHealthRelief`); absent config means the ledger fraction everywhere;
- the boolean `suppress_health_enforcement` is gone, and a config still carrying
  it makes the node **refuse to start**. A node which silently reverted to full
  enforcement, or silently kept a key nobody reads, is precisely the
  disagreement the window exists to prevent;
- evidence is per branch, not per startup: a branch which passes only because of
  the window is logged `SUBMIT BRANCH … UNDER HEALTH RELIEF` with both
  fractions;
- `suppress_coverage_contribution_lower_bound` is a different flag and is
  untouched.

The floor argument of §4 is **not** enforced in code: nothing stops an operator
configuring a relief below 1/2, which is where a minority can advance consensus
alone. It is a coordinated, deliberate act by design; the constraint is stated
here rather than in a validation rule.

## 5. Design: bootstrap mode

The coverage is not gone after an outage — it is unfrozen. Frozen delegation
amounts contribute to the branch's coverage delta through `frozenCoverage` on
the sequencer output, and a freeze performed in slot S is in the past cone of
the branch closing slot S, so it counts in **that** branch's delta. One slot of
aggressive freezing can lift coverage back over the threshold with no rule
change at all — provided the freezes fit in the budget. Making them fit is the
whole design.

### 5.1 Bootstrap mode is a property of the proposal

Bootstrap mode is **per proposal**, read off the proposal's own structure:

> A proposal is in bootstrap mode iff it is itself a bootstrap transaction, or a
> bootstrap transaction of the **same slot** is in its past cone.

No sequencer state, no entry/exit conditions, **no LRB reasoning** and no health
comparison. The rules of §5.2–§5.4 apply to exactly the proposals that carry the
property.

Consequences of defining it this way:

- It propagates to the descendants by construction. The sequencer's own
  milestones later in the slot extend the bootstrap transaction, and a peer's
  milestone that endorsed one carries it too — so "the bootstrap transaction and
  its same-slot descendants" needs no bookkeeping to express.
- It is self-terminating. Once the network takes off, no bootstrap transactions
  are issued, so no proposal carries the property. Nothing has to be switched
  off, and nothing can be left on by mistake.
- It is objective: two sequencers looking at the same proposal classify it
  identically, because it is a fact about the DAG rather than about a node's
  view of network health.
- Slot-scoped for the same reason the budget argument is slot-scoped (§5.2): the
  next slot either has a branch or a fresh bootstrap transaction.

**Evaluation.** `(*WrappedTx).IsBootstrapMode()` in `core/vertex` — presence of
an explicit baseline in the transaction itself, a bool; the baseline value is
never needed outside the builder, so it is not exposed. Both `Vertex` and
`DetachedVertex` embed `*transaction.Transaction`, so it is a plain `RUnwrap` +
`tx.ExplicitBaseline()` (`ledger/transaction/tx.go:134`) in both forms —
immutable data, recomputed at the read site, nothing cached.

A `WrappedTx` in general *can* be virtual — the sequencer's own-milestone
watcher reports the tippool tip, which at startup is the branch it starts from,
attached by ID — so the vertex-level predicate answers false there rather than
asserting: an unsolidified transaction is one this node cannot classify. The
invariant that does hold is narrower and belongs to the past-cone scan: Good
sequencer transactions of the current slot are never virtual, so
`ContainsBootstrapTransaction` asserts it per same-slot vertex and the scan is
exact.

The proposal-level predicate is then: **any** vertex in the proposal's past cone
with `Slot() == targetTs.Slot` and `IsBootstrapMode()` — not necessarily the
sequencer's own milestone, which is the point: a proposal that endorsed a peer's
bootstrap transaction is in bootstrap mode just the same.

Read it off the past cone the incremental attacher has already built
(`PastConeBase.VertexSet()`, `core/vertex/past_cone.go:190`) at proposal time,
rather than accumulating a flag during the traversal. Same result, and it keeps
the fact out of core's mutable state: a traversal-time flag would have to
survive `Clone`, be OR-ed in `MergePastCone`, and unwind correctly on
`RollbackDelta` — three places to keep coherent, each of which fails silently as
a misclassification. The vertex set is already in memory, so the read is a
filtered set iteration with no DAG walk. Expose it as one narrow accessor on
`IncrementalAttacher` so `PastCone` itself stays unexported to the sequencer.

### 5.2 Freezing on the whole budget

A bootstrap transaction has an explicit baseline and no endorsements, so its
past cone is nearly empty and almost the entire `AttachmentCostBudget` is
available. That is what makes it the cheapest possible place for mass freezing —
and the property does not hold for its descendants, which spend budget on the
endorsement past cones. So the policy follows from the cost model rather than
from preference: **freeze hard in the bootstrap transaction, keep freezing in
its descendants with whatever is left.**

Concretely, the bootstrap path calls `insertDelegations()` only (no
`insertTagAlongInputs`), with `SetEffectiveCostBudget(AttachmentCostBudget)`
(`sequencer/task/proposal.go:195-207`).

### 5.3 Freeze cap

`MaxFrozenDelegations` (default 300 per epoch, `sequencer/config.go:99`) makes
`latestArgminUnderCap` (`proposal.go:382`) **refuse** freezes once every
reachable epoch is at the cap. After a long outage everything unfreezes at once,
so this cap is precisely what would leave coverage on the table. It exists to
bound per-milestone work, not for safety — lift it for bootstrap-mode proposals.

The epoch-spreading itself stays: frozen coverage counts *now* regardless of
which future epoch is chosen, so balancing costs nothing during bootstrap and
still avoids re-creating a synchronized unfreeze wave.

### 5.4 Tag-along priority

Strict "freeze before tag-along" is applied **only in bootstrap mode**. In the
healthy steady state the current order stays (tag-along first, up to
`tagAlongBudgetFraction` = 2/3 of the budget; delegations take the remainder,
`proposal.go:21-23,271-276`): freezes are long-lived and coverage is already
above threshold, while tag-along is the paid, latency-visible service. A global
reversal would let any mass re-freeze wave — which an outage manufactures —
starve tag-along for many slots after the network has already recovered.

If a single unconditional rule is preferred later, it must be bounded: freeze
first, but cap freezes per milestone so tag-along always keeps a slice.

### 5.5 Early-tick placement

Today the target is `max(nowTs, paceMin)` at pulse cadence
(`sequencer/strategy_async.go:309-326`), so a bootstrap transaction can land at
any tick, including too late for anyone to consolidate it. The rule is a guard
in the bootstrap proposer itself: refuse to build when `targetTs.Tick` is past
an early threshold. Later in the slot the proposer simply stays silent and the
sequencer takes the next slot's early ticks instead. Pace is not a constraint
here — a bootstrap transaction follows a long gap by definition.

No per-slot flag is needed for this: "one bootstrap transaction per slot" is
already implied by the proposer's staleness condition, which stops firing as
soon as the own milestone is in the current slot.

Consequence observed while testing: a sequencer whose first opportunity in the
slot is the slot edge does not issue a bootstrap transaction at all — the branch
proposer extends the stale *branch* directly (a branch predecessor is exempt
from the same-slot rule) and the chain is current again without ever passing
through a bootstrap transaction. That is a legitimate way out of the gap, it
just isn't this one; the bootstrap path is taken when the sequencer's pulse
first fires inside the early ticks. This is what the test has to align to.

### 5.6 Naming and evidence

- `proposer_boot.go` → `proposer_bootstrap.go`; `tryBootProposal` →
  `tryBootstrapProposal`; `finalProposal.source` `"boot"` → `"bootstrap"`;
  trace tag likewise. Rename `bootstrapOwnMilestoneOutput` out of the way (§2).
- Log the evidence at the **submit** site, where it is a fact:
  `decideSubmitMilestone` (`sequencer/sequencer.go:809`) detects it with
  `tx.ExplicitBaseline()` — no plumbing of the proposal source needed — and
  prefixes the line with `BOOTSTRAP`.
- Level: there is no verbosity-bypassing logger in `global` today, so `Warnf` is
  the practical maximum (`Errorf` would be semantically wrong). If a genuinely
  level-independent record is wanted, add one explicit always-on sink rather
  than abusing the error level.

## 6. Touch points

| File | Change |
|------|--------|
| `sequencer/task/proposer_boot.go` | rename; freeze-only input insertion; early-tick guard |
| `sequencer/task/proposal.go` | bootstrap-mode predicate on the proposal; freeze-only path, full budget, cap lift |
| `core/vertex/vid.go` | `IsBootstrapMode()` predicate on `*WrappedTx` |
| `core/vertex/past_cone.go` | `ContainsBootstrapTransaction(slot)` |
| `core/attacher/attacher_incremental.go` | `IncrementalAttacher.IsBootstrapMode()` |
| `sequencer/sequencer.go` | `BOOTSTRAP` submit log; rename `bootstrapOwnMilestoneOutput` → `ownMilestoneOutputFromLRB` |
| `tests/bootstrap_transaction_test.go` | new: bootstrap transaction after a gap, early tick, branches follow |
| `global/global.go` | health relief window: `SetHealthRelief`, `FractionHealthyBranchAt`, `IsHealthyBranchAt` |
| `ledger/multistate/roots.go` | LRB searches judge each branch by its own slot; `fraction` parameter dropped |
| `node/node.go` | `readInHealthRelief`; refuses to start on the obsolete boolean key |
| `global/health_relief_test.go` | new: window boundaries, validation, which fraction applies where |

Nothing in `sequencer/strategy_async.go` or `slot_data.go`: with the predicate
of §5.1 there is no mode to track across pulses.

`core/vertex` and `core/attacher` are touched (read-only additions: a vertex
predicate and a past-cone read), so the core rule applies — run the relevant
`tests/` sequencer tests under `-race`, one at a time.

## 7. Open questions

1. **Quantitative**: what fraction of supply is delegated to sequencers that are
   actually alive after a restart? If freezing that fraction closes the gap to
   7/12, §4's residual case never arises in practice and no lever is needed.
2. The early-tick threshold needs a number, ideally from a local multi-node
   cold-restart run.
3. Whether to implement the ranged relief parameter of §4 now or leave the
   boolean flag until the residual case is actually observed.
