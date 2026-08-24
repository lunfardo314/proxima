# Mine chain winner-take-all bias

> **QUEUED → `participate/mine.md`** — Winner-take-all bias on the mine chain. Diagnosis stands; resolved by other work, the candidate fix was never needed.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

Status: **RESOLVED** (2026-08-15). The diagnosis below stands and is worth
keeping; the `constMineMaxPace` candidate fix was never needed. Both halves of
the causal chain were closed by other work:

- **The root cause — difficulty collapsing to the floor** (steps 1–3 below) is
  gone. It was a consequence of flat `K = B`, which pinned the pace at the floor
  and let B ratchet. The pace-relieved `K = max(B − (M − P), E)`
  (`fairlaunch.md` §8) replaced it. Measured live 2026-08-15: B stable at 20–21
  against a floor of 10, pace 47.8 s while mining is active vs a 41 s target
  (`fairlaunch.md` §8.9). With solve time comparable to the pace, PoW decides
  heights again, which is exactly what step 3 said was missing.
- **The latency head start** (step 4) is addressed client-side by
  [`mining_tx_streaming.md`](mining_tx_streaming.md), now IMPLEMENTED: nodes
  stream mining txs at gossip speed, the miner subscribes by default and to
  several nodes if asked, verifies transits itself, and its fork-choice never
  prefers a transit for being its own.

The general form stated below — *the ratchet appears whenever solve time falls
well below the pace floor* — remains the thing to watch for. It is now
structurally prevented rather than merely unobserved, but a difficulty floor
that is too high for the live hashrate would reintroduce it.

## Observation

Three fair-launch miners running concurrently (`proxi node mine`, commit
`2c0625f3` — speculative mining), each against a different node:

| miner | machine | node API |
|-------|---------|----------|
| loc0 | proxima02-1 | 65.21.170.230:8001 |
| seq1 | proxima03 | 79.137.70.25:8001 |
| loc1 | proxima04-ams | oloc1:8001 |

loc0 won every single height. seq1 and loc1 never landed a confirmed transit.

## Evidence

From the miners' own terminal output — all three solve every transit and
submit successfully, so this is not a communication or configuration failure.

All three target the **identical slot** for each height (#728 → 9044, #729 →
9048, #730 → 9052), because `succSlot = predSlot + targetPace` is fully
deterministic. Every height is therefore a dead heat decided by propagation
order, not by work.

loc0 runs uninterrupted at `(speculative, +2)` and confirms #725–#728, all its
own. seq1 and loc1 repeat this cycle indefinitely:

```
SOLVED transit #728 ... submitting 9044-1-02a3d1fb77a8     <- seq1's
transit #728 confirmed as 9044-1-02a17909973d — not ours   <- loc0's
re-anchoring on confirmed transit #728
mining transit #729 ...                                     <- loc0 submitted #729 ~40s earlier
```

Measured: K=11 against a floor of 10; 2k–7k attempts per solve; ~41 s per
transit at ~10 s slots; LRB confirmation lags ~2 transits (~8 slots, ~80 s).

## Causal chain

The ratchet is a symptom, not the root cause. In order:

1. **The pace is miner-discretionary, and longer stamps ease difficulty.**
   `_mineAdjustedB` compares `span = txSlot - s3` against
   `4 * constMineTargetPace = 16`. `constMineMinPace = 3` bounds M from below;
   nothing bounds it from above — an older stamp is equally valid. So the miner
   picks: stamp 3 → span 12 → harder; stamp 4 → span 16 → hold; stamp 5 →
   span 20 → easier. Stamping long is the dominant strategy and is collectively
   self-reinforcing.

   Note `lock_mine.easyfl` asserts miners will "mine at the shortest allowed
   step". The incentive runs the other way.

2. **Difficulty therefore collapses to the floor.** Observed K=11 vs floor 10.
   The retarget is not holding the pace; it has bottomed out.

3. **At the floor, PoW decides nothing.** Solve time (tens of seconds) is far
   below the pace floor (~40 s), so every miner is solved and idle, waiting for
   the earliest legal slot.

4. **A race everyone has already won is decided by latency, and latency has a
   systematic winner.** The producer of transit N knows N's txid instantly;
   everyone else learns it via gossip, or — with the current client — via LRB
   confirmation ~8 slots later. Whoever wins once is permanently ahead.

The general form: **the ratchet appears whenever solve time falls well below
the pace floor.** The retarget is supposed to prevent exactly that, and is
inert because the signal it measures is chosen by the party it constrains.

## Dead end: freshness anchor

The first idea was to force a mine tx to commit to information that only
becomes public after its predecessor — an explicit baseline branch with
`slot > _minePredSlot`. `txExplicitBaseline` is readable from EasyFL and a
branch txid carries its own slot and branch flag, so the check needs no state
access.

**Not viable:** a mine transaction is non-sequencer, and both explicit
baselines and endorsements are sequencer-only. A mine tx cannot reference a
branch at all.

## Candidate fix: bound the pace from above

Add a maximum pace beside the minimum in `_minePaceAndPoW`:

```
require(lessOrEqualThan(uint8Bytes(_mineM), uint8Bytes(constMineMaxPace)),
        !!!mine_pace_above_maximum)
```

with the retarget dead band retuned around it. Once M is confined to
`[P, Pmax]`, easier difficulty can no longer be bought by stamping long; the
retarget can only be held down by genuinely slow mining, so B rises until solve
time is comparable to the pace. Solve times are then exponentially distributed
with mean ≈ one pace, a gossip-latency head start is a small fraction of the
work required, and wins distribute by hashrate.

Raising `constMineFloorDifficulty` is worth doing too, but it is a stopgap that
has to track hardware. The pace bound is the structural change.

This is a hardfork: new constant plus a changed retarget body.

## Open questions

- **Liveness under `Pmax`.** A miner too slow to solve within `Pmax` has no
  legal stamp left and cannot produce a transit at all. Difficulty eases only
  one bit per transit, so a sharp drop in hashrate is the stall risk. How much
  headroom does `Pmax` need — `2 * targetPace`?
- Does anything else depend on M being unbounded above?
- Is there a retarget signal that is not miner-chosen at all? Nothing in a
  non-sequencer tx obviously supplies one.
- Where should `constMineFloorDifficulty` sit so the floor is not the binding
  constraint on plausible hardware?

## Client-side contribution (separate from the protocol question)

`proxi node mine` stamps at `MineTargetPace`, which lands every transit exactly
in the retarget dead band and freezes B. Stamping at `MineMinPace` floored by
the wall clock is the honest signal — a slow solve then shows up as a longer
span on its own. This is a miner's free choice today, which is precisely why
the constraint has to enforce it rather than the CLI.

The client also detects a lost height via LRB confirmation (~8 slots) rather
than at gossip speed, which inflates the head start well beyond its structural
size. Fixing that alone would not remove the bias, only shrink it.
