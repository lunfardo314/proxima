# Cold wallet in `proxi node consolidate`

> **DRAFT** — spec, not built. Branch `feat/cold-wallet`, written 2026-11-25, open questions of the first draft decided by the user the same day, `address_mode` added the same day. No ledger change, no hardfork, no node change: wallet side only (`proxi/glb`, `consolidator`, `proxi/node_cmd/consolidate`).

---

## 1. What and why

`proxi node consolidate` sweeps the wallet's account and moves what is above the configured minimum into consensus: to a sequencer, into delegations, or into one output (`kb/consolidate.md`). Everything it creates is controlled by the key that receives the payouts: the key of a miner, a sequencer controller or any other always-online process. That key is hot.

With the cold wallet, the hot wallet keeps receiving and keeps its minimum, and what the consolidator moves out ends up controlled by a second key: the owner of the cold sigLock it moves to, or the master of the delegation it creates or tops up. Losing the hot key then loses at most the hot wallet's minimum and what has arrived since the last sweep.

There are two modes, chosen by `cold_wallet.address_mode` (§3):

- **Key mode**, the default. The cold key is unlocked in the consolidator's process for as long as it runs. A move into a delegation or to a sequencer is two transactions, the second signed by the cold key.
- **Address mode.** Only the cold wallet's address is needed. Every move is one hot transaction, and the cold key can stay off the machine.

## 2. Configuration

One wallet profile `proxi.yaml` for both keys. A new top-level section, next to `wallet`:

```yaml
wallet:
  key_file: wallet.key
  holder_id: <hex>
  sequencer_id: <hex>
cold_wallet:
  holder_id: <hex>
  key_file: cold.key     # required in key mode; optional in address mode
  address_mode: false    # default false
```

- **Absent or empty `cold_wallet`: no-op.** Every command behaves as today, and `--target-cold` refuses to start.
- `holder_id` is required. It is the cold account, and it must differ from the hot wallet's holder ID.
- `key_file` is a keystore file, as `wallet.key_file`. It is required in key mode and optional in address mode. When it is set, the holder ID derived from it must equal `holder_id`, as for `wallet.holder_id` (`glb.GetWalletData`).
- `address_mode`, default `false`, selects the mode (§3).
- Only `proxi node consolidate --target-cold` ever unlocks the cold key, and only when `key_file` is set. Every other command that needs the cold account uses `holder_id`.
- **Passphrase.** The keystore's own passphrase file (named after its holder ID, `Keystore.ReadPassphraseFile`) is read as for the hot key; otherwise `proxi` prompts. `PROXIMA_KEY_PASSPHRASE` is **not** read for the cold key: it is the hot key's passphrase. Unlocking the cold key must not overwrite `glb.KeyPassphrase()`, which hands the hot key's passphrase to a spawned nonce seeker. So `LoadPrivateKeyFromFile` gets an option, or a sibling, that skips the environment variable and the global.

## 3. The command and its two modes

`proxi node consolidate --target-cold`. All other flags and the `consolidate` section of the profile mean what they mean today. The trigger, the threshold, the minimum and the split rules are unchanged: only where the moved part lands, and the shape of each move, change.

| Consolidation target | Key mode (`address_mode: false`) | Address mode (`address_mode: true`) |
|---|---|---|
| cold sigLock (`--autodelegate none`, no `--send-to-sequencer`) | one hot transaction (§4) | one hot transaction (§4) |
| new delegation, top-up | two transactions (§5) | one hot transaction (§4) |
| `--send-to-sequencer` | two transactions (§5) | one hot transaction (§4) |

**Why key mode is the default.** In key mode the start unlocks the cold key and checks it against `holder_id`, so `proxi` knows the cold account can be spent from. In address mode `proxi` cannot check that the cold key exists at all. A mistyped `holder_id`, or a key file lost or never made, sends everything the consolidator moves to an account nobody can spend from. Address mode logs a warning at start saying so.

**Start-up checks**, each failure refusing the start with a message naming the fix:

1. `cold_wallet.holder_id` is configured and differs from the hot holder ID.
2. Key mode: `cold_wallet.key_file` is configured. Message: key mode needs the cold key; set `key_file`, or set `address_mode: true` to move by address only.
3. Key mode with a delegation or sequencer target: **the cold account is known to the ledger.** `client.IsKnownController` of its sigLock controller must be true. This is the same reader the ingate's unknown-sender rule calls (`txinput_queue.checkSenderPace` → `IsKnownController` on the LRB state), so the check accepts exactly what the ingate will. Message: the cold wallet owns no output in the ledger state; send it any amount with `proxi node send` and start again once that has settled.
4. When `key_file` is set: the cold key is unlocked, and its derived holder ID matches `holder_id`.

Check 3 runs before check 4, so the start fails before the passphrase prompt. Where check 3 applies it is repeated before each pair, beside the tick's other reads; an unknown account defers the move with one log line. One-transaction moves need no such check: they are signed by the hot key, which is known.

