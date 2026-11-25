# Cold wallet in `proxi node consolidate`

> **DRAFT** — spec, not built. Branch `feat/cold-wallet`, written 2026-11-25. No ledger change, no hardfork, no node change: wallet side only (`proxi/glb`, `consolidator`, `proxi/node_cmd/consolidate`).

---

## 1. What and why

`proxi node consolidate` sweeps the wallet's account and moves what is above the configured minimum into consensus: to a sequencer, into delegations, or into one output (`kb/consolidate.md`). Everything it creates is controlled by the same key that receives the payouts: the key of a miner, a sequencer controller or any other always-online process. That key is hot.

With the cold wallet, the hot wallet keeps receiving and keeps its minimum, but everything the consolidator moves out ends up controlled by a second key: the delegation master, the sender of a tag-along and the owner of any output the move produces. Losing the hot key then loses at most the hot wallet's minimum and what has arrived since the last sweep, not the delegations.

The cold key is decrypted in the consolidator's process for as long as `--target-cold` runs (§8). It is "cold" because no other `proxi` command and no other process ever unlocks it, not because it is offline.

## 2. Configuration

A new top-level section of the wallet profile `proxi.yaml`, next to `wallet`:

```yaml
wallet:
  key_file: wallet.key
  holder_id: <hex>
  sequencer_id: <hex>
cold_wallet:
  key_file: cold.key
  holder_id: <hex>
```

- **Absent or empty `cold_wallet`: no-op.** Every command behaves as today, and `--target-cold` refuses to start (§4).
- `key_file` is a keystore file, as `wallet.key_file`; `holder_id` is its holder ID, hex.
- The holder ID can be read without the passphrase (it is in the keystore file). Every command that only needs to name the cold account uses `holder_id` and never unlocks the key. Only `proxi node consolidate --target-cold` unlocks it.
- On unlocking, the holder ID derived from the key must equal `cold_wallet.holder_id`, as for `wallet.holder_id` (`glb.GetWalletData`), and must differ from the hot wallet's holder ID. Either mismatch refuses the start.
- **Passphrase.** The keystore's own passphrase file (named after its holder ID, `Keystore.ReadPassphraseFile`) is read as for the hot key; otherwise `proxi` prompts. `PROXIMA_KEY_PASSPHRASE` is **not** read for the cold key: it is the hot key's passphrase, and a cold key under the same passphrase is no separation. Unlocking the cold key must not overwrite `glb.KeyPassphrase()`, which hands the hot key's passphrase to a spawned nonce seeker. So `LoadPrivateKeyFromFile` gets an option, or a sibling, that skips the environment variable and the global.

## 3. The command

`proxi node consolidate --target-cold`. The sibling flags are hyphenated (`--send-to-sequencer`, `--target-delegations`), so the flag is `--target-cold`. The request named it `--target_cold`. All other flags and the `consolidate` section of the profile mean what they mean today. The trigger, the threshold, the minimum, the split rules and target selection are unchanged: only the shape of each move changes.

## 4. Start-up checks

In this order, each failure refusing the start with a message naming the fix:

1. `cold_wallet.key_file` and `cold_wallet.holder_id` are configured.
2. The cold holder ID differs from the hot one.
3. **The cold account is known to the ledger:** `client.IsKnownController` of its sigLock controller is true. This is the same reader the ingate's unknown-sender rule calls (`txinput_queue.checkSenderPace` → `IsKnownController` on the LRB state), so the start-up check accepts exactly what the ingate will. Message: the cold wallet owns no output in the ledger state; send it any amount from the hot wallet with `proxi node send` and start again once it has settled.
4. The cold key is unlocked, and its derived holder ID matches `cold_wallet.holder_id`.

Check 3 runs before check 4, so the start fails before the passphrase prompt. Check 3 is repeated before each pair (§5) as part of the tick's reads. If the account has become unknown, the move is deferred with one log line, as a missing tag-along target is deferred today. Transaction 2 consumes only transaction 1's output, never an older cold output, so the consolidator never makes the cold account unknown itself.

## 5. A move is two transactions

Every action that moves hot funds (`plan.moved > 0`) becomes a pair:

