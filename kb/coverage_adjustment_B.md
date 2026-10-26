# Descent-adjusted coverage delta (coverage adjustment, option B)

> SPEC, not built (written 2026-10-24, revised the same day after two external reviews). A
> node-level decision metric for the reference sequencer; no ledger change, no library upgrade. The
> coverage delta stays as it is (stored, validated, used for health and the latest reliable branch);
> a second, adjusted delta decides what a sequencer builds on. Removes the advantage of building a
> branch on an early milestone of the previous slot. Background: `kb/research/branch-bias.md` (fourth
> input), `kb/research/early_cut_derivation.md`. Not yet demonstrated as a complete fix; section 10
> lists what must be shown. Read before changing the coverage computation, the milestone preference,
> or the factory's and proposers' comparisons.

## 1. The problem

A branch extends one of its issuer's own milestones of the previous slot and contains that milestone's
past cone. The coverage delta of a transaction counts the baseline-state outputs consumed anywhere in
its past cone, whatever slot the spending transaction belongs to (`PastCone.CoverageDeltaRaw`). That
is the intended accounting model, and it has a consequence: a branch built on a milestone from early in
the previous slot (an **early branch**) still holds older output versions, and a transaction that
brings in the later spends of those outputs counts them at full weight. On a branch that already holds
them (a **late branch**) they count at half, because a milestone's coverage halves its baseline's:

    LC(T) = LC(B)/2 + Δ(T)     for a non-branch T on baseline B (same slot)

Two related mechanisms, not to be confused:

- an issuer's early non-branch milestone brings its own capital and frozen delegation weight in early: a
  capital and timing advantage (the earlier VULCAN pattern; library upgrade 2 removes most of it);
- an early branch retains older unspent outputs, so a follower or a collector introduces their later
  spends at full weight. Fixing the first does not fix the second; this spec is about the second.

On slot 37786 (from the DAG, checked against the log to the mote) the founder's sequencer oseq1 scored
124.70T building on MORA's early branch against about 75T on its own late branch. The branch with the
top coverage won 89% of 360 slots before the early-cut strategy started on 2026-10-22, 7-11% after.
One sequencer can exploit it alone (margin A/2 − w_F ≈ +18.7T, with A one slot's moved capital and w_F
a follower's own chain output); a group with a collector does it every slot. Library upgrade 2 (no
sequencer transaction before tick 9) moves the first moves to tick 9 and does not remove it.

Option B stops counting value that reaches a transaction's past cone through history its baseline does
not contain, except where that value is handed into the baseline's own history.

## 2. Principles

- **Coverage decides.** A sequencer maximises the score of its next transaction among admissible ones;
  nothing selects a branch, an issuer or a group by preference.
- **Two metrics.** The coverage delta keeps every role it has on the ledger. The adjusted delta is used
  only for the sequencers' choices. Validity is unchanged, so the adjustment needs no ledger change;
  it is still consensus-relevant, since consensus follows those choices (section 6).
- **Deterministic.** The adjusted delta of a transaction depends only on transactions on the DAG:
  their IDs, inputs, endorsements, explicit baselines. Not on a node's retained history beyond a
  stated minimum, its caches, state or clock, nor on who evaluates it or when.
- **No state reads.** Everything is computed from transactions.
- **Conservation.** Value crossing into the baseline's own history is counted once, whatever path it
  takes (a direct sequencer handover, a non-sequencer payment, a chain of both).

## 3. Definitions

### 3.1 The baseline of a sequencer transaction, from transactions

For a sequencer transaction v, its **direction** d(v) (`Transaction.BaselineDirection`):

1. the explicit baseline, if v has one (`ExplicitBaseline`, a branch ID);
2. otherwise, if v's chain predecessor is a sequencer transaction of the same slot, that predecessor;
3. otherwise, if v is a branch, its chain predecessor (the milestone it extends);
4. otherwise its first endorsement (the ledger guarantees one, `_crossSlotPredecessorCase`).

The **baseline** β(v) of a non-branch v is the first branch reached by following directions from v.
For a branch, β is itself as seen by others; the baseline of its own delta is β of its direction (rule
3): the previous branch on its lineage.

### 3.2 The branch lineage

