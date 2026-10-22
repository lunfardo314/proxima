# Mine transit settlement: network upgrade

> SPEC. **Sequencer policy built 2026-10-19 (no hardfork); ledger rule and miner change not built.**
> **Endorsement policy and settlement pulse (no hardfork) built 2026-10-20 for v1.0.6.**
> Drafted 2026-10-19 from the MORA analysis (`.internal/mora_analysis_2026-10-19.md`).
> A library upgrade at a slot (`ledger/upgrade.md`), no reset. Changes the mine covenant (one
> rule), the sequencer's transit settlement and the miner. Read before touching any of them.

## Problem

The canonical winner rule of `kb/mine_conflict_rule.md` (oldest slot, then smallest VRF output,
settled at the end of the slot) is a sequencer policy that two facts defeat on the live network:

1. **Nothing stops early inclusion.** A transit enters the ledger when any of its outputs is
   consumed. A sequencer that does not wait for the settlement window (any build before v1.0.3,
   or a modified one) includes whatever it holds first; the others then endorse that milestone
   and drop their conflicting candidates. Observed: transits addressed to the early sequencer
   win 25% of the time, all others 1-6%.
2. **Each transit is visible to one sequencer.** Its fee (options 1, 2) or its whole payout
   (option 3) is a tag-along addressed to one sequencer, the only one allowed to consume it, so
   each sequencer ranks only the transits addressed to it. No sequencer can apply a global rule.

Consequences on the miners' side: the miners predict the winner by the rule, the sequencers do
not follow it, so miners build on losers until the LRB re-roots them; the miner whose own
sequencer took the winner extends it unopposed (the "late-seen" self-extension), and slots in
which it has no solution stay empty (~8% of slots).

## Change

Two parts: one ledger rule that nobody can bypass, and a sequencer policy that makes the
honest sequencers agree. The tag-along lock is not touched: a tag-along stays consumable by its
target only, so sequencers never contend for one output.

### Ledger (library upgrade)

1. **No transit is consumable before the settlement tick.** `_mineShape` additionally requires
   the transit's own timestamp tick to be at least `S - constTransactionPaceSequencer`, where the
   settlement tick `S = ticksPerSlot64 - 1 - constPreBranchConsolidationTicks -
   constMineSettlementWindowTicks` (new constant, 16, equal to the Go
   `txbuildercore.MineSettlementWindowTicks`; today S = 86, so the tick must be >= 83). A
   sequencer milestone needs only the sequencer pace (3 ticks) to consume an input and a
   non-sequencer transaction needs 12, so no output of a transit can be consumed, by anyone or
   in any way, before tick 86 of its slot. Compliant or not, no sequencer gets there first.
2. **`constMinerVersion` bumped** (4 -> 5) in the same upgrade, so an old miner builds only
   invalid transits and is told to update.

Payout layouts, fees and the tag-along lock are unchanged.

### Sequencer policy: defer to the best transit

Competing transits on one predecessor conflict by construction (they spend the same mine
output); the aim is that only one sequencer includes one of them, and that it is the one the
rule names. Each transit stays consumable by its target only, so:

- At settlement a sequencer ranks **all** validated transits its node holds on a predecessor,
  not only those addressed to it. It consumes its own candidate only when that candidate is the
  best of them. When the best is addressed to another sequencer, it consumes none on that
  predecessor. (It was meant to endorse the milestone that includes the best once it sees it;
  nothing does that yet: the factory endorses by coverage alone. See the endorsement policy below.)
- It defers only to an **active** target: one whose latest milestone in the tippool is at most
  `ActiveSequencerSlots` (5) slots older than the target timestamp, the rating's window. A
  transit addressed to an inactive sequencer is ranked out.
- **A competitor counts only once validated** (mine lock checked, proof of work included): a
  forged transit with a small claimed VRF output must not make honest sequencers stand back.
- Liveness: if the deferred-to target never includes the best, the chain still moves. The next
  transit spends the best's mine output, so the sequencer including that transit includes its
  parent too. Only the parent's payout waits for its own target.

**Built** (`sequencer/task/mine_registry.go`, `mine_transits.go`, `proposal.go`): a
`MineTransitRegistry` per sequencer, fed by the mining-transaction event, which every node posts
for every transit before the attach gate. It keeps each transit's bytes by the mine output it
spends (32 slots) and validates a transit on demand with the ledger's own full-context check on a
fresh parse, the spent output taken from the registry or the tx store; the memDAG is not touched.
The proposer takes its own pending candidates' validity and the rivals from it.