- **Transaction 1**, signed by the hot key: consumes the planned hot inputs (`plan.inputs`, as today) and produces **at most two outputs**: a sigLock on the cold holder ID holding `moved`, and, when `kept > 0`, a sigLock on the hot holder ID holding `kept`. No tag-along, no fee, no other output.
- **Transaction 2**, signed by the cold key: consumes transaction 1's cold output (and, for a master-path top-up, the cold-mastered delegation it tops up) and only moves it to the target. It produces what today's single transaction produces for `moved`, with the cold holder ID wherever today's carries the wallet's, and nothing goes back to the hot wallet:

| Mode (today's function) | Transaction 2 produces |
|---|---|
| send to sequencer (`sendToSequencer`) | a tag-along of `moved` to the target, sender cold (it is its own fee, as today) |
| new delegation (`delegate`) | a delegation of `moved` minus the fee, master cold; the tag-along fee |
| top-up on the master path (`topUpDelegation`) | the re-delegated cold-mastered delegation plus `moved` minus the fee; the tag-along fee |
| top-up request (`kb/delegation_topup.md`) | the top-up request of `moved`, sender cold (no fee) |
| into one output (no destination) | one cold sigLock of `moved` minus the fee; the tag-along fee |

Only transaction 2 carries a tag-along. The fee comes out of transaction 1's cold output.

**`kept` goes back in transaction 1.** The wallet's current balance, the minimum, stays in the hot wallet and is never produced by a cold-signed transaction; transaction 2 touches only what moves. The cost is a weaker atomicity argument, discussed in §6.

**Not a move: stays one transaction.** Compaction with nothing to move (`compact_at` reached, `moved == 0`) folds hot outputs into one hot output as today, signed by the hot key.

## 6. Submission and atomicity

**Atomic by structure, not by a new API.** Transaction 2 consumes transaction 1's output, so every state that contains transaction 2 contains transaction 1. Transaction 1 has no tag-along, so no sequencer ever includes it on its own: it reaches a state only when a transaction consuming one of its outputs does. Its cold output is spent only by transaction 2, since only the consolidator holds the cold key. Its hot `kept` output could in principle be spent by another hot transaction, which would drag transaction 1 into the state without transaction 2. Every wallet, the consolidator, the other `proxi` commands and the miner, picks inputs from the LRB state (`GetSpendableOutputs`), where `kept` appears only once transaction 1 has settled, and by then transaction 2 has settled with it. So both settle or neither does, unless someone deliberately builds a hot transaction on the unsettled `kept` output taken from a txstore. Even then nothing is lost: transaction 1 settles alone, `moved` stays on a cold sigLock, and the move is simply not made (the consolidator never sweeps cold outputs, open question 4). No batch endpoint is needed, and none is added.

**Submission.** Both are built and signed in the same tick and submitted in order with today's `submit_tx`: transaction 1, then transaction 2. If transaction 1's submit fails, transaction 2 is not submitted. The tick's pending set is transaction 1's consumed hot IDs (plus the delegation on a master-path top-up), exactly as today. The pair has settled when they leave the state.

**What the node does with them.** `processValidated` persists and relays every API transaction before the attach gate. A sequencer node drops transaction 1 at that gate (`shouldAttachNonSeq`: it pays no local sequencer), but the transaction is already in its txstore. When the sequencer attaches transaction 2, solidification pulls transaction 1 from the txstore as a solicited transaction, which always attaches. Arriving out of order is handled the same way.

**Ingate.** The two senders differ, so each passes its own pace and known-sender checks. The hot wallet is known, since it spends its own outputs, and the cold wallet is known by §4.

**Timestamps.** Transaction 1 is stamped as today (`Consolidator.timestamp`). Transaction 2 is stamped by the same rule with transaction 1 as its newest input: at least `TransactionPace` ticks after it, off the slot boundary. Transaction 2 can therefore be stamped a few ticks ahead of the clock. The node holds it until the clock catches up (the `wait` path in `processValidated`), well inside the far-future bound. Submitting it right away is simpler than sleeping in the consolidator.

**If transaction 2 is lost.** Transaction 1 stays in txstores and never reaches a state. Its hot inputs stay spendable, the pending timeout (`pendingTimeout`) expires and the next tick builds a new pair, whose transaction 1 conflicts with the orphan, which is harmless.

## 7. Delegations under `--target-cold`

Delegations created in cold mode have the cold wallet as master, so:

- **The managed set is the cold account's.** `listOwnDelegations` reads delegations mastered by the cold controller. Tidying (fold, re-delegate a stale one, `askStopUnderMinimum`) acts on that set, signed by the cold key. Fold and re-delegate take their fee from the delegation, as today, and need no hot funds.
- **askstop** pays its fee from the wallet's outputs today. In cold mode the request must come from the master, the cold key. It is funded as a move, by a pair: transaction 1 carries the fee from the hot inputs to a cold sigLock, the change back to hot, and transaction 2 is the cold request. The cold account's own sigLocks, including the one that keeps it known, are never touched.
- **Hot-mastered delegations left from before** are not touched in cold mode. Running without `--target-cold` manages them as before. Folding them into the cold set is an open question (§9).

The other sharing points take the cold holder ID where the delegation or tag-along must be the cold wallet's: `SelectDelegationTarget`, the target's cut and room checks, and the rating. They are otherwise unchanged.

## 8. What the cold key protects, and what it does not

- It protects against loss of the hot key, which typically sits in more places than the consolidator: the miner, scripts, other `proxi` commands. The hot key can take the hot minimum and what has accumulated since the last sweep, nothing the consolidator has moved.
- It does not protect against compromise of the process or machine running `--target-cold`: the cold key is in memory there. An operator who wants the cold key offline does not run `--target-cold`. They let the consolidator move to the cold wallet by address only (open question 1).

## 9. Open questions

1. **Address-only mode.** Could `--target-cold` without the cold key in memory send everything above the minimum to the cold sigLock, one hot transaction with a tag-along fee? That moves funds to an offline key but puts nothing into consensus, so it is not what this spec does. It would be the simple variant if the cold key must stay off the machine.
2. **One transaction in plain mode.** With no destination configured, a single hot transaction (inputs → cold sigLock + kept + tag-along fee) does the same as the pair without the cold key. The spec keeps two transactions for every move, as asked, because one code path is simpler. Confirm, or take the shortcut.
3. **Hot-mastered delegations** when switching to cold mode: leave them (spec) or move them under the cold master one by one, which a master-path re-delegation can do (master change in place, if the delegate lock allows it: to verify).
4. **Cold account compaction.** Transactions 2 in plain mode produce one cold sigLock each and nothing folds them. Leave them to a manual `proxi node compact` under the cold profile, or let transaction 2 also consume older cold sigLocks, keeping at least one so the account stays known.
5. **Flag spelling**: `--target-cold` (sibling flags) or `--target_cold` (as requested).
6. **Profile alternative.** A cold-only operator could run a second profile with `wallet` pointing at the cold key. `cold_wallet` is preferred because one process then holds both keys and builds the pair atomically.

## 10. Where the code changes

- `proxi/glb/profile.go`, `keyfile.go`: `cold_wallet` reading; a cold-key loader that skips `PROXIMA_KEY_PASSPHRASE` and leaves `keyPassphrase` alone; the holder-ID checks.
- `proxi/node_cmd/consolidate/consolidate.go`: the flag, start-up checks 1-4, and the cold key passed in `consolidator.Environment` (a second private key, nil when off).
- `consolidator/consolidator.go`, `delegate.go`: a cold mode in which each move builds transaction 1 then transaction 2. The existing builders already split into consuming inputs, producing outputs and finishing, so they are reused, not copied: inputs become transaction 1's output, and the signer, master and sender become the cold key. The delegation listing reads the cold controller. The per-tick known check sits beside the existing reads.
- Tests in `consolidator/`: the pair's shape per mode (transaction 1 has one cold output holding `moved` and at most one hot output holding `kept`, transaction 2 the only tag-along and no hot output), and transaction 2's timestamp at least `TransactionPace` after transaction 1.
- `kb/consolidate.md` gets a pointer; the user-facing page on the docs site gets a section once built.
