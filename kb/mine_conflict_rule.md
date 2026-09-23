# Mine chain: canonical winner and pace 1

> **LIVE** — Spec in two parts. Part A, the canonical winner rule, is policy only
> (miner and sequencer), not a ledger change; built 2026-09-19 on `develop`. Part B,
> pace 1, changes the covenant and was **built on `develop-take1` on 2026-09-23** for
> the next reset, together with the take 1 emission schedule and the fixed mine
> transit fee of `kb/take1_todo.md`; what was built differently from the text below is
> listed at the end of Part B. Nothing in either part changes who wins how often: the
> expected share of transits stays the share of hashrate. The strategic reasoning
> behind both is in `.internal/lottery_mining.md`.

## Problem

Several miners regularly hold a valid solution for the same step of the mine chain. Today
which one wins is decided by the sequencers: a mine transit is an ordinary tag-along
(`sequencer/backlog`, `sequencer/task/proposal.go`), candidates are ordered fee first and
oldest first, and of two transits consuming the same predecessor the one a sequencer
inserts first wins while the other conflicts in the past cone and is skipped. Different
sequencers insert in different orders, so the outcome depends on inclusion order and, in
the centralized phase, on the founder's sequencers. The reference miner already ranks
competing transits deterministically (`mineTreeNode.betterThan` in
`proxi/node_cmd/mine_tree.go`), but its last two keys are the tag-along fee, which the
miner chooses, and the txid, which the miner can grind through free fields of the
transaction.

Two consequences. Whoever finds a solution first, or is best connected to the sequencers,
has an edge that has nothing to do with work. And at the difficulty cap, where every slot
has several solutions, inclusion order decides everything.

## Part A: the canonical winner rule (now)

### The rule

Among valid mine transits that consume the same predecessor output, the winner is decided
by, in order:

1. the smaller successor slot (`txSlot`): the earlier slot required the higher K, and a
   miner cannot stamp a slot earlier than its predecessor allows;
2. the smaller VRF output: the 64-byte value `vrf.ProofToHash(proof)` compared as bytes
   (`bytes.Compare`), the same value the covenant tests for trailing zeros
   (`_mineBeta64` in `ledger/def/lock_mine.easyfl` is its last 8 bytes). It is fixed by
   the miner's key and the message, so it cannot be chosen; a miner can only find another
   valid solution with a smaller one, which is more work;
3. the smaller txid, as a last resort that never decides in practice.

Chain height stays ahead of all three in the miner's tree, since the tree compares across
steps. The tag-along fee is removed from the ordering.

The smaller of independent draws is equally likely to be any of them, so a miner's chance
of winning a contested step is its share of valid solutions, i.e. its share of hashrate.
The rule changes who wins a given contest, not how often anyone wins.

The ledger does not enforce the rule and cannot: a transaction does not see its
competitors. It is a policy in the same sense as the health threshold on branches: every
sequencer applying it converges on the same transit; one that does not gets its milestone
orphaned by the rest.

### Sequencer

Implemented in `sequencer/task/mine_transits.go`, applied by `insertTagAlongInputs` in
`sequencer/task/proposal.go` after the consumed-in-past-path purge and before the
fee-first sort:

- **Recognize mine transits** among tag-along candidates: the producing transaction is
  `IsMiningTransaction()` (`ledger/transaction/tx.go`). Its predecessor is the single
  input's output ID, its VRF output comes from the unlock parameters of that input
  through `vrf.ProofToHash`. That function does not verify, and a forged proof can be
  given any hash cheaply, so a transit is ranked only once its vertex has passed full
  validation; the account listener fires at attach time, before that, and a candidate
  that is not yet validated simply waits for a later milestone.
- **Group by predecessor** and insert only the best of each group by the rule. The
  losers stay in the backlog and leave through the existing purge once the LRB shows
  the predecessor consumed. Ordinary tag-alongs are untouched.
- **Settlement window.** A mine transit whose successor slot is `s` becomes eligible only
  for milestones whose target tick in slot `s` is inside the last
  `mineSettlementWindowTicks` (16) ticks before the pre-branch consolidation zone, which
  takes no tag-alongs (`IsPreBranchConsolidationTimestamp`), as branches do not either;
  and in every later slot. This is what gives competitors time to arrive before the
  choice is made; without it a sequencer that has already consumed a transit cannot swap
  a better one in. An input can in any case only be consumed by a milestone later than
  its own timestamp, and the miner stamps tick 1 of the successor slot, so the hold costs
  less than one slot on top of what the pace already implies.

Two sequencers that saw different sets at settlement can still pick differently; that is
the late-arrival case, settled by coverage as today, and rare once the settlement tick
leaves room for one gossip hop.

### Miner