**Why the registry and not the memDAG.** A non-sequencer transaction is attached as a bare vertex:
its inputs are linked, its consumer edges recorded and its constraints validated only inside the
attacher of a milestone that walks it. So a sequencer never sees the competitors addressed to
others, and under v1.0.3 never sees its own transit validated either: measured on slots
9,428-9,680, every compliant sequencer consumed winners only 1-3 slots after their slot, once
another sequencer's milestone had validated them, and MORA decided every slot. The v1.0.3 fix
(`2581260b`) made compliant sequencers stand back, not settle.

`kb/dag_semantics.md` and `kb/sync_semantics.md` are not affected: no change to
attachment semantics, coverage, pruning or sync, only to which candidates the proposer
consumes.

### Miner

- Transit timestamp `(s, S - 3)` instead of `(s, 1)`. Transits are persisted, gossiped and
  streamed on arrival up to 6 slots ahead of their timestamp (`txinput_queue`), only attachment
  waits for the clock, so a transit submitted early is held by every node well before
  settlement and the views agree.
- `MinerVersion` 5. No other change: with the sequencers following the rule, the miner's
  prediction of the winner becomes right.

**Built, no hardfork: miners follow the included transit.** Until the ledger rule ships, a
sequencer can still take a transit before the window, and a miner predicting the winner by the
rule then builds on a loser while the taker's own miner extends the winner alone. The mining
stream now sends an inclusion notice (`{"included", "by"}`, see `api/api.md`) when a validated
sequencer milestone consumes an output of a streamed transit, and the miner's tree ranks an
included transit, and its ancestors, above any sibling (`proxi/node_cmd/mine/mine_tree.go`). It
needs the access nodes and `proxi` updated; the miner version is unchanged, the frame is additive.

## Endorsement policy: do not follow a deviating inclusion

No hardfork. Proposer policy only, the same kind as the existing rule that keeps bootstrap
transactions out of the endorsement candidates (`endorsableFrom`, `sequencer/backlog`).
`kb/dag_semantics.md` and `kb/sync_semantics.md` are not affected: attachment, coverage, pruning
and sync are unchanged. The only change is which milestones a sequencer is willing to endorse.

### Why the settlement policy alone loses

Measured 2026-10-20 on hloc0 with every one of our nodes on v1.0.5. Transits are compared by
**solution** (VRF output), not by txid: 8 of the 34 miners, the largest ones, send each solution
11 or 12 times, one transit per active sequencer, so they always have one at every sequencer
(96% of all transits). Counting those copies as separate transits overstates the problem
several times over; an early version of this section did.

- MORA's transits won 75-80% of the slots on 9% of the submissions, mostly with its copy of the
  best solution: the outcome for miners is mostly right, only the sequencer is not.
- In 30 of 200 slots (slots 17,050-17,250) a strictly worse solution won, every time through
  MORA. In 26 of the 30 the best solution's target **did** include it, mostly at ticks 88-92;
  MORA had included its pick at 86-89. The other 4 were never included.
- It falls on miners who send each solution once, that is everyone on stock `proxi`: over about
  300 slots their best solution lost 16 of 51 times when it was not addressed to MORA (31%),
  never when it was. A solution sent to every sequencer lost 2 of 235 times.
- MORA does not take transits early in any significant way: almost all of its inclusions are at
  tick 86, the first tick of the window. Its edge is speed, pace 3 against our template's 12, and
  ignoring the transits addressed to other sequencers (deferring to them came in v1.0.4, so
  MORA runs an older build or a modified one).

The settlement tick in Go is `MaxTickValue - PreBranchConsolidationTicks -
MineSettlementWindowTicks` = 127 - 25 - 16 = **86** (`settled`, `MineSettlementTick`). The
ledger rule first counted back from `ticksPerSlot64` and gave 87; aligned to 86 on
`develop-upgrade1` on 2026-10-20, before any activation, together with the wallet-side
`txbuildercore.MineSettlementTick` the miner uses.

The mechanism: under the settlement policy, every compliant sequencer whose own candidate is
not the best stands back on that parent. Its factory then endorses by coverage alone, and
MORA's milestone at tick 86 is already in the tippool before the best's target produces its
own. The standing-back sequencers endorse MORA's lineage, the best transit is left on a single
milestone, and coverage orphans it. The compliant sequencers' own rule hands the slot to the
deviator.

### Rule

