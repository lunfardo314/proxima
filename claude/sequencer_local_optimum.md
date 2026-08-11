# Sequencer skeleton search: parallel heuristics instead of one

Status: **proposal, nothing implemented.** Extends the TSF design in
[`claude/sequencer.md`](sequencer.md), which specifies a single factory. Supersedes an earlier
draft of this file; a greedy version of the idea in it was shipped and reverted the same day
(see *The failed greedy fix*).

## The search, and what it must include

Choose an own chain output to extend and a set of peer milestones to endorse, maximising the
result. Three properties define the problem:

- **Reverting own state is inside the search space.** Extending an *earlier* own output, and so
  orphaning the milestones built after it, is how a sequencer resolves a conflict. A normal move,
  not an exception path. Traversing the own past cone for extend candidates is therefore
  **required**, not an optimisation to be justified.
- **The space is exponential and changes asynchronously.** Candidates arrive and are replaced
  during the slot; every combination can conflict with every other.
- **No optimal algorithm exists.** Everything here is a heuristic; the question is which ones and
  how many.

Today there is exactly one heuristic and it commits early:

1. **Seed** — `chooseFirstExtendEndorsePair`: extend the own chain **head only**, walk endorse
   candidates coverage-descending, take the **first** that reconciles. Older own outputs are
   deliberately not tried, commented as producing only "already consumed" conflicts — the
   assumption this proposal rejects, since those conflicts *are* the revert.
2. **Climb** — `improvementLoop`: add one endorsement at a time to a single incumbent, keep the
   best, never backtrack.
3. **Score** — `FinalLedgerCoverage`, nothing else.

Sibling branches of a slot all spend the parent's stem, so they mutually conflict: a skeleton
seeded on lineage A can never absorb a milestone anchored to B — the attacher rejects it as a
**conflict**, not as a lower score. The landscape is **disconnected basins, one per lineage**,
the climb cannot cross between them, and the first-fit seed alone decides which basin the whole
slot's work happens in.

## Why this is a security matter

**Sticking to the seed chosen at the start of the slot is a bug**, not a tuning problem, and the
reason is an attack rather than throughput.

An attacker sends conflicting transactions whose tag-alongs are aimed at *different* sequencers.
Only one of those sequencers can consume a given tag-along, so the others **must revert** in
order to consolidate. A sequencer that cannot revert — or that has committed to a basin at the
start of the slot and cannot leave it — cannot resolve this. Repeated, it holds sequencers in
incompatible basins and decays consensus.

So the revert capability is not a performance nicety; it is the defence. Tag-alongs being
re-added to the reverted state afterwards is the normal, expected outcome — that is what
resolving the conflict looks like.

### Measured, today

3.4 h, 1206 slots, 200 senders: **15.1 %** of slots forked, **every fork 2-way, none 3-way**,
52.7 % one-against-four. At slot 60662 three of five sequencers spent the whole slot on a branch
with 1.05×10⁹ less coverage and a smaller past cone (22 seq + 320 non-seq against 24 + 342).

### The failed greedy fix

`20af2f51`, reverted in `d21de415`. Making the seed evaluate both extend sources and take the
heavier turned `proxima_lrb_coverage / proxima_lrb_supply` from **1.960 on every sample for 50
minutes** into oscillation between 1.542 and 1.960 within one minute of the first upgraded node
starting — on upgraded and not-yet-upgraded nodes alike. Plus ~20 % CPU and 5–7× attachment p95.

Sibling coverages differ by ~0.001 %, and pre-branch consolidation is *designed* to equalise them
so the VRF bonus decides. The objective is flattest exactly where the decision is made, so a rule
that moves on any advantage churns. **Any new rule must be damped by something that is not raw
coverage.**

## Governing constraint: time-bounded, anytime search

The sequencer is expected to be CPU-hungry, and that cost grows with the number of sequencers.
**CPU is not the limiting resource — the deadline is.** Each target has a bounded build budget,
so what matters is delivering a reasonable proposal *as early as possible* and continuing to
improve it while time remains.

This is the argument for several cheap heuristics over one thorough one: they cover different
parts of the space simultaneously and each can yield a usable skeleton immediately, rather than
one search arriving late at a better answer. The existing round already has the right shape —
post `skeleton_0` at once, then improve — and it should be preserved as heuristics are added.

## Architecture: several factories, one shared memory

Restores the pre-factory pipeline — `e1/e2/e3` with randomised `r2/r3`, deleted in `abc6e114`
(550 lines) as "superseded by factory proposer (f0)" — in the factory setting.
`CandidatesToEndorseShuffled` in `backlog` is a leftover of exactly that: defined, never called,
because the strategy that used it was the one removed.

**Several factories run concurrently, each with a different heuristic, all feeding the same
skeleton channel.**

- **Factory A — current heuristic.** Extend from the own past cone, endorse candidates
  coverage-descending. Exploits the obvious quickly.
- **Factory B — randomised.** Endorse candidates shuffled (`CandidatesToEndorseShuffled`), extend
  sampled from the own past cone. Explores, and breaks the symmetry that makes every node commit
  the same first-fit error.

**Randomisation applies to the search order only.** Selection still follows the score, so the
outcome is not randomised — only which parts of the space get examined before choosing. Adding
Factory B cannot make the choice worse than Factory A alone; it can only surface candidates A
would never have reached.

**They share one checked-combination set.** This is the cost control: a combination built by one
factory is never rebuilt by another, so N factories cost far less than N× the attacher work.
`combinationSet` is today owned by the Run goroutine and reset there on slot change, precisely to
avoid racing `isChecked`/`markChecked` — sharing it means that ownership model has to change: it
must become concurrency-safe, with a defined owner for the per-slot reset. This is the main
structural work in the change.

**Selection is by a weighted sum of coverage and newly consolidated sequencers**, replacing bare
`FinalLedgerCoverage` wherever skeletons are compared. `numSeq` comes from
`NumNewTransactionStatsInPastCone`, already available on `IncrementalAttacher` (it embeds
`*attacher`) and already committed on the stem as `NumSeq`.

The weight is the delicate part. `numSeq` is an integer over 1..N and immune to the 0.001 %
coverage noise that produced the churn; coverage is not. If the weight lets a coverage difference
outvote a whole extra sequencer, the churn returns. Start with +1 sequencer dominating any
within-slot coverage difference — effectively lexicographic `(numSeq, coverage)` — and relax only
against measurement.

## Explicitly unaffected

- **`pendingSubmit` / `awaiting` and the pulse gate.** Nothing changes. Every submitted
  transaction simply becomes part of the search space.
- **Orphaned tag-alongs on revert.** Not a loss to be scored against; they are re-added to the
  reverted state. That is how the conflict resolves.

## Genuinely open

- **Effect on fork rate.** Broader search should reduce basin-sticking, but it is a measurement,
  not an argument.
- **The weights**, per above.

## Instrument first

- **`proxima_lrb_num_seq`** — distinct sequencers in the LRB branch's past cone, straight off the
  stem's `NumSeq`. The objective being proposed should be observable before it is optimised;
  today it is inferred from fork partitions after the fact.
- **`proxima_lrb_coverage / proxima_lrb_supply` flatness is the acceptance test.** It caught the
  regression that settled TPS, branches/slot and LRB lag all missed.

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
