# Archive — shipped

Specs and plans for work that is now in `develop`. **The code is the truth.**
These documents say what was intended and why alternatives were rejected; where
they disagree with the code, the code wins.

They are worth opening for one reason: the *why*. A spec records the option that
was rejected and the reason, which the code cannot. That is also the trap — a
spec describes the design at the moment it was written, and several features
here have been changed or superseded since.

The "proves it" column names something on `develop` that exists because of the
document. A document's own status line is not evidence: three entries here call
themselves unimplemented and are wrong about it, and one that called itself
shipped turned out to be deferred and now sits in `superseded/`.

| File | Date | What it introduced | Proves it |
|------|------|--------------------|-----------|
| `async-sequencer-plan.md` | 2026-03-27 | Async milestone submission, replacing the step-based synchronous flow. | `sequencer/strategy_async.go` |
| `attachment_cost.md` | 2026-07-01 | Attachment cost and budget, bounding unbounded past-cone chains as an attack vector. | `tests/attach_cost_test.go`, `tests/sequencer_attach_cost_test.go` |
| `bootstrap_transactions.md` | 2026-07-31 | Per-proposal bootstrap mode with an explicit baseline, and the `health_relief` window replacing a boolean flag. The *adaptive* threshold was rejected — that reasoning is the point of §4. | `57000760`; `tests/bootstrap_transaction_test.go`; cited from `global/global.go` |
| `chain_constraint.md` | 2026-02-07 | The chain constraint: UTXO chains preserving a stable `chainID` across transitions. | `ledger/tests/claude_chain_test.go` |
| `chain_constraint2.md` | 2026-02-24 | One-page addendum reshaping the `chain` constraint's arguments. | `ledger/tests/claude_chain_constraint2_test.go` |
| `chain_explorer.md` | 2026-07-14 | Chain explorer: `/list` + `/utxo` over chained accounts. | `api/chain_explorer/` |
| `chainid32to24.md` | 2026-06-01 | ChainID narrowed from 32 to 24 bytes. | `ledger/base/id.go` — `ChainIDLength = 24`. **Its header still says "analysis only (not implemented)"; that is wrong.** |
| `dag_visualizer.md` | 2026-03-08 | A browser DAG visualizer over the streaming API, to show cooperative consensus as it happens. | `api/dagviz/`, `api/streaming/dag_vertex_server.go`, `api/dag_explorer/`, and `proxi db txstore dag_explorer` |
| `deferred_commit.md` | 2026-05-06 | Deferred branch commit, with a forced path to bypass the delay. | `ForceCommitBranch` in `core/workflow/access.go`, used by `forward_sync` |
| `delegate_lock.md` | 2026-03-07 | `delegateLock`'s master argument changed from accountable bytecode to a raw holder ID. | `ledger/def/lock_delegate.easyfl`. Much evolved since — see `claude/delegation_scalability.md`. |
| `delegation_add_tokens.md` | 2026-08-16 | Top-up: adding tokens to a live delegation without unwinding it. | `proxi/node_cmd/delegate/topup.go`, `f9eaa187`. **Header says "spec, not implemented"; it shipped the day after it was written.** |
| `delegation_allowance.md` | 2026-08-24 | Delegator-signed allowance so askstop compensation comes out of the delegation balance, not the delegator's own tokens. Hardfork. | `ledger/def/ensure*.easyfl`, `sequencer/txbuilder_seq/req_askstop.go`, `ledger/tests/delegate_test.go` |
| `delegation_epoch_params.md` | 2026-08-24 | Per-target-chain delegation epoch parameters; `delegateLockState` pinned last. | Shipped 2026-05-17 on develop08; cited from six files incl. `ledger/def/lock_delegate.easyfl` |
| `dex_orders.md` | 2026-05-16 | `sellOrder` / `buyOrder` locks for trustless DEX orders. | `ledger/lock_dex_orders.go`, `ledger/def/lock_dex_orders.easyfl`, `ledger/tests/dex_orders_test.go`, `examples/dex/`. **No status header at all — it reads like a proposal and is not one.** |
| `easyfl.md` | 2026-05-07 | Proxima-specific EasyFL internals: the three-stage library build, the topological sort that protects embedded-function resolution, pooling, and the evaluation context. | Rewritten as `ledger/def/easyfl.md` (2026-08-24). Its `IntroduceUpdateYAMLMulti` is stale — the library loads JSON now. |
| `endorsement.md` | 2026-02-07 | Endorsement validation rules for sequencer transactions. | Enforced in the ledger; endorsements are core to the tangle |
| `frozen_coverage.md` | 2026-05-28 | Stem `frozen_coverage` corrected to a cumulative state total, derived as a delta with no new arguments. | Live in the stem aggregates. **Superseded 2026-08-18** by the frozen-coverage *bound* at amounts index 2. |
| `get_outputs.md` | 2026-05-06 | `get_outputs` as the single state-query primitive; all legacy account-query handlers retired. | `api/server/server.go`, `api/client/client.go`, `api/api.go` |
| `hands_on_proxi_script.md` | 2026-06-10 | Repeatable manual smoke test of `proxi` against a standalone node. | Absorbed into `tests/README.md` (2026-08-24). |
| `has_tx_refactor.md` | 2026-03-11 | The trie's transaction record carries a set of unspent output indices instead of an unused value, so UTXO and transaction pruning cannot diverge. | `ledger/multistate/mutate.go` — `set256.Set256`, `InsertAddTxMutation`, `updateTxUnspentSet`. **No status header.** |
| `library_upgrade.md` | 2026-06-03 | Ledger library upgrade mechanism, phases 1–16. | `ledger/upgrade.md` is the resulting reference doc |
| `limits.md` | 2026-02-07 | Size and count limits, and the gap analysis that produced the parse-level caps. | Rewritten as `ledger/limits.md` (2026-08-24). Its fourth constant, `MaxOtherDataSize`, is gone with `TxOtherData`. |
| `local_3node_testnet.md` | 2026-07-03 | Localhost 3-node network for sync, restart and snapshot edge cases. | Absorbed into `tests/README.md`. Near-duplicate of `local_testnet_runbook.md`. |
| `local_testnet_edge_cases.md` | 2026-07-03 | Peering and onboarding gotchas on a localhost network. | Absorbed into `tests/README.md` (the "Edge cases worth knowing" section). |
| `local_testnet_runbook.md` | 2026-06-19 | Bring-up procedure for the 3-node laptop network. | Absorbed into `tests/README.md`. Bound to one machine's paths; the rewrite is generalized. |
| `local_script.md` | 2026-05-10 | `redeemScript` / `callRedeemer`: design and as-built reference for local scripts. | Shipped — `ledger/local_script_builtins.go`, `examples/dex/`, `examples/chess_poc/`. Rewritten for users as `txdocs/redeemer_scripts.md` on the docs site (2026-08-24). |
| `metadata-refactor.md` | 2026-05-06 | Global ledger values moved onto the stem constraint; persistent `TxMetadata` removed. | `ledger/multistate/state.go` |
| `mining-bias.md` | 2026-08-15 | Winner-take-all bias on the mine chain: diagnosis, and a `constMineMaxPace` candidate fix. | Diagnosis stands; the candidate fix was never needed — both halves of the causal chain were closed by other work. Feeds `participate/mine.md` on the docs site. |
| `mining_tx_streaming.md` | 2026-08-15 | Node-side mining transaction stream, so a competitor's win reaches miners in a gossip hop rather than a confirmation. | `api/streaming/mining_tx_server.go` with tests. Feeds `participate/mine.md`. |
| `native_token.md` | 2026-08-24 | Native tokens and foundries: `foundry(supply)`, `token(...)`, `tokenAmount(tag, amount)`, mint/burn conservation. | `ledger/foundry.go`, `ledger/native_token.go`, `ledger/tests/native_token_test.go`, `proxi node foundry`. Rewritten for users as `txdocs/native_tokens.md` (2026-08-24). |
| `network_rtt_mapping.md` | 2026-06-20 | Peer round-trip-time mapping, a distance metric, and visualization. | `api/server/netviz.go`. **Layers 1–3 shipped; the offline Monte-Carlo simulator was never built.** Companion to `claude/tick_duration.md`, which needs its `d(i,j)` graph. |
| `nolock-traversal.md` | 2026-04-13 | Lock-free past-cone traversal, resting on "Good ⇒ immutable". | `core/vertex/past_cone.go` — `findConsumersNoLock`. This is why core changes must be run under `-race`. |
| `one_node_bootstrap.md` | 2026-05-23 | Manual bootstrap scenario for a single standalone node, 563 lines. | Absorbed into `tests/README.md`. |
| `output_parsing.md` | 2026-05-06 | `OutputFromBytes` made structural-only and library-free, with opt-in validation hooks. | `ledger/output.go`. **Phase 1 only**; the rest was overtaken by the wasm refactor. |
| `proxi_txbuildercore.md` | 2026-05-20 | proxi restructured as a wasm-style wallet: no `ledger.L()` singleton in CLI commands. | `proxi/glb/wallet_submit.go`; the rule is now in CLAUDE.md |
| `return_to_sender.md` | 2026-06-10 | `returnToSender(amount)`, an additive constraint on sendWithDeadline outputs. | `ledger/return_to_sender.go`, `ledger/def/return_to_sender.easyfl`, `ledger/tests/return_to_sender_test.go` |
| `send_with_deadline_lock.md` | 2026-05-12 | `sendWithDeadline`, a lock for conditional deadlined transfers. | `ledger/lock_send_with_deadline.go`, `ledger/def/lock_send_with_deadline.easyfl`, `ledger/tests/send_with_deadline_test.go` |
| `seq-improvements.md` | 2026-05-06 | Async submission plus adaptive timing, to cut sequencer transactions per unit of throughput. | `sequencer/strategy_async.go`. **Phases 1 and 2 only**; later phases were overtaken. |
| `seq_key.md` | 2026-02-08 | The sequencer's ED25519 key was plaintext hex in `proxima.yaml` at mode 0666. Moves it into a key file. | `util/keystore/`, `proxi/glb/keyfile.go`. The stricter follow-on — banning plain keys in YAML entirely — was not adopted; see `../superseded/key_management.md`. |
| `seq_v080_pbc_removal.md` | 2026-04-30 | v0.8.0 sequencer pace and permanent PBC encoding, landing `seq-improvements.md`'s Phase L in the ledger without changing observable behaviour. | develop08. Records one table **deliberately not implemented** — don't read that as an omission. |
| `sequencer.md` | 2026-03-18 | The TSF sequencer design: the greedy-token-holder model, the constraints, and the incremental refactor that produced `sequencer/factory`. | `sequencer/factory/` (TransactionSkeletonFactory). Absorbed into `sequencer/README.md` (2026-08-24); still extended by `claude/sequencer_conflict_resolution.md`. |
| `snapshot_optimize.md` | 2026-03-23 | Load shedding during snapshot generation, so snapshots stop competing with transaction processing. | Phase 1 implemented |
| `stem_data_refactor.md` | 2026-06-01 | `stemLock` split into a constrained 6-arg lock plus an unconstrained `StemData` tuple. Hardfork, no backward compatibility. | `1aade5a0` |
| `sync_startup.md` | 2026-07-03 | Auto-download a snapshot from trusted sources on startup instead of wedging on a missing or corrupt DB. | `CheckAndRestoreOnStartup` in `node/node.go`, `core/core_modules/snapshot_restore/`. Its own 2026-06-15 note records the change since: the source list is the shared top-level `sources`, not `sync.sources`, and forward sync activates on whether `sources` is populated. |
| `tag_along.md` | 2026-08-24 | `tagAlongLock` stores the sender as a raw holder ID instead of full sigLock bytecode. | Live in the ledger; the same change was then applied to `delegateLock` |
| `target_info.md` | 2026-03-06 | `proxi node delegate target_info <sequencer ID>` — everything a delegator needs to judge a target, in one command. | `proxi/node_cmd/delegate/target_info.go`, `/get_sequencer_target_info` |
| `tx_test.md` | 2026-03-07 | An independent second pass of ledger and transaction validation tests, written without reference to the existing suite. | `ledger/tests/` — `claude_chain_test.go`, `claude_index_bounds_test.go` and siblings. Ends "all topics completed". |
| `txflow2.md` | 2026-03-29 | Transaction flow restructured into separate pipeline modules. | `core/core_modules/` — `txinput_queue`, `txsolicit_queue`, `txstore_writer`, `pull_tx_server`, … **No status header.** |
| `txid_ttl_tiered.md` | 2026-06-28 | Tiered txid-state retention with a sync horizon decoupled from it. Hardfork. | Cited from eleven files across `ledger/multistate`, `core/attacher`, `core/core_modules`; `tests/txid_ttl_tiered_test.go` |
| `txstore_audit.md` | 2026-04-29 | `proxi db txstore audit <slot>` — walk the past cone of every branch in a slot back as far as the local txstore allows, and report gaps. | `proxi/db_cmd/txstore/audit.go` |
| `wallet_eval_api.md` | 2026-05-20 | `/eval` and `/ledger_constants`, letting an external wallet work without the ledger singleton. | `api/server/server.go`, `ledger/txbuildercore/constants.go` |
| `wasm_easyfl.md` | 2026-05-20 | EasyFL made WASM-compatible. | `ledger/txbuildercore/wasm/` |
| `wasm_txbuilder.md` | 2026-06-02 | `ledger/txbuildercore` builds under TinyGo — 1.3 MB, 429 KB gzipped. Phases 0–6. | `ledger/txbuildercore/wasm/main.go`, `README.md` |
| `wasm_txbuilder_helpers.md` | 2026-05-20 | 16 compose helpers across five files, adding no new wasm transitive imports. | `ledger/txbuildercore/helpers_*.go` |

Five of the entries above — `hands_on_proxi_script.md`, `local_3node_testnet.md`,
`local_testnet_edge_cases.md`, `local_testnet_runbook.md`, `one_node_bootstrap.md` — are
the five overlapping local-network runbooks that `tests/README.md` replaced. They are kept
as the working record; the commands in them are stale (`proxi init node`, `init wallet`,
`node transfer` and the `--ignore-freeze-bound` flag no longer exist). Follow
`tests/README.md`, not these.

## Read these with the date in mind

Four entries describe designs that have since moved on, and reading them as
current would mislead:

- **`frozen_coverage.md`** — the cumulative stem total it introduced was
  replaced by a frozen-coverage *bound* at amounts index 2 (2026-08-18).
- **`delegate_lock.md`** and **`delegation_epoch_params.md`** — delegation has
  been de-parametrized since; `claude/delegation_scalability.md` is current.
- **`output_parsing.md`** — only its Phase 1 is real; the remainder was
  absorbed by the wasm refactor.

And three entries have status headers that are simply wrong — `chainid32to24.md`
and `delegation_add_tokens.md` deny being implemented, and `dex_orders.md` has
no header at all despite having locks, an EasyFL definition, tests and a worked
example on `develop`. Trust the "proves it" column, not the header.
