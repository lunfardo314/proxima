# Mine transit settlement: network upgrade

> SPEC. **Sequencer policy built 2026-10-19 (no hardfork); ledger rule and miner change not built.**
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
   settlement tick `S = ticksPerSlot64 - constPreBranchConsolidationTicks -
   constMineSettlementWindowTicks` (new constant, 16, equal to the Go
   `txbuildercore.MineSettlementWindowTicks`; today S = 87, so the tick must be >= 84). A
   sequencer milestone needs only the sequencer pace (3 ticks) to consume an input and a
   non-sequencer transaction needs 12, so no output of a transit can be consumed, by anyone or
   in any way, before tick 87 of its slot. Compliant or not, no sequencer gets there first.
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
  predecessor and endorses the milestone that includes the best once it sees it.
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

## Residual

- A sequencer can still deviate inside the window and include a transit that is not the best
  (its owner's). It can no longer pre-empt: its pick conflicts with the one the honest
  sequencers make, and the lineage decides by coverage. The ledger cannot do better without
  seeing competitors.
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
