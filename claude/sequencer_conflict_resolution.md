# Sequencer conflict resolution

Status: **measured on the testnet 2026-08-12; one proposal, not implemented.** Replaces an
earlier note that framed the skeleton search itself as the open problem; the measurements below
say it is not. Extends the TSF design in [`claude/sequencer.md`](sequencer.md).

## Summary

A sequencer that has consolidated a tag-along cannot endorse a peer that consolidated a
conflicting one; to fold that peer in it must revert its own state. Whether the search can
make that move was the open question, and three attempts were made to widen it.

Driving the network with an explicit conflict spammer answers it: **the search reverts, freely
and correctly.** What conflicts actually break is narrower and was not what anyone was looking
at — endorsement breadth. Under a conflict aimed at every sequencer, no milestone can hold more
than one endorsement, because any two peers have consolidated mutually incompatible members.
Mean endorsements per milestone falls threefold and the fraction of branches folding in *every*
sequencer falls 13-fold. Consensus survives only because at least one sequencer per slot still
manages it.

That collapse is a cost of resolving the conflict, not a failure to resolve it. The sequencer
cannot avoid it by choosing tag-alongs differently — the conflict is not observable at
consumption time — and the revert during coverage optimisation is precisely the mechanism that
splits the paths and lets the coverage rule pick one.

## The search, and what it must include

Choose an own chain output to extend and a set of peer milestones to endorse, maximising the
result. Three properties define the problem:

- **Reverting own state is inside the search space.** Extending an *earlier* own output, and so
  orphaning the milestones built after it, is how a sequencer resolves a conflict. A normal
  move, not an exception path.
- **The space is exponential and changes asynchronously.** Candidates arrive and are replaced
  during the slot; every combination can conflict with every other.
- **No optimal algorithm exists.** Everything is a heuristic.

Sibling branches of a slot all spend the parent's stem, so they mutually conflict: a skeleton
seeded on lineage A can never absorb a milestone anchored to B — the attacher rejects it as a
**conflict**, not as a lower score. The landscape is disconnected basins, one per lineage, and
a hill climb cannot cross between them. The only moves that change basin are the revert and the
re-anchor through committed branch state.

That much is structural and still true. The inference drawn from it — that the single
heuristic commits to a basin early and cannot leave — is what the experiment refutes.

## The attack, and how to reproduce it

An attacker sends conflicting transactions whose tag-alongs are aimed at *different*
sequencers. Only one of those sequencers can consume a given tag-along, so the others must
revert in order to consolidate.

`multispam conflict` (in the `proxima-multispam` repo) is exactly this. Each round a sender
spends one set of inputs with N transactions, each aiming its tag-along at a different
sequencer, spaced one `TransactionPace` apart — a node drops a transaction landing within a
pace of another from the same holder *before* persisting or gossiping it, so a tighter set
would collapse to a single transaction and produce no conflict.

Verified on the DAG, slot 67154 — five transactions, identical inputs, five distinct
sequencers, ticks exactly 12 apart:

```
inputs: 67150-109-02ffa55ded04…#0 and #2

67154-0-0222674bc52c    tag-along -> oloc2
67154-12-02007d6a9463   tag-along -> oseq1
67154-24-0202614d5674   tag-along -> hloc0
67154-36-02be7549294e   tag-along -> hboot
67154-48-028c9ae9925d   tag-along -> oloc1
```

`shouldAttachNonSeq` partitions the set: a node attaches only the member carrying an output for
its own sequencer and drops the rest (access nodes drop all). Measured per node: 89/s received,
71/s dropped, 17.8/s attached — precisely one member per set.

## Experiment results

Testnet, 5 sequencers, single-factory baseline `1b782130`, 2026-08-12.

### The search reverts, heavily

oseq1's own chain through slot 67154, read off `predInputIdx` (input `#0`):

```
tick  14   chain-pred s67153-27   endorses oloc1@0    15 tag-alongs
tick  28   chain-pred s67153-27   endorses oloc2@14   15 tag-alongs   orphans own tick-14
tick  41   chain-pred s67153-27   endorses hboot@28   15 tag-alongs   orphans own tick-28
tick  55   chain-pred s67153-27   endorses oloc1@40   15 tag-alongs   orphans own tick-41
tick  70   chain-pred s67154-41   endorses oloc1@54   15 tag-alongs   reverts, orphans tick-55
tick  86   chain-pred s67154-70   endorses none
tick 101   chain-pred s67154-86   endorses none
```