`proxi/node_cmd/mine_tree.go`, `mine.go`, `mine_stream.go`:

- `betterThan`: height, then successor slot, then VRF output, then txid. The transit's
  VRF output comes out of `vrf.Verify` in `verifyMineTransit` and is carried on the tip
  (`mineTip.vrfOutput`) in place of the tag-along fee.
- Own transits get no preference (already the case).
- Deferred to Part B, where contests are the normal case: keeping the round open on a
  contested slot and submitting a better own solution.

### Node ingress

Unchanged in Part A. The per-sender pace gate (`TransactionPace`, 12 ticks) only bites a
miner that improves its own solution within a slot, which is Part B behaviour; the
exemption ships with it (see Part B), since the floor proof-of-work gate reads the
unverified proof hash and is the only other per-transaction gate.

### What does not change

The covenant, the message, the proof placement, the stream, the monitor. Pace, reward,
difficulty and emission are as they are.

### Tests

- `proxi/node_cmd/mine_tree_test.go`: ordering by VRF output, fee ignored, txid last.
- `sequencer/task/mine_transits_test.go`: the order (slot, VRF output, txid); two
  transits on one predecessor, only the smaller VRF output stays whatever the arrival
  order; a transit is not eligible before its settlement window and stays eligible in
  later slots.
- The settlement hold keeps a transit eligible in every later slot, so a slot without a
  regular milestone after the settlement tick delays, never drops, it.

## Part B: pace 1 (built on `develop-take1`)

### Goal

One step of the mine chain per slot, with the winner of each slot known to everyone at
the same moment. Transits ~4x more frequent and ~4x smaller for the same emission, and no
failure mode at the difficulty cap: beyond it the slots are simply all full and Part A
decides.

### Covenant (`ledger/def/lock_mine.easyfl`)

- **Minimum pace 1**: `constMineMinPace = 1`. `_mineM >= 1` is then always true for a
  successor in a later slot; the check stays as the guard against same-slot successors.
- **Relief from gap 1**: unchanged formula `K = max(B − (M − P), E)` with P = 1: full B at
  gap 1, one bit easier per empty slot.
- **Retarget.** The gap is the only signal the ledger sees, and at pace 1 it can only say
  "at least one solution" (gap 1) or "none" (gap ≥ 2). A symmetric ±1 controller settles
  with half the slots empty. Replace `_mineAdjustedB` with an asymmetric one, and add a
  third argument to `mineLock`:
  - `$2 C` (z64): count of consecutive gap-1 transits since the last harden;
  - gap 1: `C' = C + 1`; if `C' = constMineHardenAfter` (k) then `B' = B + 1` (clamped
    at the cap) and `C' = 0`, else `B' = B`;
  - gap M ≥ 2: `B' = max(B − (M − 1), E)`, one bit per empty slot, and `C' = C`.
  With k full slots per harden and one ease per empty slot, the equilibrium has k/(k+1)
  full slots. k = 8 gives ~11% empty slots and, from the Poisson count, ~2.2 valid
  solutions per full slot: a contest in about two slots out of three, which Part A
  settles. Empty slots cost emission time, contests cost nothing, so k on the large side.
  `constMineTargetPace` is retired.
- **Cap.** Raise `constMineMaxDifficulty` from 40 toward the 64-bit wall (the PoW tests
  the low 64 bits; 56 leaves margin). At pace 1 a solving window is one slot, so the cap
  is reached at ~9x the hashrate of today for the same K; beyond it nothing breaks.
- **Schedule.** Emission per slot is A over the mean pace. Sized at the realised pace
  of 1.12 (the equilibrium of the retarget with k = 8): the take 1 schedule of
  `kb/take1_todo.md`, 95 PROX per transit for 60 days, then +134 motes per slot.
  `constMineRemainingInit` unchanged. The tag-along fee is a fixed 1 PROX
  (`constMineTagAlongFee`) in place of the cap at 1% of A.
- Go mirrors: `MineLock{R, B, C}` and `MineLockTemplate` in `ledger/lock_mine.go`;
  `MineLockView`, `MineRequiredK`, `MineAdjustedB` in
  `ledger/txbuildercore/helpers_mine.go`; `verifyMineTransit` in
  `proxi/node_cmd/mine_verify.go`; genesis seeding of C = 0 in `ledger/genesis.go`;
  `ledger/tests/mine_test.go` and `mine_schedule_test.go`.

### Miner

- Successor slot is `predSlot + 1`, or the current slot if later (`successorSlot`,
  unchanged logic with P = 1). `mineMaxFutureSlots` is irrelevant in practice.
