# The sequencer skeleton search space

Status: **semantics and measurements, no accepted proposal.** The search space and the failure
it produces are established; three attempts to change the search have been reverted. Extends
the TSF design in [`claude/sequencer.md`](sequencer.md), which specifies a single factory —
still what is in the tree.

## The search

Choose an own chain output to extend and a set of peer milestones to endorse, maximising the
result. Three properties define the problem:

- **Reverting own state is inside the search space.** Extending an *earlier* own output, and so
  orphaning the milestones built after it, is how a sequencer resolves a conflict. A normal
  move, not an exception path. Traversing the own past cone for extend candidates is therefore
  **required**, not an optimisation to be justified.
- **The space is exponential and changes asynchronously.** Candidates arrive and are replaced
  during the slot; every combination can conflict with every other.
- **No optimal algorithm exists.** Everything here is a heuristic; the question is which ones
  and how many.

What is in the tree is one heuristic that commits early:

1. **Seed** — `chooseFirstExtendEndorsePair`: extend the own chain **head only**, walk endorse
   candidates coverage-descending, take the **first** that reconciles. Re-anchoring through a
   branch's committed state is a fallback, reached only when extending own state fails
   entirely.
2. **Climb** — `improvementLoop`: add one endorsement at a time to a single incumbent, keep the
   best, never backtrack.
3. **Score** — `FinalLedgerCoverage`, nothing else.

## The landscape is disconnected

Sibling branches of a slot all spend the parent's stem, so they mutually conflict: a skeleton
seeded on lineage A can never absorb a milestone anchored to B — the attacher rejects it as a
**conflict**, not as a lower score.

So the landscape is **disconnected basins, one per lineage**. A hill climb cannot cross between
them. The first-fit seed alone decides which basin the whole slot's work happens in, and the
only move that changes basin is the revert (or a re-anchor through committed branch state).

This is the whole difficulty. It is not that the search finds a poor optimum inside its basin;
it is that the basin is chosen once, cheaply, and never revisited.

## Why it is a security matter

An attacker sends conflicting transactions whose tag-alongs are aimed at *different*
sequencers. Only one of those sequencers can consume a given tag-along, so the others **must
revert** in order to consolidate. A sequencer that cannot revert — or that has committed to a
basin at the start of the slot and cannot leave it — cannot resolve this. Repeated, it holds
sequencers in incompatible basins and decays consensus.

The revert capability is therefore the defence, not a performance nicety. Tag-alongs being
re-added to the reverted state afterwards is the normal, expected outcome — that is what
resolving the conflict looks like.

**Reproducing it:** `multispam conflict` (in the `proxima-multispam` repo) is exactly this
attack as a test tool. Each round spends one set of inputs with N transactions, each aiming its
tag-along at a different sequencer, spaced one transaction pace apart so the per-holder input
filter does not drop them before they are gossiped. Until it existed, the failure was only ever
observed as fork statistics after the fact.

## Measured

3.4 h, 1206 slots, 200 senders, on the single-factory search:

| quantity | value |
|----------|-------|
| slots forked | 15.1 % |
| fork width | every fork 2-way, none 3-way |
| one-against-four share | 52.7 % |
| fork depth | 73 % resolve within one slot |

At slot 60662 three of five sequencers spent the whole slot on a branch with 1.05×10⁹ less
coverage and a smaller past cone (22 seq + 320 non-seq against 24 + 342).

Latency clustering as an explanation was **refuted**: classifying each milestone by its own
reachable parent branch gives zero cross-lineage endorsements, and the partitions do not
follow network distance.

## Three reverted attempts

| commit | change | measured |
|--------|--------|----------|
| `20af2f51` → `d21de415` | seed evaluates own head and branch re-anchor, takes the heavier | coverage/supply 1.960 flat → oscillating 1.542–1.960 within a minute, network-wide; ~20 % CPU, 5–7× attachment p95 |
| `d6319056` → `94c6d21f` | two heuristics — **but the main hunk never applied**, so both factories ran the same greedy search; only the `(numSeq, coverage)` scoring took effect | coverage/supply min 1.64, num_seq min 3, CPU +30–44 % |
| `ad0654fa` → `1b782130` | two heuristics, correctly wired this time; re-anchor evaluated every round, winning by 1 % | CPU 2.14 cores against 0.72, coverage/supply min 1.03, bootstrap transactions every ~40 s |