A branch consumes the previous branch's stem output; the producer of that input is its **previous
branch** prev(B). The lineage of B is B, prev(B), prev(prev(B)), ... Both are transaction data (the
stem link `FindLatestReliableBranch` already walks).

### 3.3 Descent, three-valued

For a sequencer transaction v and a branch B, `descends(v, B)` reads "v descends from B": v was built
on B or on a later branch of B's lineage, so B is part of v's history. Its value is one of:

- **yes**: β(v) = B, or B is on the lineage of β(v);
- **no**: β(v) ≠ B and slot(β(v)) ≤ slot(B) (decided by slots, no walk), or the walk from β(v)
  reaches a branch of slot ≤ slot(B) other than B;
- **unknown**: a transaction needed for the walk (a direction pointer, a branch of the lineage) is not
  yet on this node.

**Descent is a property of the DAG.** It depends on two pieces of it: v's direction pointers, from v
to β(v), which all lie in v's own past cone; and the branch chain from β(v) back to B, through the
stem inputs, of which only the branches between slot(B) and slot(β(v)) are involved. Given those
transactions the answer is **yes** or **no**, the same on every node.

**Unknown only means not yet solid**, never pruned. A pointer or a branch on that path has not arrived
yet; that is transient and resolves as solidification pulls it. Pruning and retention do not produce
it: anything the walk would need below B's slot is never needed, since as soon as a pointer or a branch
is older than B the answer is **no** by the slot comparison, without fetching anything. Snapshot floors
and memDAG pruning therefore leave descent decidable.

**Inside a completed attachment, descent is always decidable.** For a T being attached, every v in its
delta is solid before T is complete (the attacher requires it), and a solid v has its whole past cone
down to its baseline. Unknown can appear only while T itself is incomplete, and needs no handling
beyond what solidification already does. It is never cached and never turned into **no**: an
incomplete candidate is left out of the comparison until it completes.

### 3.4 The adjusted coverage delta: value crossing into the baseline's history

For a sequencer transaction T, write B = β(T) for its baseline (section 3.1; for a branch T, the
baseline of its own delta, the previous branch). Since β is itself a function of the transaction, the
**inside** set I = I(T) is a function of T alone, through T's past cone and β(T). It is a subset of T
itself and T's past cone above its baseline:

    I(T) ⊆ {T} ∪ (past cone of T  minus  past cone of β(T))

Apart from T itself, whether a transaction belongs to I depends only on β(T) and that transaction's
own ancestry, not on which T is evaluated: two transactions with the same baseline classify every
transaction they share the same way, which is what lets membership be cached per (transaction, B).

I(T) consists of:

- T itself;
- every sequencer transaction that descends from B;
- every transaction, sequencer or non-sequencer, that consumes at least one output produced by a member
  of I.

