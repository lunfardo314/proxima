# Branch fork convergence: ordering Phase-2 baselines by a network-wide key

> **LIVE** — Why sibling branches of one slot split over which parent stem they consume. Proposal, not implemented; the measurement is complete.

Status: **proposal, not implemented.** Measurement is complete and reproducible; the mechanism
is partly inferred and the inference is flagged as such below.

## The phenomenon

Sibling branches of one slot always conflict — each spends the stem of the parent branch — so
every sequencer must adopt exactly one parent. A *fork* here means the branches of slot N split
over which slot-(N-1) stem they consume:

```
slot 58716:  3 branches -> s58715-0-017c34625dcf   (hloc0, oloc1, oseq1)
             2 branches -> s58715-0-01db8e207833   (hboot, oloc2)
```

## What was measured

3.4 h on the 5-sequencer testnet under 200 senders, 1206 slots (2026-08-11). Collector and
analysis live in the gitignored `.internal/` (`fork_probe.py`, `fork_watch.sh`,
`fork_analyze.py`); the probe parses the `dag_explorer/slot` graph on the node and returns
compact lines.

| observation | value |
|-------------|-------|
| slots forked | 182 / 1206 = **15.1 %** |
| fork arity | **2-way in 182 of 182 — no 3-way at all** |
| fork depth | 133 runs of 1 slot, 20 of 2, 3 of 3 (longest 3) |
| 1-vs-4 splits | 52.7 % (uniform baseline 33.3 %) |
| 2-vs-3 splits | 47.3 % (uniform baseline 66.7 %) |
| LRB lag | at the structural floor of 1 in 15 of 20 samples |

Per-sequencer minority participation: hboot 37.9 %, oloc1 30.8 %, oloc2 30.8 %, oseq1 24.7 %,
hloc0 23.1 %.

### Two hypotheses the data kills

**Spatial / latency clustering.** The exact Hetzner{hboot,hloc0} / OVH{oloc1,oloc2,oseq1}
partition is the second-rarest of the 15 possible, 3.3 % against a 6.7 % uniform baseline — half
chance rate, not above it. The endorsement matrix is likewise provider-blind (40.8 % intra-provider
against 40.0 % expected). Inter-box RTT is ~13 ms and attachment 1.7–7.5 ms average / 26 ms worst,
against the ~1040 ms from the slot boundary to the first milestone at tick 13, so candidate
availability is nowhere near the binding constraint.

**Independent per-node tie-breaking.** Five sequencers choosing independently among five equal
parents would fragment three and four ways routinely. Zero 3-way forks in 182 says the ranking is
near-deterministic network-wide and that divergence is *individual* — one node departing from a
consensus the other four share, which is exactly the 1-vs-4 shape.

## Where the choice is made

A sequencer's lineage for a slot is fixed by its **first milestone of that slot**: the chain
predecessor is cross-slot, so `Transaction.BaselineDirection()` returns `MustEndorsementAt(0)`.
Confirmed against raw transactions at slot 58715 — hboot endorsed oloc2's branch, orphaning its
own; oloc2 had `Endorsements(0)` and extended its own branch.

Phase 1 of `factory.chooseFirstExtendEndorsePair` cannot adopt a peer at this point: extending
the own branch while endorsing a sibling is the stem conflict. So the decision falls to Phase 2,
whose candidate order comes from `rankedUniqueBaselines`:

```go
sort.SliceStable(ret, func(i, j int) bool {
    return !ret[i].pending && ret[j].pending   // committed first
})
// pending: f.Branches().IsPending(bid)
```

Endorse candidates arrive in a deterministic coverage-descending order, but this stable sort
promotes whichever siblings are **already committed on this node**, and commit status is a
per-node race. Two nodes holding the identical five branches can therefore rank them differently,
and coverage cannot break the tie because pre-branch consolidation is designed to equalise it
(byte-identical `coverage_delta` observed at 58715, 58716 and 58717).

> **Inference boundary.** That local `IsPending` ordering is the leading candidate, consistent
> with every measurement, but it has not been demonstrated directly — doing so needs the
> `IsPending` state of each node captured at the same boundary. What *is* established is that the
> choice is near-deterministic network-wide and that divergence is individual.

## Proposal

Order Phase-2 baselines by properties every node computes identically from the branch itself,
and demote local commit status to a tiebreak of last resort:

1. **branch inflation bonus** (VRF-derived; `SequencerOutput.Output.Inflation()`, exposed as
   `branch_inflation`). This is what the fair-launch design already nominates to decide between
   equal-coverage branches, so using it here aligns baseline adoption with branch fork choice.
2. **`base.LessTxID`** beneath it, since the bonus can itself tie. This mirrors
   `vertex.IsPreferredMilestoneAgainstTheOther`, which already resolves equal coverage this way.
3. Keep committed-before-pending only *within* an otherwise exact tie, where it is a pure
   trie-read cost optimisation and cannot change the outcome.

Expected effect: all sequencers holding the same candidate set adopt the same parent, leaving
only genuine propagation gaps to cause splits — which should convert most of the 1-vs-4 forks
into clean slots.

### Costs and risks

- Committed-before-pending exists to read the cheapest branch state first. Ranking by a global
  key will sometimes read a pending branch instead; the trie-read cost needs measuring, though
  Phase 2 runs at most once per slot per sequencer.
- This changes how the network converges, so it is a design decision rather than a bug fix. It
  should be taken deliberately, not folded into an unrelated change.
- It does not touch the two other open issues: the ~5 % of slots where a sequencer refuses to
  branch on the 7/12 health gate, and the structural gap where all three proposers refuse after a
  genuinely missed branch.

### Validation

Re-run `.internal/fork_watch.sh` for a comparable window under the same load and compare against
this baseline. The discriminating numbers are the fork rate (15.1 %), the 1-vs-4 share (52.7 %)
and the depth distribution. Fork *depth* matters more than rate for scaling: 15 % at depth 1 is
noise, the same rate at depth 3+ would mean convergence is losing to fork generation.
