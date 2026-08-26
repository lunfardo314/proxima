# Research — investigated, not implemented

Ideas and specs that were worked out properly and then **not built**. Nothing in
here describes the running system.

That is the whole point of the bucket. A design note sitting beside live
documents gets read as a description of how things work; four of these were
mistaken for that at various times. Here, the default assumption is inverted:
**if it is in this directory, the code does not do it.**

## How this differs from the archive

| Directory | Holds |
|-----------|-------|
| `claude/` | Live documents describing what is running, plus the two hard constraints |
| `claude/research/` | Investigated, still open. A decision could still go either way |
| `claude/archive/superseded/` | Investigated and **closed** — overtaken, shelved, or rejected. Nothing there is a live question |

The line between this and `archive/superseded/` is whether the question is still
open. When one of these is decided — built, or definitively dropped — it leaves:
to `claude/` if it ships, to `archive/shipped/` once the code is the truth, or
to `archive/superseded/` if it is rejected.

## What is here

| Document | The question | Why it is not implemented |
|----------|--------------|---------------------------|
| `tick_duration.md` | Should a tick be 80, 100 or 120 ms? A consolidation model bounded by latency, not computation | Genuinely open. Changing it is a **fresh-genesis event** and silently rescales annual inflation unless the constants are re-derived, so it is not a tuning knob. The conceptual half is on the docs site under `overview/consensus.md` |
| `branch_fork_convergence.md` | Why sibling branches of one slot split over which parent stem they consume, and ordering Phase-2 baselines by a network-wide key to stop it | Proposal only. **The measurement is complete and worth reading**: 15.1% of slots fork, always 2-way, 73% resolving within one slot. That is the case for and against acting |
| `credit_tokens.md` | Signed credit amounts to securitize frozen delegated capital | Undecided, leaning against. Leveraged coverage is the open objection and it is not answered |
| `forced_delegation.md` | Forcing idle UTXOs into delegation | Draft only. Written to **map the ledger invariants it would break** rather than to propose shipping it — read it for the invariants, not the feature |

## Before acting on any of these

Verify against `develop` first. These documents were written at a point in time
and the code has moved; a proposal may already be moot, or the numbers it rests
on may have changed. Two specific traps: `tick_duration.md` reasons from the
80 ms tick as the *current* value, and several documents across this knowledge
base have been found describing themselves wrongly — trust the code over any
status line, including the ones in the table above.
