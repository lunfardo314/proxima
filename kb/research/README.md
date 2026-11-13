# Research — investigated, not implemented

Ideas and specs that were worked out properly and then **not built**. Nothing in
here describes the running system.

That is the whole point of the bucket. A design note sitting beside live
documents gets read as a description of how things work, and several of these
have been mistaken for that. Here, the default assumption is inverted:
**if it is in this directory, the code does not do it.**

One partial exception, flagged in its own header and in the table below:
`delegation_scalability.md`, whose freeze-grid *mechanism* shipped while the
load model it argues from remains measured against nothing. It sits here for
the model, not the mechanism.

## How this differs from the archive

| Directory | Holds |
|-----------|-------|
| `kb/` | Live documents describing what is running, plus the two hard constraints |
| `kb/research/` | Investigated, still open. A decision could still go either way |
| `kb/archive/superseded/` | Investigated and **closed** — overtaken, shelved, or rejected. Nothing there is a live question |

The line between this and `archive/superseded/` is whether the question is still
open. When one of these is decided — built, or definitively dropped — it leaves:
to `kb/` if it ships, to `archive/shipped/` once the code is the truth, or
to `archive/superseded/` if it is rejected.

## What is here

| Document | The question | Why it is not implemented |
|----------|--------------|---------------------------|
| `tick_duration.md` | Should a tick be 80, 100 or 120 ms? A consolidation model bounded by latency, not computation | Genuinely open. Changing it is a **fresh-genesis event** and silently rescales annual inflation unless the constants are re-derived, so it is not a tuning knob. The conceptual half is on the docs site under `overview/consensus.md` |
| `branch_fork_convergence.md` | Why sibling branches of one slot split over which parent stem they consume, and ordering Phase-2 baselines by a network-wide key to stop it | Proposal only. **The measurement is complete and worth reading**: 15.1% of slots fork, always 2-way, 73% resolving within one slot. That is the case for and against acting |
| `credit_tokens.md` | Signed credit amounts to securitize frozen delegated capital | Undecided, leaning against. Leveraged coverage is the open objection and it is not answered |
| `state_scan_paging.md` | How to read more of the ledger state than one API call can return: a resumable cursor over a controller's UTXOs, an IDs-only listing with paged fetch, and a pinned-snapshot session | **Nothing here exists** — every state-read endpoint is still single-shot and capped. Not on anyone's critical path: its first consumer, [`compact.md`](../compact.md), is specified to work without it and merely to get faster if it arrives |
| `scaling_1000tps.md` | Is 1000 TPS reachable, and what binds first? No ledger constant forbids it; the architectural term is per-transaction past-cone work, the rest is engineering (memdag GC under the global lock, branch commit volume, unbounded caches) | Static assessment of 2026-09-03, every number derived from the 50 TPS baseline, none measured. None of its blockers has been worked on; its own advice is to reproduce 200-300 TPS first |
| `covenant_sketches.md` | A precise meaning for "covenant" built around ledger invariants: L1 covenants as constraints on L2 entities, tag-along as authenticated asynchronous messaging, keyless (decentralized) assets, composition rules, and a high-level language that is also the specification | Discussion of 2026-10-17, nothing built. First step proposed: specify the existing tag-along, delegation and sequencing covenants and verify today's bytecode, no hardfork |
| `delegation_scalability.md` | How many delegations can the ledger carry, given that each is permanent state, and does a fixed freeze grid bound the coverage dips | **Partly built** — the §8/§9 grid shipped 2026-08-15 and went live at the 2026-09-03 reset. What is unbuilt is the evidence: the load model is arithmetic on code constants and **not one figure has been observed on a running network**. §10 lists the measurements to take |
| `branch-bias.md` | Who wins a slot's branch: measured 2026-10-19, competing branches tie on coverage as the whitepaper expects, but winners follow the issuer's capital (balance plus frozen delegations, added once a follower endorses the issuer's first milestone) and how early that milestone appears, not the random bonus | Analysis only. Three directions (baseline selection by branch, a coverage-definition change, or accepting it) are open for discussion |
| `split_slots.md` | Why about 0.5% of slots orphan their classical leader by more than 15 points (the watch's 1-3% is an artefact of one node's commit sample; the rate is the same as under classical ranking before slot 138000): after a lost branch race, sequencers picking up their left-out late milestones lead by classical coverage, while `v3` sequencers rank by the adjusted score and stay on the other side, which wins. Classical coverage then counts a double-backed stake on both conflicting sides (197125: 152% of supply), so 7/12 health is no majority proof; finality holds through the walk-back, liveness weakens with conflicts | Analysis of 2026-11-10/11, nothing built; accepted for now. Confirmed on five conflict splits (sides sum to 152-171% classical, about 100% adjusted); the conflict is made by sequencers whose own branch lost the race restarting from their committed output while their late milestone travels on the other side; extending it was conflict-free in most cases. Judging health by the adjusted delta is not possible without validating or storing it (D4, D7); the sequencer policy of never abandoning own history that can be extended was built, deployed and reverted on 2026-11-12: it removed the double count but let the own sequencers be lured onto losing sides in a mixed network (restarting is a firewall), so the direction is closed. The attack zone above 5/12 is fundamental |
| `early_cut_derivation.md` | Can a follower's first move on a branch cut early in the previous slot out-cover any broader extension? Derived on slot 37786 from the ledger's coverage definitions and the DAG | A derivation, not a proposal. It motivated the adjusted coverage of `../coverage_adjustment.md` (built as sequencer `v3`, not activated) |
| `mining-algo/` | Which proof of work and payout rules replace the mine chain's VRF work in library upgrade 6: `options.md` compares three, `option_a_vrf_enforced.md` (A) and `option_b_key_hash.md` (B) are the two not taken, `mining_pause.md` a stop-gap that stops mining until the change ships | **Decided 2026-11-06**: option C, now `../library_upgrade6_mining.md`, built on `feat/upgrade6-mining`. A and B are **rejected**, kept here rather than in `archive/superseded/` beside the choice they informed; the pause is not planned. Moves to the archive once upgrade 6 ships |
| `branch_bonus_work.md` | Can a branch bonus capped by the trailing zero bits of the branch's txid move the reward from sequencer count to equipment? Two variants for discussion: the work alone setting the bonus (VRF dropped, ties by best hash, sybils neutral), or work scaling the cap with the VRF kept as tie-breaker (randomness limits dominance, part of the sybil lottery stays); an equilibrium model of how many sequencers branch: grinding alone bounds nothing, a minimum work per branch as the ticket gives `n* = V / c` whatever the randomness | Discussion of 2026-11-10, nothing decided or built. Needs a library upgrade and node changes binding `dag_semantics.md` |
| `network_snapshot_2026-11-02.md` | What the take 1 network looks like over one day: activity, branch competition, sequencers, network shape, delegation and holder concentration | A measurement, not a proposal. Baseline numbers for any argument about them; repeat it rather than quote it once old |

## Before acting on any of these

Verify against `develop` first. These documents were written at a point in time
and the code has moved; a proposal may already be moot, or the numbers it rests
on may have changed. Two specific traps: `tick_duration.md` reasons from the
80 ms tick as the *current* value, and several documents across this knowledge
base have been found describing themselves wrongly — trust the code over any
status line, including the ones in the table above.