The third rule is transitive: a transaction is inside as soon as any of its inputs comes from an inside
transaction, and membership is computed in the DAG's order, producers before consumers. Everything
else in the past cone (not in B's past cone) is **outside**: sequencer history not on B's lineage
that consumes nothing of I, and non-sequencer transactions fed only by B's state or by outside
transactions.

The third rule covers sequencer transactions too. A sequencer transaction that does not descend from B
normally cannot consume an output produced after B (its past cone would then contain B's lineage), but
a bootstrap transaction with a deliberately old explicit baseline can. Classified by descent alone, it
would be outside, and value would be counted twice: once on entering I, again when the bootstrap
transaction hands it back to an inside consumer. Classified by the third rule, it is inside, and the
round trip is not counted again.

    Δb(T) = Σ w(O)   over the outputs O consumed by a member of I and produced outside I
                     (that is, present in B's state, or produced by an outside transaction)

plus the bonus adjustment of today's delta (the inflation of B's sequencer output while unconsumed;
section 3.6). w(O) is `ledger.Coverage`, unchanged. Membership of a transaction depends only on its own
ancestry, so it is fixed once the transaction is known.

In words: count each output where value enters the baseline's own history; never count spends that
stay outside it.

**Conservation, on the reviewers' examples.** Baseline P holds a sequencer output Q₀ (1,000) and a
wallet output X (100). An early milestone M (tick 9) does not contain what follows. A non-sequencer
payment U (tick 12) spends X into a 100 tag-along O for Q; the sequencer's Q₁ (tick 15) consumes Q₀ and
O into Qout (1,100); branch B of the next slot extends M, so its state still holds Q₀ and X; Q₂ (tick
9 of the next slot) consumes Qout on B.

| Path | Rules (a)/(b) of the first draft | Section 3.4 |
|---|---|---|
| U, Q₁ outside; Q₂ inside consumes Qout | X 100 (non-sequencer consumer) + Qout 1,100 = 1,200 (overcount) | Qout 1,100 (the only output entering I) |
| Old sequencer withdrawal W → non-sequencer payment V → current sequencer consumes V's output Z | Z not counted by (b) (producer non-sequencer), Y not counted (consumer non-sequencer): credit lost | Z counted (produced outside, consumed inside): the same as a direct handover |
| Batch of non-sequencer transactions posted together, U1 → U2 → U3, each consuming the previous one's output, U1 fed by B's state; a sequencer consumes U3's output | X counted at U1 (non-sequencer consumer), more along the chain | only U3's output, where the batch's value enters I; the intermediate spends stay outside |
| The same batch, but U1 consumes an output of an inside sequencer transaction (a withdrawal of the current slot) | | U1, U2, U3 inside by the third rule; their inputs from B's state counted where consumed, the outputs passed along the batch not again |
| Inside output → bootstrap transaction with an old explicit baseline → back to an inside consumer | | the bootstrap transaction is inside by the third rule; the value is counted once, on first entering I |

The first draft's rules (a) and (b) are withdrawn.

### 3.5 The score

The score mirrors `attacher.FinalLedgerCoverage`, with the adjusted delta in the latest branch and in
the transaction:

    aLC(B)   = LC(P) >> (slot(B) − slot(P)) + Δb(B)     B a branch, P its previous branch
    score(T) = (aLC(B) >> (slot(T) − slot(B))) / 2 + Δb(T)     T non-branch on baseline B

LC(P) is the stored, validated coverage of the previous branch (on its stem). Only the latest branch's
delta is adjusted:

- the decisive difference is in the latest branch (on slot 37786 all branches of the slot extend the
  same P, so LC(P) cancels and Δb(E) − Δb(L) decides);
- it needs only B's own past cone, which a node attaches when it attaches B, so a node restored from a
  snapshot can score once it has attached one branch: no 16-branch history to reconstruct, no
  truncation error (a depth-16 tail of a 125T coverage is about 1,900 PROX, far above a 5 PROX bonus);
- the shift by slot distance is the ledger's own, so skipped slots are handled as today.

The inherited part is today's coverage; an adversary's earlier inflation of LC(P) is inherited too.
Deeper adjustment is possible later; it then needs a stated depth, exact rounding, and a reconstruction
rule after snapshots.

### 3.6 The bonus adjustment

Today the inflation of B's sequencer output is added while that output is unconsumed
(`coverageDeltaAdjustment`); once consumed, it is inside the consumed output's amount. Δb keeps this
unchanged: a consumed baseline sequencer output is in B's state, so it is counted at full weight by
3.4, inflation included; while unconsumed, the inflation is added as today. The score therefore never
decreases when the baseline's sequencer output is consumed, and the bonus keeps breaking ties between
branches of equal score.

## 4. Properties

- **Deterministic**: β, prev, descent and membership are read from transactions; two nodes holding the
  same transactions within the availability contract compute the same score.
- **Monotonic within a baseline**: adding a transaction or an endorsement to a past cone adds consumers
  and outputs; membership of existing transactions does not change; terms are only added.
- **Conserving**: value entering the baseline's history is counted once, whatever path it takes
  (section 3.4).
- **No amplification**: every counted output is counted once at its own weight; the outputs that enter
  I carry at most the capital that crossed.
- **Coverage only**: validity, conflict checks and the past-cone traversal are unchanged.
- **Timing-free**: the defence does not depend on when sequencers issue milestones; no zone discipline.

## 5. Against the data: slot 37786

Branches: P = Luhmann's branch of 37785 (both branches below extend it); E = MORA's branch of 37786,
extending MORA's tick-3 milestone of 37785; L = oseq1's branch of 37786, extending oseq1's tick-13
milestone of 37785. Founders' chain-output versions held, from the DAG:

| Branch | boot | hloc0 | oloc1 | oloc2 | oseq1 |
|---|---|---|---|---|---|
| P | s37783-53 | s37783-53 | s37783-53 | s37783-53 | s37783-49 |
| E | s37784-45 | s37784-45 | s37784-45 | s37784-45 | s37784-45 |
| L | s37785-9 | s37785-9 | s37784-45 | s37784-45 | L's own output |

- **Δb(E)** relative to P: E's inside transactions (the group's own milestones of 37785, baseline P)
  consume only the group's outputs handed on by its outside history (the collector's tick-127
  milestone of 37784 and the group's milestones of 37784, on older baselines). The founders' outputs
  are spent only by outside transactions (their milestones of 37784): not counted. Δb(E) ≈ the group's
  own chain outputs, well under 1T.
- **Δb(L)** relative to P: the founders' milestones of 37785 that L contains (oseq1, boot, hloc0) are
  inside (baseline P) and consume their own chain outputs of 37784, produced outside: 3 × 12.3 ≈
  36.9T.
- **oseq1's first move on E**: inside (it is T), it consumes its tick-13 output of 37785, produced by a
  transaction with baseline P, outside E's history: w ≈ 12.3T; nothing else of 37785 enters. **On its
  own L**: L's sequencer output, in L's state: w ≈ 12.3T.
- aLC(L) − aLC(E) = Δb(L) − Δb(E) ≈ 36T (the same LC(P) under both); **score difference ≈ 18T in favour
  of L**.

Derived from the definitions and the DAG relations above, with about 12.3T per founder (oseq1's
measured). Historical rescoring checks this arithmetic; it cannot establish the equilibrium, because a
different metric changes what honest and adversarial sequencers produce (section 10).

### Single sequencer

An early branch E built on R's own tick-9 milestone holds none of the slot's activity. The followers'
milestones of that slot have the previous branch as baseline: outside E's history. On E a follower
counts only its own chain output entering I, w_F, plus aLC(E)/2 with Δb(E) ≈ R's own. On a late branch
L, aLC(L) is larger by about A. L wins by about A/2.

### Collector

The collector's harvest milestone has an older baseline: outside. Its spends do not count; only what it
hands into the early branch's own history (its own chain output) does. The early branch is not
pre-loaded.

### Normal operation

- A follower whose latest milestone is in the winning branch: that milestone is behind the baseline.
  Unchanged.
- A follower whose last milestone came after the winning branch's cut: that milestone is outside; its
  chain output enters I when the follower's next milestone consumes it (counted w_F). In the pre-branch
  zone such a milestone has a single input, its chain predecessor, so no other spend is lost.
- User payments consumed by sequencers in the slot (tag-alongs): their outputs enter I when a
  sequencer consumes them; counted at their amount, as today in effect. A user payment's change output
  not consumed by any inside transaction no longer counts: the adjusted delta measures value entering
  the baseline's history, not total spending.
- Lineage switches and re-anchors: the own output in the branch state, or the own milestone's output,
  enters I: equivalent.
- Bootstrap transactions with an old explicit baseline: dependencies on the same lineage descend
  (inside); dependencies on another lineage do not.

## 6. Interplay with the unchanged metric and consensus

- **Admissibility before maximisation.** The ledger requires the declared coverage delta of a
  milestone to strictly exceed its same-slot predecessor's (`_enforceCoverageAdvance` in
  `sequencer.easyfl`, on the effective predecessor: 0 for a cross-slot one), and `task.Run` refuses a
  milestone whose coverage does not beat the own latest milestone of the slot. Today `task.Run` picks
  the best proposal first and gates it after, so if score and coverage diverge it can drop the only
  valid alternative:

  | Candidate | Coverage | Score |
  |---|---|---|
  | own latest milestone of the slot | 100 | |
  | B | 99 | 50 |
  | C | 101 | 11 |

  Choosing B by score and gating it misses C. Each candidate's eligibility must be decided first, and
  the best score taken among the eligible, completed proposals.