A **deviating inclusion** is a sequencer milestone M that consumes, as a direct input, an
output of a mine transit T, and either:

1. **Early:** M's tick is before the settlement tick (86). Objective, the same for every node,
   and what the ledger rule of this document will later make invalid. Rare today.
2. **Not the best solution, in any slot:** eligibility never expires, but a later slot does not
   make a worse solution better (MORA took the worse transit at tick 5 of the next slot once
   the in-slot case was refused). This node's `MineTransitRegistry` held a validated transit on the
   same predecessor with a **better solution** (an older slot or a strictly smaller VRF output;
   the txid tie-break is left out, so a copy of the same solution never counts), addressed to
   an active sequencer, and **registered at least 10 ticks (0.8 s) before the settlement
   tick**. The margin keeps an honest sequencer from being refused for missing a rival that
   reached it late. Transits arrive 6-47 s before settlement (median 17 s).

**Policy.** A sequencer does not endorse a milestone that is, or is built on within its slot,
a deviating inclusion. (Until 2026-10-22 it also endorsed first a milestone that took a transit
by the rule; see Convergence.) The
walk covers only the milestones of that slot (chain predecessor and endorsements), a few dozen
vertices, and every transit they consume directly, whatever its slot.

**Convergence.** Refusing the deviator is not enough. In the first hours of v1.0.6 (slot
17,566) the best transit's target included it at tick 86, the others stood back and kept
endorsing higher-coverage milestones without it, and the deviator's branch, holding a worse
transit, won by exactly that transit's 50 PROX of coverage. The factory builds every skeleton
of a round from the first endorsement it can pair, so putting the milestones that took a
transit by the rule first in the candidate list makes the compliant sequencers converge on the
lineage holding the best transit.
**Removed 2026-10-22** (`develop-upgrade2`): the ordering put a lower-coverage candidate ahead of
a heavier one, a criterion other than coverage deciding which lineage a sequencer follows, and
only the biggest coverage rule may do that. The refusal of deviating milestones stays, and
since upgrade 1 (slot 29000) the ledger itself forbids consuming a transit before the
settlement window, which removes the early takes that made the ordering necessary. Removed the
same day: the refusal, for 3 slots after, of a lineage holding a deviating transit while the
own lineage held a better one (`d81b8d06`), which kept a sequencer off a heavier lineage. The
refusal is confined to the slot again, as below. Since slot 29000 the filter fired once in
about 17 hours on hboot, against 194 times in the day and a half before.

**Confined to the slot, no wall clock.** The factory reads ledger time only, so the refusal has
no time-out: it applies to the slot of the deviating milestone and nowhere else. In the next
slot the lineages are kept apart by the mine output itself: once the best transit is in the
honest lineage, a milestone descending from the deviating one double-spends against it, and
attachment refuses it like any conflict. If the best was never included, nothing conflicts, the
deviator's milestones are endorsable again, and the next transit, built on the deviator's pick,
carries it into every lineage: the outcome of today, for that slot only.

**Settlement pulse.** The rule only helps if the best's target includes the best inside the
window. A sequencer whose backlog holds a mine transit of the current slot builds one milestone
as soon as the window opens, ahead of its pace, once per slot, instead of waiting for its next
paced target (ours include at 88-92 today, MORA at 86).

**Miner side: no change.** When two siblings on one parent are both announced as included, the
miner's tree ranks them by the canonical rule (`betterThan` in `proxi/node_cmd/mine/mine_tree.go`:
included first, then slot, then VRF output). So it follows the lineage the honest sequencers
keep.

### Effect

The deviator's milestone keeps only its own coverage plus any sequencer that does not run the
policy. MORA holds about 93k PROX of frozen coverage against about 12M on each of ours, so its
deviating milestone is orphaned. On the measured window, the 26 of 30 slots where the best
solution was included by its target flip; the 4 where it never was should shrink with the
settlement pulse. Miners that send each solution to every sequencer, to be sure to have one at
MORA, gain nothing from it any more.

### Built (2026-10-20, no hardfork, v1.0.6)

- `sequencer/task/mine_deviation.go`: `InspectMineInclusions` (the slot walk) over
  `deviatingInclusion` (the rule for one consumed transit), `MineSettlementTick`, and
  `ActiveSequencersAt`, now shared with the proposal.
- `MineTransitRegistry` stamps each transit with the node's ledger time on arrival;
  `BetterRivalKnownBefore` answers case 2, with `betterSolutionThan` (the order without the txid).