Four rebuilds from the same slot-67153 base, each discarding the previous own milestone to
endorse a fresher peer, then a revert to an earlier own milestone. Five of seven milestones
discarded own recent work.

The metric agrees independently: 3.8/s milestones issued network-wide against 1.32/s in the
winning branch — **65% of all sequencer milestones orphaned**. Basin-sticking did not occur.

### Consolidation fragments 13x, and it is the conflicts, not the load

| regime | non-seq/s | confirmed/s | branches numSeq=5 | deficient |
|---|---|---|---|---|
| idle (slots 67398-67416) | 0 | — | 92.6 % | 7.4 % |
| `run` 400 senders x batch 3 | 68 | 29 | 97.4 % | 2.6 % |
| `conflict` 400 senders x fanout 5 | 89 | 16.5 | 65 % | **35 %** |

`run` submits at a comparable rate, settles nearly twice as many transactions, and leaves 2.6%
of branches deficient. The cause is the conflict shape, not throughput.

The mechanism is the predicted one: if A has consolidated its own conflict member and B a rival
member, A cannot endorse B. Aiming a member at every sequencer guarantees that some pairs are
incompatible at branch time, so a branch folds in only the compatible ones.

### Endorsement breadth collapses — this is the mechanism

Endorsements per milestone, rate per second per bucket:

| endorsements | `run` 400 x batch 3 | `conflict` 400 x fanout 5 |
|---|---|---|
| 0 | 0.144 | 0.309 |
| 1 | 0.200 | **0.449** |
| 2 | **0.316** | 0.000 |
| 3 | 0.137 | 0.000 |
| 4 | 0.067 | 0.000 |
| 5+ | 0.000 | 0.000 |

Under ordinary load buckets 2–4 carry ~60% of milestones. Under a 5-wide conflict, **two
endorsements become impossible** — zero on all five nodes, not merely rarer. A milestone holds
at most one.

The reason is a counting argument. Endorsing peer A commits the skeleton to A's past cone,
which contains the conflict member aimed at A. Adding peer B requires B's cone to reconcile,
but B consolidated a *rival* member of the same set, so `InsertEndorsement` returns a conflict
and `improvementLoop` stalls. With a member aimed at every sequencer, any two peers are
mutually incompatible.

It is dose-dependent on the width of the conflict, which is what the argument predicts. Mean
endorsements per milestone, tracked across the regime changes (and recovering just as sharply
when the spammer stops):

```
idle                        1.57 – 1.63
conflict fanout 2, 100 sndr 1.08 – 1.17
conflict fanout 5, 100 sndr 0.71 – 0.93
conflict fanout 5, 400 sndr 0.51 – 0.63
run 400 x batch 3           1.52 – 1.73
```

So the full chain is:

> conflicting tag-alongs aimed at different sequencers → peers consolidate rival members →
> `InsertEndorsement` conflicts → `improvementLoop` stalls at 0–1 endorsements → the branch
> folds in fewer sequencers → `numSeq < 5` → coverage ratio below the slot maximum

Endorsement breadth is the direct observable; `numSeq` and the coverage deficit are downstream
of it.

### Consensus held — on a margin, not by immunity

Coverage/supply stayed at 1.960 and `num_seq` at 5 for 25 minutes at 400 x 5; zero breaches
(worst coverage/supply 1.9597, max LRB lag 2). But that is not immunity:

> **In every conflict-load slot at least one sequencer still reached numSeq=5, and the LRB
> always selected one of those. The failure threshold is the slot where none does.**

The resilience metric is therefore *the fraction of slots with at least one numSeq=N branch*,
which was 100% throughout. That margin — not coverage/supply — is what a stronger attack has
to exhaust, and what any future test should aim at.

### numSeq determines branch coverage exactly

Sibling branches, ratio of `coverageDelta` to the slot maximum:

| numSeq | n | mean ratio | range |
|---|---|---|---|
| 5 | 123 | **1.000** | 1.000 – 1.000 |
| 4 | 9 | 0.84 | 0.807 – 0.867 |
| 3 | 17 | 0.66 | 0.615 – 0.674 |

