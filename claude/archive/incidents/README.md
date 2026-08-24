# Archive — incidents

Investigation notes, each tied to one event on one date: a crash, a wedge, a
halt, a stress exercise. They record what was observed, what it turned out to
be, and what shipped.

**Nothing here constrains current work.** The semantic models
(`claude/dag_semantics.md`, `claude/sync_semantics.md`) do. These notes are
useful for one thing: when a symptom recurs, the note says whether it has been
seen before and what it was last time. Read the "still holds" column before
trusting any of it — several were written mid-investigation and their
conclusions were later overtaken.

| File | Date | What it concluded | Still holds? |
|------|------|-------------------|--------------|
| `attachment_time.md` | 2026-04-27 | Attachment latency of 250–650 ms is software contention, not hardware; 100 TPS / 8 sequencers judged plausible on the proposed split topology. | Verdict yes; the measurements are from an 8-node topology that no longer exists. |
| `bottleneck.md` | 2026-03-15 | pprof-driven bottleneck hunt. | **Never concluded** — ends at "pending Phase 1 results". Superseded by `attachment_time.md`. |
| `committed_branch_baseline_wedge.md` | 2026-07-02 | A milestone referencing a branch inside the pending-commit window could not see it: the baseline lookup read the DB only. A stopgap that dodged a long-gone cache recursion had outlived its reason. | Fixed, one line, in `core/attacher/attach.go`. |
| `conflict.md` | 2026-04-02 | A past cone spanning two forks: the losing branch is pruned before reconciliation, so the attacher cannot track fork boundaries. | Mechanism superseded — conflict detection is now stated in `dag_semantics.md` §2.7. |
| `consensus_halt_2026-04-23.md` | 2026-04-23 | Two independent incidents. The halt came from a race on `pb.Mutations` during deferred branch commit, leaving `b.pending`/`b.m` inconsistent and blocking branch proposals. | Fixed (`ff05e018`). The preserved stuck-state analysis is of no further use. |
| `coverage_delta_enforcement_fix.md` | 2026-06-11 | A sync wedge when the attach baseline is newer than the milestone. Part B (Go-side) written; the on-chain rule deliberately untouched. | **Check before trusting** — the note says Part B shipped *uncommitted*. Verify against `develop`. |
| `crash.md` | 2026-03-18 | Testnet crash under 217 spam senders; proposes txInputQueue backpressure and an attacher-goroutine cap. | Proposals only, from the oldest crash here. Rate control has been rewritten several times since. |
| `crash2.md` | 2026-04-12 | Two root causes: a branch short-circuit in `attachOutput` skipped input traversal for non-state branch dependencies (token conservation violation); and a stale `OwnLatestMilestoneOutput()` made the boot proposer merge incompatible state views. | Both fixed. Written on `develop07-seq-improvement`, a branch that no longer exists. |
| `crash3.md` | 2026-04-13 | Competing branches in a past cone. Confirms the mechanism — a baseline stored only as an ID is invisible to the consumer filter, so `CheckAndClean` drops the competing branch. | Diagnosis holds. **Four attempted fixes were reverted**; the note records why the strict check must stay. |
| `delegation_params_position_bug.md` | 2026-05-25 | `delegationParams` at a fixed tuple position was a design flaw, plus a `setup_seq` default-attach bug. | Superseded — delegation params were since de-parametrized and repositioned. |
| `fix-detach-reattach-race.md` | 2026-04-25 | A FATAL detach/reattach race; the "memory leak" investigated alongside it was the same race in disguise. Four transferable lessons, including growing-under-load vs leaking. | Fixed. The lessons are the reason to keep it. |
| `fork_detection_recovery.md` | 2026-07-03 | Design spec: a node whose committed state has diverged must recover deterministically or refuse cleanly, never wedge silently. Sequencer start gate = on-canonical-lineage AND (synced OR must-bootstrap). | Implemented, pending end-to-end validation. **Filed here because it began as an incident, but it reads as a spec** — see note below. |
| `forward_sync_lineage_nonstitch.md` | 2026-06-25 | Forward sync did not stitch lineage. Redesign shipped: lineage-exact targets, `to_branch` API, set-based targets, cap only on branches. | Shipped. Carries one durable lesson: *a vertex having the correct baseline available is not the same as the vertex being `Good`.* |
| `forward_sync_oom.md` | 2026-08-04 | Access-node OOM during forward sync. Deployment error (two nodes each configured for 6 GB on an 8 GB box) plus a real bug: the txstore write-behind buffer was invisible to `pull_tx_server`, so peers missed buffered transactions. | Both fixed. Access-node goroutine counts of 700–1000 are normal, not a leak. |
| `known_baseline_attacher.md` | 2026-06-26 | A far-behind node floods the attacher pool because every milestone independently re-solidifies its baseline direction. Fix: propagate a known-baseline floor. | Implemented, `-race` clean. **The tests do not exercise the far-behind runaway itself** — only the mechanism. |
| `stress_sequencer_shutdown.md` | 2026-08-07 | Sequencers stopped one by one until branch production ceased, then restarted. Recovery wedged: the bootstrap sequencer's past-slot explicit baseline inverted the superset assumption. | Fixed (`d73b4142`). **Two latent issues recorded and not closed** — see note below. |
| `sync_findings.md` | 2026-03-20 | First working sync: sequential branch-by-branch with pull-ahead, event-driven wake, forced commit to dodge the deferred-commit delay. | Historical. Sync has been redesigned twice since; `sync_semantics.md` is the authority. |
| `trie_iteration.md` | 2026-04-25 | `PrunableTxIDsAtSlot` dominated idle CPU (~40%) because prefix iteration was not O(matching sub-trie). Analysis plus a fix proposal. | Fix landed; `core/core_modules/branches/branches.go` still cites it. |

## Open items recorded here and not closed elsewhere

Three notes end with something unresolved. They are listed here so the archive
does not bury them:

- **`stress_sequencer_shutdown.md`** — `vid.baselineBranchID` conflates the
  baseline *floor* with the *resolved* baseline. `d73b4142` removed one source
  of a bad floor; it did not remove the overloading, and forward sync pins older
  baselines the same way. Separately, `PastCone.SetBaseline` writes the delta's
  field while `GetBaseline` prefers the outer one, so a baseline swapped by
  `MergePastCone` inside an incremental attacher is invisible until commit.
  Described there as real, latent, and not yet fixed.
- **`coverage_delta_enforcement_fix.md`** — records its own change as shipped
  but uncommitted. Verify against `develop` before relying on either the fix or
  the described behaviour.
- **`known_baseline_attacher.md`** — the fix is in and race-clean, but no test
  reproduces the runaway it exists to prevent.

`fork_detection_recovery.md` is a design spec that was implemented, not an
incident write-up; it sits in this bucket because it grew out of the fork
incident and four code comments point at it here.