- `backlog`: `CandidatesToEndorseSorted/Shuffled` drop deviating milestones after the tippool
  returns them (`withoutMineRuleDeviations`): the tippool runs its own filter under an exclusive
  lock, and the check reads the tippool and the tx store. The environment method is
  `MineRuleFilter`, implemented by the sequencer (`sequencer/mine_rule.go`).
- Candidate order: `withoutMineRuleDeviations` puts the milestones that took a transit by the
  rule first (`InspectMineInclusions` reports both the deviation and the take).
- Counter `proxima_seq_mine_rule_refused`; one log line per refused milestone naming the
  deviating milestone, the transit and the reason.
- Settlement pulse: `settlementPulseDue` in the pulse gate of `strategy_async.go`, using
  `HoldsMineTransitOfSlot` of the backlog.
- One transit per solution at intake (`core/core_modules/txinput_queue`,
  `firstCopyOfMineSolution`): the first transit to arrive unsolicited with a given signer,
  predecessor and VRF output is admitted, later copies are dropped before persist and gossip
  (counter `mine_copy_drop`, `core/resilience.md`). Checked after the signature, so a junk
  transaction reusing another miner's proof cannot crowd out the miner's own. A pulled copy
  always passes: a milestone that included it needs it. Which copy survives depends on arrival
  order, so in a rare race two sequencers may include the same solution; that is the same
  conflict copies from one miner make anyway, settled by coverage. Cuts the transit load by up
  to the number of active sequencers (8 miners sent 11-12 copies of every solution, 96% of all
  transits).

### Tests

- `sequencer/task/mine_deviation_test.go` (`-race`), on the public take 1 fixture transit:
  early consumption is deviating; inside the window only with a better rival known by the
  deadline (settlement tick minus the margin); a later slot never. A rival counts only when
  registered before the deadline, addressed to an active sequencer, and a better solution; a
  copy of the same solution does not.
- Not unit-tested: the walk, which needs real sequencer milestones. Covered by two local runs:
  two sequencers with one patched to take every transit at once (21 refusals, the best kept
  where it was included in time), and three sequencers with one patched to take its own
  transit in the next slot whatever the rivals: over 59 slots, 38 contested, the best
  solution won every one, 9 next-slot takes refused.

## Residual

- A sequencer can still deviate inside the window and include a transit that is not the best
  (its owner's). Without the endorsement policy above, its pick wins in practice: the compliant
  sequencers that stand back endorse it by coverage before the best's target includes the best.
  With the policy, it keeps only its own coverage. The ledger cannot do better without seeing
  competitors.
- **A milestone must strictly advance coverage, and a late transit adds none.** Coverage
  counts only outputs of the baseline state. A transit included in the slot after its own
  spends, in the next slot, an output that is not in that slot's baseline, so including the
  next transit adds no coverage and the ledger refuses a milestone that does nothing else. The
  sequencer can then include it only after the next branch, and the delay repeats from slot to
  slot. Seen in the local rehearsal (two sequencers, the deviant refused, nothing else to
  endorse): 22 of 52 of the compliant sequencer's wins were included only in the next slot,
  in runs. On the live network a sequencer nearly always has fresh milestones to endorse in
  the window, which advance coverage; a quiet network does not. Not addressed.
- Two honest sequencers whose views differ (a competitor reached one too late) may both include
  their own; rare when transits arrive well ahead of settlement, and resolved by coverage.

## Rollout

- First live library upgrade: `PendingUpgrade` with a `Build(prevJSON)` function, a
  `def_upgrade1` JSON replacing `_mineShape`, adding `constMineSettlementWindowTicks` and
  bumping `constMinerVersion`. Exercise on a local
  network from a pre-upgrade snapshot, including a snapshot round-trip, before scheduling.
- From the upgrade slot every transaction carries the new TxVersion: every node, miner and
  wallet (`proxi`) must be updated before it. Announce with a slot several days ahead.

## Tests

Ledger (`./ledger/...`): a transit stamped before tick S-3 is refused, one at S-3 accepted; a
milestone at tick S-1 cannot consume an output of a transit stamped S-3, one at S can; the
pre-upgrade library still validates historical transits. Sequencer (`-race`): a sequencer holding
a better transit addressed to an active other sequencer consumes none on that predecessor and
endorses the including milestone; it ignores a better one addressed to an inactive sequencer or
not yet validated; two sequencers holding the same competitors include exactly one transit
between them.
