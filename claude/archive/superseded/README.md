# Archive — superseded

Designs that were overtaken, shelved, or never taken up. **Nothing here is a
plan.** Read a document from this bucket only to find out why an approach was
*not* taken; several were written far enough to look implementable, which is
exactly the risk.

The useful column is the last one: what replaced it. A superseded design with no
named successor is a design nobody has revisited.

| File | Date | What it proposed | What replaced it |
|------|------|------------------|------------------|
| `adaptive_rate_control.md` | 2026-04-01 | Graded rather than binary rate-control gates, driven by a stress signal, with staged drops of unsolicited sequencer non-branches. | Rate control was rewritten; see the note below on the four drafts. |
| `big_tasks.md` | 2026-02-07 | A top-level task checklist, part struck through as done. | Overtaken by the `claude/TODO.md` backlog, itself deleted as stale in 2026-08. Kept only as a record of what the roadmap looked like in February. |
| `branch_cost.md` | 2026-05-25 | Make issuing a branch cost something, by requiring the branch inflation bonus to be mined. | **Rejected by its own analysis**: mining raises an honest sequencer's cost but does not stop a malicious one issuing zero-work branches it knows will lose, since every node still pays full validation, attachment and past-cone solidification first. `develop` derives the bonus from a VRF instead (`ledger/def/inflation.easyfl`). Read before proposing anything in this direction again. |
| `hands_on_plan.md` | 2026-01-18 | Hands-on test plan for single-node operation after the library-upgrade refactor. | Abandoned part-way — items 14.4 and 14.5 are still unticked. The oldest document in the knowledge base. |
| `key_management.md` | 2026-02-08 | Uniform key management: `.key` files only, plain keys forbidden in YAML, passphrase prompt for encrypted keys. | Substance landed via `../shipped/seq_key.md` and `util/keystore`. Its stricter requirements — banning plain keys outright — were not adopted, and it notes the consequence: a systemd-run node cannot use an encrypted key, because there is no stdin. |
| `native_token_tag_32vs20.md` | 2026-05-28 | Narrowing native-token tags from 32 bytes to a 20-byte prefix. | **Shelved deliberately — 32-byte tags stay.** The analysis is retained so the question is not re-litigated; revisit only on concrete signal that a high-volume native token is landing. |
| `output_kind_index.md` | 2026-05-31 | An in-trie output-kind index for enumerating outputs by kind. | **Deferred 2026-05-31** in favour of an external or in-node async index, eventually consistent and post-filtered. The in-trie design is retained as the record of what it would look like should scale force it. One piece survived and is live: the cap on state traversal for the in-node fallback (`ledger/multistate/sugared.go`). |
| `rate_control_non_seq.md` | 2026-03-23 | Filtering non-sequencer transactions that target other sequencers, to keep them out of the local attacher. | Superseded with the rest of the rate-control drafts. |
| `ratecontrol.md` | 2026-03-19 | The first rate- and congestion-control design, written against `crash.md`. | Superseded — see the note below. |
| `ratecontrol2.md` | 2026-04-07 | memDAG pruning/GC architecture, gated on whether a vertex is referenced by the sequencer. | Superseded. Pruning is now governed by `claude/dag_semantics.md` §2.5, whose criterion is "rooted and not consumed by a not-rooted transaction" — explicitly *not* a TTL or reference test. |
| `scenario_delegation_freeze.md` | 2026-06-19 | A local-testnet scenario validating freeze-epoch distribution across the reachable window. | Overtaken by the fixed freeze grid (epoch grid 600, freeze depth 60 as ledger constants). The FAIL signature it records — all delegations collapsing onto one epoch — is the useful part. |
| `sync.md` | 2026-08-04 | "Sequential sync mode": an `IsSyncing` workflow flag, transaction filtering while syncing, and a slots-behind trigger threshold. | **Replaced.** `core_modules/forward_sync` has no slots-behind threshold and no reach cap of its own: it runs exactly while a sync target is pending and hands off to recursive sync. See the package comment in `sync.go` and `core/core_modules/forward_sync/sync.md`. |
| `unitrie_double_booking_proposal.md` | 2026-03-14 | Refactoring `NewMutationsMustNoDoubleBooking` in `unitrie`, where two trie keys holding identical large values panic on commit. | Not taken up. Targets the `unitrie` dependency, not this repository, so nothing here supersedes it — it is simply unactioned. |
| `peering_refactor.md` | 2026-08-26 | Pre-refactor assessment of `peering/`: architecture, findings, incremental plan. | Superseded by the removal of the heartbeat protocol — its §1.1/§1.4 describe a package that no longer exists. Kept for why per-message outbound streams were rejected (~1 RTT of negotiation per message). |

## The four rate-control drafts

`ratecontrol.md`, `ratecontrol2.md`, `rate_control_non_seq.md` and
`adaptive_rate_control.md` were written between March and April and **were never
reconciled with each other**. They overlap, disagree in places, and none of them
describes what the node does today. Do not read them as four parts of one
design.

What survives from that period is the standing rule that rate control and
load-shedding drops belong to the sync path and must not be deleted when
touching that area — and `claude/dag_semantics.md`'s M1: pruning and GC are
cache policy and must be invisible to protocol-derived values.