- The timeline per slot `s`: transits for slot `s` are submitted during slot `s`; the
  winner is known to every miner from the stream by the settlement tick of `s`; all
  miners grind step `s + 1` on it from then until the settlement tick of `s + 1`. Before
  the settlement of `s`, a miner grinds speculatively on the best-known transit of `s`
  and switches when a better one arrives, which the tree already does (`superseded`).
- Round deadline: the settlement tick of the target slot, replacing the adaptive refetch
  window. Stall handling (`stallTimeout`, `mineConfirmationStall`) in slots rather than
  seconds; 90 s is 9 slots and can stay.
- The tree bounds (`mineTreeMaxNodes = 512`, `mineTreeKeepBelowRoot = 8`): eight
  steps below the root is ~80 s at one step per slot. Check against the LRB lag the
  tree re-roots on; raise `mineTreeKeepBelowRoot` if the lag can exceed it.

### Sequencer and node

Part A as is; the settlement tick is the same constant. Two pieces deferred from Part A:

- **Miner keeps the round open on a contested slot.** It submits a solution as soon as
  it has one and keeps grinding the same predecessor and slot until the settlement tick
  or until its own best is the best known; a solution with a smaller VRF output is
  submitted too. The two transactions conflict on purpose; the rule picks one.
- **Pace-gate exemption.** The per-sender pace gate in `core/core_modules/txinput_queue`
  (`TransactionPace`, 12 ticks) silently drops the second submission above. Mine
  transactions are exempted from it as they already are from the unknown-holder check.
  The floor proof-of-work gate then remains the only per-transaction gate, and it reads
  the unverified proof hash, which can be forged cheaply; whether it needs verifying
  the proof at ingress is to be decided with this change. Test: mine transactions pass
  the pace gate. Load rises from ~1,900 transits
a day to ~8,000 plus contested losers, ~20,000 mine transactions a day at λ ≈ 2 with
one VRF verification each. Miners that watch the stream do not submit a solution worse
than the best known, so above the cap submissions grow only as the running-minimum
records of λ draws, about 1 + ln λ per slot.

### Consolidation

Payouts are ~4x smaller and ~4x more numerous; `proxi node consolidate` defaults
(`threshold_prox`, `compact_at`) may need lowering. Storage deposit of a ~125 PROX
payout is not an issue (minimum ~9.25 PROX).

### Sensitivities

A solution found after the settlement tick of its slot is lost, and a miner that learns
the winner late loses that part of its window. Both favour well-connected miners
regardless of hashrate, bounded by one gossip hop. Clock accuracy matters more than
today. Forked slots give two predecessors for the next step; miners follow their node's
best and one side loses a slot of work.

### As built (2026-09-23)

- Covenant: `mineLock(R, B, C)`; `_mineAdjustedB` and `_mineAdjustedC` with
  `constMineHardenAfter` = 8; `constMineTargetPace` retired; cap 56; the pace check
  moved into `_mineShape` so that it precedes the retarget, whose empty-slot count
  would underflow on a same-slot successor. The fee rule is `equalUint(fee,
  constMineTagAlongFee)`.
- Go mirrors: `MineLock{R, B, C}`, `MineLockView`, `Constants.MineRetarget` returning
  both values, `MineHardenAfter` and `MineTagAlongFee` in the constants, genesis
  seeds C = 0. The settlement window width lives in `txbuildercore`
  (`MineSettlementWindowTicks`, `Constants.MineSettlementTick`) so the miner and the
  sequencer read one constant.
- Miner: the fee is the ledger's, `--fee` is gone; a round ends at the target slot's
  settlement tick, and a target whose settlement has passed is skipped to the next
  slot; the adaptive refetch window stays as an upper bound within that. After its
  own solution the miner keeps grinding a contested slot for a smaller VRF output
  (`grindContested`, with `mineParallel` taking the output to beat) while a
  competitor outranks it and settlement is ahead, and submits each improvement; the
  tree tells it who leads on the predecessor (`bestOnParent`) and the stream does not
  abort it meanwhile (`contested`). Stall timeouts and the tree bounds are unchanged.
- Node: mine transactions are exempt from the per-sender pace gate in
  `txinput_queue`, so an improved solution stamped in the same slot is not dropped;
  the floor proof-of-work gate stays the only per-transaction gate and still reads
  the unverified proof hash.
- Monitor: `target_pace` is now the retarget's equilibrium (k+1)/k, with
  `harden_after` beside it.
- Tests: the ledger mine tests run at P = 1 with harden-after 2, cover the harden,
  the per-empty-slot ease, both clamps, the fixed fee above and below, and the
  same-slot successor; the wallet mirror and the verifier tests follow.

## Out of scope

Random reward drawn from the VRF output, one draw per key per slot, VDF tickets,
trustless share inclusion. Reviewed in `.internal/lottery_mining.md`; parked.