Three lessons that any further attempt inherits:

- **Sibling coverages differ by ~0.001 %, and pre-branch consolidation is *designed* to equalise
  them** so the VRF bonus decides. The objective is flattest exactly where the decision is made,
  so a rule that moves on any advantage churns. Any new rule must be damped by something that is
  not raw coverage.
- **Evaluating the re-anchor on every round is expensive**, and memoising the branch reads did
  not pay for it — the cost is more plausibly the extra `NewIncrementalAttacher` calls over the
  past cone. Unproven, and worth measuring before redesigning.
- **A silent no-op still compiles and still passes the tests.** `d6319056` shipped because a
  scripted string replace did not match and only the build was checked. Patch with tools that
  fail loudly.

## The blocker

**Sequencer search changes cannot be validated with what exists locally.** The unit and
integration tests assert liveness, and passed on all three bad builds. Every signal that
mattered came from Grafana after deploying to a live network under load.

`proxima_lrb_coverage / proxima_lrb_supply` flatness is the acceptance test. It caught what
settled TPS, branches/slot, LRB lag and attachment p95 all missed. `proxima_lrb_num_seq`
(distinct sequencers in the LRB's past cone, read off the stem's `NumSeq`) makes consolidation
quality directly observable rather than inferred from fork partitions.

Until coverage/supply and CPU under load can be measured **off production**, a change to the
search is not shippable however sound the reasoning looks. Building that measurement is the
prerequisite, not the search change.

## Governing constraint for any design

The sequencer is expected to be CPU-hungry, and that cost grows with the number of sequencers.
**CPU is not the limiting resource — the deadline is.** Each target has a bounded build budget,
so what matters is delivering a reasonable proposal *as early as possible* and continuing to
improve it while time remains. The existing round already has the right shape — post
`skeleton_0` at once, then improve — and it must be preserved.

That is the standing argument for several cheap heuristics over one thorough one, and it
survives the three reverts: none of them failed because parallel search is wrong in principle.
`ad0654fa` failed on cost and on evaluating the re-anchor unconditionally; `d6319056` never ran
the second heuristic at all; `20af2f51` failed on an undamped coverage comparison.

## Explicitly unaffected

- **`pendingSubmit` / `awaiting` and the pulse gate.** Every submitted transaction simply
  becomes part of the search space.
- **Orphaned tag-alongs on revert.** Not a loss to be scored against; they are re-added to the
  reverted state. That is how the conflict resolves.

## Baselines any change must beat

| quantity | baseline |
|----------|----------|
| slots forked | 15.1 % |
| one-against-four share | 52.7 % |
| coverage/supply ratio | flat 1.960, no excursions |
| CPU per sequencer | 0.36–0.72 cores (expected to rise; not the constraint) |
| attachment p95 | ≤ 33 ms |
| time to first usable skeleton | must not regress — the deadline is the real limit |

Collector and analysis: `.internal/fork_watch.sh`, `fork_probe.py`, `fork_analyze.py`.
Pre-change baseline `.internal/fork_watch_before.log`; the regressed window is kept in
`.internal/fork_watch_REGRESSED_20af2f51.log` for contrast.

## Genuinely open

- **Where the CPU actually goes** in a broader search: trie reads or attacher construction.
- **What damps the choice**, given that coverage alone cannot. `numSeq` is an integer over 1..N
  and immune to the 0.001 % coverage noise, which is why it was tried; it went out with
  `d6319056` before it could be judged on its own.
- **Effect on fork rate** of any broader search. A measurement, not an argument.
- **The ~5 % of slots that refuse a branch** on the 7/12 health gate, upstream cause `no
  proposals`, and the branchless-slot dead zone that follows. Related but not the same problem.