Every branch that folded in all five sat at the slot maximum, with **zero variance** across 123
branches from both idle and loaded windows. A branch's coverage deficit is entirely explained
by how many sequencers it missed.

### Transaction accounting under conflict load

Per node, 400 senders x fanout 5:

```
into input queue      840/s      dedup hits 747/s   ->  unique 93/s
non-seq received       89/s   +  seq 3.8/s          =         93/s   (reconciles)
non-seq attached       17.8/s     (one member per set)
non-seq validated      46.1/s     (rest pulled in via peers' past cones)
non-seq confirmed      15.6/s     = 17.4 % of submitted (theoretical max 20 %)
```

So ~2.6 of every 5 members are consolidated by *someone* before being discarded, 87% of sets
yield a surviving member, and 13% are wiped out entirely. Gossip amplification ~9x.

The winning branch carries ~174 tx/slot against ~24 at idle: the state grows normally. What is
abnormal is the ratio — the node does roughly 5x the validation work per unit of state growth,
and the sequencer discards two thirds of the milestones it builds.

### Forks are a separate problem: equal-coverage ties

Slot 67059 forked 3-2 over two slot-67058 parents:

```
                    coverageDelta            branch bonus   numSeq
hloc0 01317a1116f2  104,177,524,492,982       3,994,255       5     <- chosen by oloc1, oloc2
oloc1 017069511a2d  104,177,524,492,982       3,220,699       5     <- chosen by hboot, hloc0, oseq1
oloc2 01dce8a62b34  104,176,444,508,617           —           5
hboot 0147921ef5ea   90,306,820,129,637           —           4
oseq1 014faf7e110f   70,239,014,225,945           —           3
```

Both candidates consumed the same slot-67057 stem and their coverage is **bit-identical** — not
"within 0.001%", the same integer. No coverage rule can break this tie. The higher-bonus branch
lost, and hloc0 and oloc1 each abandoned their own branch for the other's. Resolved in one slot.

The weak branches (numSeq 4 and 3) were simply ignored; nobody built on them. **Weak branches
and forks are different phenomena** — a branch gate would not have prevented this fork.

## Proposal: defer branches deficient in numSeq

Aimed at the 35%, not at the forks.

**Rule.** When a sequencer is about to submit a branch whose `numSeq` is below the number of
sequencers it has recently seen branch, it does not submit immediately. It holds the branch
for a short window (~1 s). If a better branch arrives and can serve as a baseline meanwhile,
it drops its own; otherwise it posts it unchanged.

**Why `numSeq` and not coverage.** Coverage is the wrong variable: it is noisy at the margin,
needs renormalising as supply grows, and "10% below a recent maximum" is a moving target.
`numSeq` is an integer over 1..N, exact, identical on every node, already committed on the stem
and already exposed as `proxima_lrb_num_seq`. The table above shows it identifies deficient
branches perfectly. It is also the non-coverage damping the record demands: ranking on raw
coverage differences was tried in `20af2f51` and oscillated the whole network's coverage,
because sibling coverages are equalised by design and choosing on that difference is choosing
on noise.

**Why deferral is fail-safe.** Posting anyway when nothing better arrives means the rule can
never manufacture a branchless slot — which matters, because a branchless slot is worse than a
weak branch and the dead zone after one is a known pathology. When the rule does drop a branch
the outcome equals today's (the branch would have been orphaned regardless), minus the network
attaching, validating and storing it.

**Cost/benefit.** Fires on ~35% of branches under attack and ~2.6% under ordinary load: an
attack mitigation that is nearly free when not under attack.

**Open concerns.**

- It puts wall-clock timing into the branch path, which the factory otherwise avoids (it works
  in ledger time only). Precedent exists in the `strategy_async` branch latch.
- The existing health gate (`FractionHealthyBranch` 7/12 of supply) is far too loose to catch
  these: a numSeq-3 branch still sits near 130% of supply. This rule is a relative gate on top
  of an absolute one, not a replacement.
- It treats a symptom. It stops the waste; it does not make the deficient sequencer consolidate
  its peers.