- **The two guards differ.** `task.Run`'s gate compares with the own latest milestone of the slot; the
  ledger's rule compares with the transaction's effective predecessor, which is 0 for a cross-slot
  re-anchor. A lineage switch can be valid on the ledger and still refused by `task.Run`; the refactor
  must apply the ledger's notion.
- **Health.** Unchanged: `IsHealthyBranchAt` on the stored coverage delta. A sequencer still refuses
  to issue an unhealthy branch by today's rule.
- **Latest reliable branch.** `FindLatestReliableBranch` takes the healthy branches of the latest
  healthy slot and walks back from the heaviest until a branch known in all of them: the common
  ancestor, independent of which tip is called heaviest. No change needed. Convergence of the reliable
  lineage under the new choices must still be demonstrated (section 10); local candidate order and
  commit state can influence lineage choices (`kb/research/branch_fork_convergence.md`, an inference
  there, not a proof).

## 7. Computation, cost and availability

- **β(v)** is followed through direction pointers; each step is a transaction in the past cone or the
  memDAG. Immutable per transaction: computed once, cached on the vertex.
- **Descent** of v from B: see 3.3; walks memoised per pair (β(v), B), only definite answers cached.
- **Membership** in I: per transaction, from its inputs' producers and its own descent; computed in the
  traversal order (producers first), cached per (transaction, B).
