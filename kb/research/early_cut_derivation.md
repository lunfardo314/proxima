# Early cut against a broader extension: derivation on slot 37786

> RESEARCH (2026-10-23). A derivation from the ledger's coverage definitions and the
> transactions of slots 37785-37786, read from the DAG (dag explorer on hloc0-acc), checked
> against the sequencer log. Question: can a follower's first move on a branch cut early in the
> previous slot have more coverage than any broader extension, with post-branch consolidation
> (first move at tick 9) and a group that endorses only its own milestones? Context:
> `kb/research/branch-bias.md` (fourth input).

## Definitions (from the code)

For a transaction T with baseline branch B (`attacher.FinalLedgerCoverage`,
`PastCone.CoverageDeltaRaw`, `ledger.Coverage`):

- **Coverage delta** Δ(T): the sum of w(o) over the outputs o consumed in T's past cone (the part
  not in B's cone) that are present in B's state, plus the bonus of B's sequencer output if T's
  cone leaves it unconsumed (3-5 PROX, ignored below).
- **Output weight** w(o): a plain output, its balance; a delegation, 0 while frozen; a sequencer
  chain output, its balance plus the frozen coverage of the delegations on it. A founder's
  (bootstrap) sequencer chain output weighs about 12.30T.
- **Coverage** of a non-branch T: LC(T) = LC(B)/2 + Δ(T).
- **Branch** B' of slot s+1 on the previous branch P: LC(B') = LC(P)/2 + Δ(B'), carried on the
  stem (`stemLock`, total coverage argument); Δ(B') is in the branch's sequencer constraint.

## The transactions (from the DAG)

| Transaction | What it is | Read from the DAG |
|---|---|---|
| P = `s37785-0-014343d02928` | the branch of 37785 every branch of 37786 extends | LC(P) = 124,917,971,929,145 |
| `s37785-5-001c0245c1fe` | oseq1's first move of 37785: extends its own branch, endorses nothing | Δ = 12,304,494,062,842 |
| `s37785-13-0037b5737a2a` | oseq1's last milestone of 37785; endorses two peers' tick-9 milestones | Δ = 62,868,932,361,323 |
| E = `s37786-0-01b78b0c620e` | MORA's branch of 37786, extending MORA's own `s37785-3` | Δ(E) = 62,602,981,544,650; LC(E) = 125,061,967,509,222 |
| L = `s37786-0-01902f09e1e0` | oseq1's branch of 37786, extending its own `s37785-13` | Δ(L) = 62,868,932,361,323; LC(L) = 125,327,918,325,895 |
| `s37786-5-0013befe5723` | oseq1's first move of 37786: extends its own `s37785-13`, endorses MORA's `s37786-3` | Δ = 62,164,602,469,119 |

Check: LC(P)/2 + Δ(E) = 62,458,985,964,572 + 62,602,981,544,650 = 125,061,967,509,222 = LC(E);
the same for L. And oseq1's first move: LC(E)/2 + Δ = 62,530,983,754,611 + 62,164,602,469,119 =
**124,695,586,223,730**, the coverage in oseq1's log (`SUBMIT SEQ TX s37786-5-0013befe5723`), to
the mote.

## Why the two branches have almost the same coverage

Δ(E) and Δ(L) differ by 0.27T only. E is cut at tick 3 of 37785, before the founder's sequencers
spent their chain outputs at ticks 5-13, so E does not contain 37785's activity. But MORA's tick-3
milestone endorses Luhmann's tick-2 milestone, which carries 37784's activity (Luhmann's tick-127
milestone of 37784 collected it). So E holds one slot of activity, 37784's, and L holds one slot
too, 37785's. Each delta is about one slot's capital, about 62.6T, mostly frozen coverage on the
sequencer chain outputs (E's stem records 62.60T frozen).

## Every move oseq1 can make at its first move of 37786

Its own outputs: its branch L, and its last milestone of 37785, `s37785-13` (M). Endorsements must
be of slot 37786. The moves, with their coverage:

| # | Extends | Endorses | Δ | LC | Source |
|---|---|---|---|---|---|
| 1 | its branch L | nothing (no milestone is on L but its own) | ≈12.30T, its own chain output | 62.66 + 12.30 = **≈74.97T** | Δ as measured for the same move in 37785 |
| 2 | M | another founder's branch cut at tick 9-49 (it contains M, or M's content) | ≈12.30T: M's output, in that branch's state | ≈62.66 + 12.30 = **≈74.97T** | derived |
| 3 | M | MORA's bare branch E | 37785's spends of the founder's chain outputs, in M's cone, all present in E's state: ≈61.5T and more | ≈62.53 + 62 = **≈124.6T** | derived |
| 4 | M | MORA's `s37786-3` on E (actual) | 62,164,602,469,119 | **124,695,586,223,730** | DAG, exact |

Moves 3 and 4 beat 1 and 2 by about 49.7T. The difference is all in Δ: the branches weigh the same
(LC/2 within 0.13T), but on E oseq1's own cone brings 37785's activity in, while on L that activity
is already inside the branch.

## With the 9-tick buffer

From slot 45000 nobody builds before tick 9. Assumptions from the measured arrival times: 94% of
branches arrive within 6 ticks, 99% of the founder's within 12; a milestone needs 1-3 ticks to
reach the others.

- **At tick 9** every follower moves at once, and none sees the others' tick-9 milestones. Its
  options are moves 1-3: the bare branches and its own outputs. Move 3, on the bare early branch,
  is worth ≈124.6T against ≈75T for any late branch. Every follower chooses the early branch on
  its own; no endorsement by the group is needed for it.
- **After tick 9**, all followers are on E. A follower that went back to a late branch would
  re-extend M there (the earlier-slot re-anchor) and find no one else's milestone on it: ≈75T.
- **Fully coordinated counterfactual**: all five founder's sequencers move onto one late branch L*
  at tick 9 (each against its own best move), and a milestone at tick 12 endorses all five:
  LC(L*)/2 + 5 × 12.30 ≈ 62.66 + 61.52 = **≈124.2T**, against ≈124.6T on E. The early lineage
  still leads, because its followers' deltas carry the whole previous slot, everyone's activity,
  while L* has only the five moves of the current slot so far.

**Role of the biased endorsement.** The group endorses only its own milestones. That keeps their
branches free of the founder's activity and gives each branch its previous-slot content through
the collector. It is not needed at the decision point: after slot 45000, move 3 alone decides.

## Conclusion

With the ledger's definitions and the real transactions, a follower's best first move is onto the
branch cut early, by about 50T over every broader extension, and the gap survives the 9-tick
buffer and full coordination of the founder's sequencers (≈124.6T against ≈124.2T).

The step it rests on: one slot's activity counts in full in Δ when the follower's own past cone
brings it onto a branch that lacks it, and at half when it is already inside the branch (LC(B)/2).
A lineage that keeps each slot's activity out of its next branch shows a full slot in the deltas
of its first moves, where a current lineage shows only the moves made so far. That is the drop at
the slot boundary, avoided by consolidating one slot late.

Moves 1-3 except the measured ones are derived from the definitions, not issued; the inputs they
rest on (12.30T per chain output, Δ of the branches, the content of M's cone) are read from the
DAG.