## 4. One transaction

Signed by the hot key and consuming the planned hot inputs (`plan.inputs`, as today), the transaction produces `kept` on a hot sigLock when `kept > 0`, and, for `moved`:

| Target | Produces for `moved` |
|---|---|
| cold sigLock | a cold sigLock of `moved` minus the fee; the tag-along fee, sender hot |
| new delegation (address mode) | a delegation of `moved` minus the fee, **master cold**; the tag-along fee, sender hot |
| top-up (address mode) | a top-up request of `moved`, sender hot, naming the cold-mastered delegation (no fee) |
| send to sequencer (address mode) | a tag-along of `moved` to the target, sender hot (it is its own fee, as today) |

The ledger allows each of these without the cold key:

- Nothing ties a delegation's master to the signer of the transaction that produces it (`_validDelegationProduced`); `TestDelegationOriginMasterNotSigner` covers it.
- Since library upgrade 7 anyone may top up a delegation (`_topUpAuthorised`; the sequencer's `req_topup.go` checks the master only before upgrade 7); `TestUpgrade7TopUpFromAnySender` covers it.
- A tag-along's sender must be the transaction's signer (`lock_tag_along.easyfl`). In address mode, the top-up request and the send to a sequencer are therefore from the hot wallet: a request the target does not take is reclaimed by the hot wallet.

In address mode a top-up is always the request: the master-path top-up spends the delegation and needs the cold key.

Compaction with nothing to move (`compact_at` reached, `moved == 0`) folds hot outputs into one hot output, as today.

## 5. Key mode: a move is two transactions

Every action that moves hot funds (`plan.moved > 0`) into a delegation or to a sequencer becomes a pair:

- **Transaction 1**, signed by the hot key: consumes the planned hot inputs and produces **at most two outputs**: a sigLock on the cold holder ID holding `moved`, and, when `kept > 0`, a hot sigLock holding `kept`. No tag-along, no fee, no other output. The wallet's balance stays in the hot wallet, never produced by a cold-signed transaction.
- **Transaction 2**, signed by the cold key: consumes transaction 1's cold output (and, for a master-path top-up, the cold-mastered delegation it tops up) and only moves it to the target, producing what today's single transaction produces for `moved`, with the cold holder ID wherever today's carries the wallet's:

| Target (today's function) | Transaction 2 produces |
|---|---|
| send to sequencer (`sendToSequencer`) | a tag-along of `moved` to the target, sender cold (it is its own fee, as today) |
| new delegation (`delegate`) | a delegation of `moved` minus the fee, master cold; the tag-along fee |
| top-up on the master path (`topUpDelegation`) | the re-delegated cold-mastered delegation plus `moved` minus the fee; the tag-along fee |
| top-up request (`kb/delegation_topup.md`) | the top-up request of `moved`, sender cold (no fee) |

Only transaction 2 carries a tag-along, and its fee comes out of transaction 1's cold output.

### 5.1 Submission and atomicity

**Atomic by structure, not by a new API.** Transaction 2 consumes transaction 1's output, so every state that contains transaction 2 contains transaction 1. Transaction 1 has no tag-along, so no sequencer includes it on its own: it reaches a state only when a transaction consuming one of its outputs does. Its cold output is spent only by transaction 2, since only the consolidator holds the cold key.

Its hot `kept` output could in principle be spent by another hot transaction, which would drag transaction 1 into the state without transaction 2. Every wallet picks inputs from the LRB state (`GetSpendableOutputs`): the consolidator, the other `proxi` commands and the miner. There `kept` appears only once transaction 1 has settled, and by then transaction 2 has settled with it. So both settle or neither does, unless someone deliberately builds a hot transaction on the unsettled `kept` output taken from a txstore. Even then nothing is lost: transaction 1 settles alone, `moved` stays on a cold sigLock, and the cold compaction (§6) folds it.

**Submission.** Both are built and signed in the same tick and submitted in order with today's `submit_tx`: transaction 1, then transaction 2. If transaction 1's submit fails, transaction 2 is not submitted. The tick's pending set is transaction 1's consumed hot IDs (plus the delegation on a master-path top-up), exactly as today.

**What the node does with them.** `processValidated` persists and relays every API transaction before the attach gate. A sequencer node drops transaction 1 at that gate (`shouldAttachNonSeq`: it pays no local sequencer), but it is already in its txstore. When the sequencer attaches transaction 2, solidification pulls transaction 1 from the txstore as a solicited transaction, which always attaches. Arriving out of order is handled the same way.

**Ingate.** The two senders differ, so each passes its own pace and known-sender checks. The hot wallet is known, since it spends its own outputs, and the cold wallet is known by check 3.

**Timestamps.** Transaction 1 is stamped as today (`Consolidator.timestamp`). Transaction 2 is stamped by the same rule with transaction 1 as its newest input: at least `TransactionPace` ticks after it, off the slot boundary. That can put it a few ticks ahead of the clock. The node holds it until the clock catches up (the `wait` path in `processValidated`), well inside the far-future bound.

**If transaction 2 is lost.** Transaction 1 stays in txstores and never reaches a state. Its hot inputs stay spendable. When the pending timeout (`pendingTimeout`) expires, the next tick builds a new pair, whose transaction 1 conflicts with the orphan, which is harmless.

## 6. Compacting the cold account

When the cold key is present, in either mode, the consolidator also compacts the cold account's sigLock outputs. Each move to the cold sigLock target leaves one, and outside payments add more. The rule is the hot one with nothing moved: when the cold account holds at least `compact_at` consolidatable sigLock outputs, one cold transaction folds up to `max_inputs` of them into one cold sigLock, minus the tag-along fee, sender cold. Its sender is known, since it holds those outputs, and the fold leaves one output, so the account stays known.

Without the cold key, which is possible only in address mode, nothing touches the cold account, and its sigLocks pile up until the key is present.

## 7. Delegations under `--target-cold`

**The managed set is the cold account's, in both modes.** `listOwnDelegations` reads the delegations mastered by the cold controller, a read that needs no key. The placement rules (count, target size, which one to top up) count that set.

**Tidying needs the cold key.** Fold, re-delegate a stale one, and `askStopUnderMinimum`, the request that keeps delegations within their bounds when a target falls below the minimum sequencer balance, all spend the delegation as its master, so they are signed by the cold key. Fold and re-delegate take their fee from the delegation, as today. askstop pays its fee from wallet outputs today, so it is funded by a pair: transaction 1 carries the fee from the hot inputs to a cold sigLock, with the change back to hot, and transaction 2 is the cold request. In address mode without the cold key nothing is tidied. A cold-mastered delegation whose target goes inactive, or that its target refuses to freeze, stays where it is until the consolidator runs with the cold key.

**Hot-mastered delegations move to the cold wallet**, in both modes, one per tick. The consolidator also lists the delegations mastered by the hot key and re-delegates one on the master path, signed by the hot key: same target, same amount minus the tag-along fee, master changed to the cold holder ID. The master path checks only the master's signature and pins nothing on the successor, so the master can change in place. Only the hot key is needed. A delegation is moved when the master can consume it, the same condition re-delegation reads today: on hold, never frozen, or inside the safe revocation window of an expired freeze. A frozen one waits for its window. One too small to stand after the fee is ended into the cold sigLock instead.

**Order within a tick**, one action at most, as today: tidy the cold set (when the cold key is present), move one hot-mastered delegation, move hot funds, compact the cold account.

The other shared helpers take the cold holder ID where the delegation must be the cold wallet's: `SelectDelegationTarget`, the target's cut and room checks, and the rating. They are otherwise unchanged.

## 8. What the cold key protects, and what it does not

- **Key mode:** the cold key is in the consolidator's memory. It protects against loss of the hot key, which typically sits in more places: the miner, scripts, other `proxi` commands. It does not protect against compromise of the process or machine running the consolidator.
- **Address mode:** the cold key need not be on the machine, so a compromised hot key or machine loses only the hot minimum, what has accumulated since the last sweep, and top-up requests not yet taken. The price is twofold:
  - `proxi` cannot check that the cold key exists or matches `holder_id`.
  - Without the key, the cold delegations are not tidied and the cold sigLocks are not compacted.

## 9. Where the code changes

- `proxi/glb/profile.go`, `keyfile.go`: `cold_wallet` reading, `address_mode` included; a cold-key loader that skips `PROXIMA_KEY_PASSPHRASE` and leaves `keyPassphrase` alone; the holder-ID checks.
- `proxi/node_cmd/consolidate/consolidate.go`: the flag, the start-up checks, the address-mode warning, and the cold holder ID, the mode and the cold key passed in `consolidator.Environment` (the key nil when absent).
- `consolidator/consolidator.go`, `delegate.go`:
  - The one-transaction moves to the cold account.
  - The key mode's pair. The existing builders already split into consuming inputs, producing outputs and finishing, so they are reused, not copied: inputs become transaction 1's cold output, and the signer, master and sender become the cold key.
  - The migration of hot-mastered delegations.
  - The cold compaction.
  - The delegation listing on the cold controller.
  - The per-tick known check.
- Tests in `consolidator/`:
  - The one-transaction shape per target: cold sigLock of `moved` minus the fee, delegation with master cold, top-up request naming a cold delegation; each with a hot `kept` and hot sender.
  - The pair's shape per target: transaction 1 has one cold output holding `moved` and at most one hot output holding `kept`; transaction 2 has the only tag-along and no hot output.
  - Transaction 2's timestamp at least `TransactionPace` after transaction 1.
  - The migration's successor: same target, master cold.
- Ledger test `ledger/tests/delegation_master_signer_test.go`: a delegation origin naming a master other than its signer is valid, before and after upgrade 7.
- `kb/consolidate.md` gets a pointer; the user-facing page on the docs site gets a section once built.