- **Bound**: a past cone touches few distinct baselines, the slots it spans times the branches per
  slot, both bounded by the attachment budget. The walks cost O(distinct baselines × slot distance);
  the per-output work is a lookup.
- **Where it runs**: inside the delta loop that already exists, a second accumulator; the loop is
  producer-centric, and `consumersByOutputIndex` gives each consumed output's consumer (nil for T).

### One accounting pass

The outputs added up in the coverage delta are the outputs deleted by the mutations. The two are
computed separately today (`core/vertex/past_cone.go`):

| | `CoverageDeltaRaw` | `Mutations` |
|---|---|---|
| Loop | every vertex of the past cone, each of its outputs consumed in the cone | the same |
| Outputs kept | those found in the baseline state, by a state read for every consumed output of every vertex (`GetOutputFromStateReader`), misses included | outputs of vertices in the state (`IsInTheState`, a flag set by the traversal) with a delta consumer or the virtual consumer (nil): the DEL set |
| Value | `ledger.Coverage(o)` | the amount, and chain bookkeeping |
| State reads | one per consumed output | none: the output comes from the vertex (`MustOutputAt`) |

An output in the baseline state and consumed in the cone is exactly an output of a vertex in the state
consumed by a delta transaction or by the transaction being built (an output already spent inside the
baseline cannot be consumed again without a conflict). So:

    Δ(T) = Σ ledger.Coverage(o) over the DEL set, plus the bonus adjustment

Consequences:

1. **`CoverageDeltaRaw` without state reads.** It can use the in-state flags and the vertices' own
   outputs, as `Mutations` does. It runs on every coverage evaluation, repeatedly in the factory's
   rounds, so the saving is likely noticeable. The coverage delta is validated: the change must give
   identical values, shown by a test comparing the old and the new computation on recorded and
   constructed cones before switching. Useful independently of option B.
2. **One function for the DEL set, three users.** `PastCone.CollectDelSet()` walks the vertices in
   the state and returns `map[vertex.WrappedOutput]*vertex.WrappedTx`: every output of theirs consumed
   by a delta transaction or by the transaction being built, mapped to its consumer (nil for the
   transaction being built). The key carries the producer (`VID`) and the index; the output and its
   ID come from the existing accessors (`Output()`, `DecodeID()`, `OutputWithID()`). In a valid past
   cone each such output has one consumer (`Mutations` asserts it today). The users:
   - `Mutations`: a DEL mutation per entry, plus the chain bookkeeping it does today (deleted chain
     IDs, amounts deleted); the ADD side stays in `Mutations`;
   - the coverage delta: Σ `ledger.Coverage(o)` over the entries, plus the bonus adjustment, replacing
     the state reads of `CoverageDeltaRaw`;
   - the adjusted delta (option B): the entries whose consumer is inside (section 3.4).
3. **The internal edges for option B.** Rule 3.4 also counts outputs produced outside I by a
   transaction not in the state and consumed inside I. Those are not in the DEL set; a sibling,
   `CollectInternalEdges()`, returns the outputs of non-rooted vertices consumed in the cone, in the
   same `map[vertex.WrappedOutput]*vertex.WrappedTx` shape, and the adjusted delta keeps those whose
   producer is outside and consumer inside.
   Membership is looked up per transaction, cached.
4. **Incremental sums.** Both sums only grow with the cone, so the incremental attacher can update them
   when it adds a vertex or an endorsement, instead of recomputing the whole cone each time the factory
   asks for a coverage. A separate step, once the pass is unified.

