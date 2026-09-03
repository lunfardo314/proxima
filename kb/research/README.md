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
| `forced_delegation.md` | Forcing idle UTXOs into delegation | Draft only. Written to **map the ledger invariants it would break** rather than to propose shipping it — read it for the invariants, not the feature |
| `state_scan_paging.md` | How to read more of the ledger state than one API call can return: a resumable cursor over a controller's UTXOs, an IDs-only listing with paged fetch, and a pinned-snapshot session | **Nothing here exists** — every state-read endpoint is still single-shot and capped. Not on anyone's critical path: its first consumer, [`compact.md`](../compact.md), is specified to work without it and merely to get faster if it arrives |
| `delegation_scalability.md` | How many delegations can the ledger carry, given that each is permanent state, and does a fixed freeze grid bound the coverage dips | **Partly built** — the §8/§9 grid shipped 2026-08-15 and went live at the 2026-09-03 reset. What is unbuilt is the evidence: the load model is arithmetic on code constants and **not one figure has been observed on a running network**. §10 lists the measurements to take |

## Before acting on any of these

Verify against `develop` first. These documents were written at a point in time
and the code has moved; a proposal may already be moot, or the numbers it rests
on may have changed. Two specific traps: `tick_duration.md` reasons from the
80 ms tick as the *current* value, and several documents across this knowledge
base have been found describing themselves wrongly — trust the code over any
status line, including the ones in the table above.