- The reference count ("sequencers I have recently seen branch") needs a definition that does
  not itself become an attack surface.

## Not a lever: choosing which tag-alongs to consume

Since consuming a conflicting tag-along is what costs the sequencer its endorsements, declining
such tag-alongs looks like a root-cause fix. It is not available:

- **The information does not exist at consumption time.** When a sequencer takes a tag-along
  from its backlog, no peer has necessarily consumed the rival member yet, and if one has, the
  milestone need not have propagated. There is nothing to test against.
- **The resolution already exists downstream.** Reverting during coverage optimisation does
  exactly this job: it splits the sequencer paths along the conflict and lets the coverage rule
  prefer one of them. That is the designed mechanism, and the measurements show it working —
  65% of milestones orphaned, the network converging on a numSeq=5 branch every slot.

The endorsement collapse is therefore a *cost* of resolving conflicts, not a defect in how they
are resolved.

## Not the problem: widening the skeleton search

Three attempts, all reverted, all predating the measurements above:

| commit | change | measured |
|--------|--------|----------|
| `20af2f51` → `d21de415` | seed takes whichever of own-head / re-anchor is heavier | coverage/supply 1.960 flat → oscillating 1.542–1.960 within a minute, network-wide; ~20% CPU, 5–7x attachment p95 |
| `d6319056` → `94c6d21f` | two heuristics — **main hunk never applied**, both factories ran the same greedy search; only the `(numSeq, coverage)` scoring took effect | coverage/supply min 1.64, num_seq min 3, CPU +30–44% |
| `ad0654fa` → `1b782130` | two heuristics correctly wired; re-anchor evaluated every round, winning by 1% | CPU 2.14 cores against 0.72, coverage/supply min 1.03, bootstrap transactions every ~40 s |

The premise behind all three — that the search commits to a basin and cannot leave — is not
supported by the oseq1 trace or by the 65% milestone orphan rate. Anything further here needs a
failure it can point at first.

Two lessons survive independent of that:

- **A silent no-op still compiles and still passes the tests.** `d6319056` shipped because a
  scripted string replace did not match and only the build was checked. Patch with tools that
  fail loudly.
- **Local tests do not distinguish a good search from a bad one.** All three passed. Every
  signal came from the live network. `proxima_lrb_coverage / proxima_lrb_supply` flatness is
  the acceptance test; `proxima_lrb_num_seq` makes consolidation quality directly observable.

## Genuinely open

- **What decides an equal-coverage tie.** Candidate arrival order, or the
  committed-before-pending ordering in `rankedUniqueBaselines`. The branch inflation bonus is
  the designed tiebreaker and lost at 67059, so it is not being consulted where it matters.
- **Where the failure threshold is.** How much conflict intensity is needed before *no*
  sequencer reaches numSeq=N in a slot. Not reached at 400 senders x fanout 5.
- **Whether the endorsement collapse is reducible at all**, given that it follows from the
  conflict structure rather than from any choice the sequencer makes. If it is not, the branch
  deferral is the only available response and the collapse is simply the price of the attack.
- **The ~5% of slots that refuse a branch** on the health gate, upstream cause `no proposals`,
  and the branchless dead zone that follows. Related but distinct.

## Method notes

- Node dagviz API: `http://<node>:8000/api/v1/dag_explorer/{slot,tx_detail,past_cone,find_tx}`.
  `tx_detail` returns **text**, not JSON. Sequencer transaction IDs are `s`-prefixed; non-seq
  are not. A branch's own aggregates are the **second** `oracleData` in its detail (the first is
  the consumed parent stem). The chain predecessor is the input at `predInputIdx`; a backward
  jump there is the revert.
- **Sample controls by explicit slot range, not by wall clock.** A window taken right after
  stopping the spammer read 31.6% deficient — contaminated by the tail. The true idle range
  measured 7.4%. The contaminated sample also produced a spurious per-node conclusion (one
  sequencer apparently deficient in two thirds of its branches; at genuine idle it is 84–100%).
- Baselines from an earlier 3.4 h / 1206-slot run under `run` load: 15.1% of slots forked, every
  fork 2-way, 52.7% one-against-four, 73% resolving within one slot. Collector
  `.internal/fork_watch.sh` with `fork_probe.py` on hboot.