Caveats:

- `Mutations` runs at wrap-up, when every vertex's in-state flag is final. `CoverageDeltaRaw` can be
  called earlier (the incremental attacher); whether the flags are complete there must be confirmed. If
  they are not, the shared pass waits for them, or falls back to the state read for the vertices whose
  flag is not yet set.
- The DEL set and the coverage delta are consensus-relevant: the refactor is core work under
  `kb/dag_semantics.md`, run under `-race`, with the equivalence test before anything relies on it.
- **Availability contract**: scores are evaluated for transactions whose past cone the node holds down
  to the baseline, and whose baseline's previous branch is known (its stem carries LC(P)). A node fresh
  from a snapshot scores once it has attached a branch on top of it; until then its sequencer uses
  today's coverage (or does not issue), and reports so. Today's snapshot carries one branch root, its
  state and the upgrade libraries, nothing more; section 3.5 keeps the requirement within that.
- **Performance is part of the outcome**: lineage queries and several baselines per round can delay the
  first publication at tick 9. Measure the full build, attach and publication latency under load, not
  the metric alone.

## 8. Implementation

### Core

- **Stage 0, before the metric** (`core/vertex/past_cone.go`): `CollectDelSet()`, used by `Mutations`
  for its DEL mutations and by the coverage delta in place of `CoverageDeltaRaw`'s state reads,
  proven identical to today's values by an equivalence test (section 7, "One accounting pass"). No
  change of any value; a foundation for the adjusted delta and a saving on its own.
- **`core/vertex/past_cone.go`**: the adjusted sum from `CollectDelSet()` and
  `CollectInternalEdges()` (section 3.4), with the membership of each producer and consumer.
- **`core/vertex`**: per vertex, the transaction-derived baseline β (immutable, set before Good), the
  inside-membership cache per baseline, and the score (atomic, set at wrap-up, like `coverage`); the
  descent helper of 3.3.
- **`core/attacher`**: `FinalScore(ts)` beside `FinalLedgerCoverage(ts)`, set on the vertex at wrap-up
  (`wrapup.go`, `attacher_milestone.go`); the checks on the declared coverage delta stay on the
  unchanged delta.
- **`core/vertex/cmp.go`**: `IsPreferredMilestoneAgainstTheOther` compares scores; ties as today (the
  bonus between branches, then the ID). Its callers follow: the tippool and through it the backlog's
  candidate order.

### Sequencer decisions

- **Eligibility first** (`task.Run`): decide each proposal's eligibility (the ledger's advance rule on
  its effective predecessor), then take the best score among eligible, completed proposals.
- **Factory** (`sequencer/factory`): one improvement loop per baseline with its own local threshold
  (improve a path as long as it improves on itself), separate from the global publication threshold
  (publish only what beats the best published score); today's loop stops a path as soon as a step does
  not beat the global best, which would kill a temporarily weaker baseline before an endorsement makes
  it the best.
- **Compare completed proposals**: tag-alongs and delegation transitions can change the ranking, so the
  final choice compares completed proposals, not bare skeletons; skeleton ranking stays a heuristic for
  the order of work.
- **Proposers**: `betterProposal` compares scores among eligible proposals.

### Sequencer strategy (the larger part, due anyway)

**Goal**: a quasi-optimal strategy. No single optimal strategy is expected; the reference sequencer
must contribute to an equilibrium in which a rogue strategy gains only a minimal advantage.

- **Post-branch zone (ticks 0-8, first milestone at tick 9)**: the factory's priority is the best
  extend-endorse pair, searched exhaustively within the time available: every extension of the own
  chain the own past cone offers (the chain head; the newest own milestone each candidate branch's past
  cone references; the own output committed in that branch) crossed with every branch that has
  arrived, ranked by score; the best eligible one goes out at tick 9. A first milestone extending the
  own milestone of the previous slot inflates the chain output (only a cross-slot transition does);
  tag-alongs and delegation freezes wait for the middle of the slot.
- **Middle of the slot (ticks 9-102)**: the current behaviour, ranked by score, with the per-baseline
  loops kept alive so that an overtaking lineage can be switched to.
- **Pre-branch zone (ticks 103-127)**: not required by option B. A final milestone endorsing the peers'
  latest, with the branch built on it, makes branches complete; to be decided with the refactor.

### API and metrics

The score beside the coverage in the monitor and the dag explorer; a metric for the score of the
latest reliable branch beside `proxima_lrb_coverage` (after the audit of existing `proxima_*`
metrics).

## 9. Non-sequencer transactions

Covered by the inside set (section 3.4): a non-sequencer transaction is inside when it consumes an
output of the baseline's own history, directly or through other transactions of the cone (batches of
transactions posted together and consuming each other included), outside otherwise. A group routing its own capital through user
transactions gains nothing: the value is counted once, where it enters the baseline's history, whatever
the path. What remains to measure is the effect on normal coverage values of not counting user
spending that never enters a sequencer's history (change outputs, user-to-user chains consumed by no
sequencer).

## 10. Validation

Neither this option nor any other is shown to eliminate the strategy until the following hold.

| Question | Discriminating case |
|---|---|
| Does B conserve value? | Non-sequencer payment → old sequencer → current handover; the same value through a direct handover; through one or two intermediate non-sequencer payments; through a batch of non-sequencer transactions consuming each other; an inside output round-tripping through a bootstrap transaction with an old explicit baseline |
| Is B independent of payment packaging? | Direct handover against one non-sequencer intermediate payment: equal Δb |
| Are scores reproducible? | The same transactions on archival, pruned and snapshot-restored nodes: identical scores for every completed attachment (unknown only before solidification) |
| Does selection stay live? | The best score fails the coverage gate while another completed candidate passes: the other is taken |
| Does B stop adaptive cutters? | Cutting just before the zone; cutting after the followers' first moves; a collector pair |
| Quiet and missed | Quiet slots; sequencers missing milestones; mixed output versions |
| Does the network converge? | Several baselines, delayed arrivals, mixed old and new policies on the network |

- **Unit (`core/vertex`)**: β from direction pointers (explicit baselines, cross-slot endorsements);
  three-valued descent; membership; the adjusted sum on constructed cones, including the conservation
  cases of section 3.4.
- **Attacher, under `-race`**: the score set before Good and immutable after; the declared coverage
  delta still checked on the unchanged rule.
- **Multi-sequencer, in process (`tests/`)**: the cases of the table.
- **Offline evaluator**: a tool over the transaction store that, for recorded slots, lists every branch
  and every first move with its coverage and score. It checks the arithmetic on history (including the
  period since 2026-10-22); it does not establish the equilibrium.
- **Rehearsal**: the local multi-node kit of library upgrade 2 (`.internal/rehearsal_upgrade2/`), with
  the behaviours above and a node restored from a snapshot.

## 11. Rollout and success criteria

A node release, no ledger change: the founder's sequencers first, then everyone. Success is not the
share of winners with the top initial coverage, which stops being the selection metric. Measure:

- progress of the latest reliable branch (slots behind, reorganisations);
- confirmation latency;
- capital left out of the winning lineage per slot;
- CPU and publication latency of the sequencers, under load;
- winners per issuer, and resistance to the adaptive cutters and collectors of section 10;
- health, as today.

## 12. Open questions

- The pre-branch zone strategy under option B: needed for complete branches, not for the defence.
- Deeper adjustment of the inherited branch coverage (beyond the latest branch), with depth, rounding
  and reconstruction defined.
- `kb/dag_semantics.md` describes the ledger coverage; whether the decision score is recorded there,
  and how, needs approval (it changes nothing the ledger validates).
- **A validation gap found in review**: `enforceSeqCoverageDelta` (`core/attacher/wrapup.go`) skips
  the declared-against-computed check when `baseline.Slot() >= milestone.Slot()`. Its comment explains
  re-attachment against a newer baseline after a snapshot; but a non-branch milestone's baseline is the
  branch of its own slot, so the condition holds with equality and the check is skipped for every
  non-branch milestone. Only branches (whose baseline is an earlier slot) are checked. A static finding,
  confirmed in the code, not a demonstrated exploit; to be fixed separately (strict inequality, or a
  snapshot-specific condition) and checked with a native test before relying on declared deltas.
- Related, separate: the VRF message of the branch bonus chains on the previous proof, which is not
  unique, so a branch issuer can grind its next bonus (a ledger fix, deferred); a fixed small branch
  bonus as a reserve measure.
