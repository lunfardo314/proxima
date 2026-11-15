# Sequencer commands

> **LIVE.** Developer reference for the commands a wallet sends to a sequencer inside a tag-along output: the `tagAlong` lock that carries them, the request payload, and each command this package implements. Ledger side: `ledger/def/lock_tag_along.easyfl`, `ledger/def/ensure.easyfl` (the guarantors, `ensureTopUpDelegation` replaced by `ledger/def/upgrade6.easyfl`). Sequencer side: this package (`parse.go`, `req_*.go`) and `sequencer/backlog`.

A sequencer does not take orders over the network. A wallet that wants something from a sequencer (pay it a fee, change its settings, take tokens off its chain, stop or top up a delegation) puts the order into an output on the ledger, locked with `tagAlong` to that sequencer. The sequencer finds it, interprets it, and carries it out by consuming the output in one of its own milestones. The order and its outcome are therefore both ledger transactions, and two facts about every order are fixed by the ledger, not by the sequencer: **who sent it** and **how long it stays open**.

## 1. The tag-along lock

### 1.1 Layout

A tag-along output has at least three and at most five elements:

| Index | Element | Content |
|-------|---------|---------|
| 0 | amounts | token balance: the fee, the compensation or the amount to deliver, depending on the command |
| 1 | index values | tuple `[senderID, targetSequencerID]`: the sender's 32-byte holder ID at position 0, the target sequencer's 24-byte chain ID at position 1 |
| 2 | lock | `tagAlong`, a constant call with no arguments; every tag-along output carries the same bytes here |
| 3 | request data (optional) | inline data with the request payload (section 2) |
| 4 | guarantor (optional) | `ensureStopDelegation(...)` or `ensureTopUpDelegation(...)`, for the commands whose effect the ledger enforces (section 1.4) |

The sender and the target are not arguments of the lock: the public `tagAlong` reads them off the index-value tuple and passes them to `_tagAlong(target, sender)`. Because they sit in the index-value tuple, the output is indexed under both accounts: the sender finds its open requests, the sequencer finds the requests addressed to it. In Go the pair is `ledger.TagAlongLock{TargetSequencerID, SenderID}`, and `ledger.TagAlongOutput` wraps an output with its parsed lock.

### 1.2 What the ledger checks when the output is produced

`_tagAlong` on the produced side requires:

- the lock at the lock index (2);
- a target that is 24 bytes long and not all zeros (a zero chain ID is the usual mistake of an uninitialised field);
- fewer than six elements;
- **the sender equal to the holder ID of the key that signed the transaction** (`txHolderID(txSignatureData)`).

The last rule is what makes the sender securely known. Nobody can produce a tag-along naming someone else as its sender, because the transaction producing it would have to be signed by that someone's key. Every authorisation decision a sequencer makes on a command (section 3) is a comparison of this sender ID with a fact on the ledger: the sequencer's own key, or the master of a delegation. The sequencer never has to trust anything the payload says about who sent it.

### 1.3 Who may consume it, and when

The consuming side measures `selfInputSlotPace`, the number of slots between the output's slot and the consuming transaction's slot, and opens three windows in turn:

| Slots since the output | Who may consume | How | Purpose |
|------------------------|-----------------|-----|---------|
| 0 to 29 (`constTagAlongSlots` = 30, about 5 minutes) | the target sequencer only | the output is unlocked as a chain lock of the target chain, i.e. in a transaction that also moves the target sequencer's chain | **accept**: the sequencer carries out the command |
| 30 to 389 (`constTagAlongReclaimSlots` = 390, about 1 hour 5 minutes) | the sender only | signature lock of the sender | **reclaim**: the sender takes back a request the sequencer did not take |
| 390 and later | anybody | no unlock condition | **cleanup**: an output left behind by both sides does not stay in the ledger state forever |

So every command has a hard deadline. A sequencer that has not taken a request within 30 slots can never take it: both `ledger.TagAlongOutput.IsTagAlongSlot` and the parser (`TxBuilderCommandFromOutput` refuses an output past its window, permanently) know this. From then on the tokens are the sender's again, and after the reclaim window anybody's. `TagAlongOutput.StatusInSlot` names the three states `tag-along`, `tag-along-reclaim` and `tag-along-purge`; the Go helpers take the constants from the library of the output's own slot.

The guarantors at element 4 (section 1.4) follow the same windows. Each binds only while the target can consume the output, and steps aside from `constTagAlongSlots` on (`ensureStopDelegation` and `ensureTopUpDelegation` both test `selfInputSlotPace` when the output's lock is `tagAlong`). A guarantor that kept demanding a delegation transition after the target's window would make the sender's reclaim impossible.

The wallet side of reclaim and cleanup is `proxi node compact`: it sweeps the wallet's own tag-alongs past the acceptance window and reports those past the reclaim window as claimable by anyone; `proxi node compact scan` counts both kinds.

### 1.4 Guarantors

The lock decides who may consume a request, not what the consumer must do in return. For most commands that is enough: the sequencer either carries the command out or does not, and in the second case the sender reclaims the tokens. When the sender's tokens pay for an effect, though, the sequencer could consume the request, keep the tokens and skip the effect. A **guarantor** closes that gap: an `ensure...` constraint at element 4 of the request which, when the request is consumed inside the target's window, fails the transaction unless it carries out the promised effect. The guarantors in the ledger today are both in `ensure.easyfl`:

| Guarantor | Command | Enforces |
|-----------|---------|----------|
| `ensureStopDelegation(delegationID, allowance)` | ask to stop (code 3) | the named delegation is produced on hold; the allowance is within the projected compensation |
| `ensureTopUpDelegation(delegationID)` | top up (code 4) | the named delegation is produced frozen through the delegate lock's top-up path, which delivers the whole request balance |

A guarantor is read by the parser and by the classifiers in `txbuildercore`, so a wallet sweeps back an untaken request with a guarantor like any other tag-along (`isTagAlongRequest`).

### 1.5 How a sequencer finds and handles requests

- **Discovery.** The backlog (`sequencer/backlog`) listens to outputs indexed under the sequencer's chain account. It takes `tagAlong` outputs and plain `chainLock` outputs locked to the chain, and drops outputs of branch transactions, outputs of BAD transactions, outputs with a chain constraint, and outputs whose balance is below the minimum fee the sequencer declares in its data (`fee`, default 100,000 motes, 0.1 PROX). The fee is checked again when the backlog is purged, so a raised fee takes effect without a restart.
- **Parsing.** `TxBuilderCommandFromOutput` (`parse.go`) pre-parses the output, refuses it once it is past the acceptance window, and hands it to the parser registered for its request code. A plain `chainLock` output with at most three elements is read as a no-op command.
- **Applying.** The command's `Apply` adds the inputs and outputs it needs to the milestone being built (`SeqTxBuilder`). `AttachmentCostDelta` tells the proposer, before the command is applied, how much it adds to the milestone's attachment cost, so the proposer can keep the tag-along phase within its budget.
- **Permanent and temporary refusal.** Both the parser and `Apply` return `(valid, err)`. `err` with `valid = false` means the request can never be carried out: the proposer blacklists the output and logs a permanent rejection. `err` with `valid = true` means not now: the output is skipped in this milestone and tried again in a later one, as long as it is inside its window. A request on a delegation also waits when an own milestone since the branch has already spent the delegation output the baseline state shows.

## 2. The request payload

### 2.1 Format

Element 3, when present, is EasyFL inline data wrapping a `util/smallkv` map: a small map with one-byte keys, serialised as a sorted tuple of `key || value` entries (at most 256 entries; setting an empty value deletes a key). Key `0` (`FieldCmdCode`) holds the one-byte request code and must be present. The other keys are per command.

`txbuildercore.Library.NewSequencerRequestOutput(fee, target, sender, requestCode, params, extras...)` is the wallet-side builder used by `proxi` and the consolidator: it builds elements 0 to 2, writes the request code into the payload (overwriting a key `0` the caller may have set), and appends each of `extras` from element 4 on. The `New...Output` functions in this package build the same layouts with the typed `ledger` API.

### 2.2 Rules the parser applies

- No element 3: a no-op command (code 0).
- Element 3 present: it must be inline data holding a valid `smallkv` map with a one-byte, non-zero code at key `0`. A no-op is expressed by leaving element 3 out, never by code 0 in the payload. Anything else is refused permanently, including an element 3 that is not inline data.
- An unknown code is refused permanently.
- Each command then checks the exact number of elements it expects (section 3).

### 2.3 Request codes

| Code | Constant | Command | Elements | Guarantor | Balance means |
|------|----------|---------|----------|-------------------|---------------|
| 0 | `RequestCodeNoop` | no request | 3 | none | fee |
| 1 | `RequestCodeWithdrawFromSeq` | withdraw from the sequencer chain | 4 | none | fee |
| 2 | `RequestCodeSetSequencerData` | set sequencer data | 4 | none | fee |
| 3 | `RequestCodeAskStopDelegation` | ask to stop a frozen delegation | 4 or 5 | `ensureStopDelegation` (optional) | compensation to the sequencer |
| 4 | `RequestCodeTopUpDelegation` | top up a delegation | 5 | `ensureTopUpDelegation` (required) | the amount added to the delegation |

Codes 5 and 6 are proposed for sequencer succession in `kb/control_succession.md`, which is not built.

## 3. The commands

In every command below, "the sequencer's own key" is the key the sequencer node signs its milestones with, i.e. the controller of the sequencer chain (`base.HolderIDFromPublicKey` of `SeqTxBuilder`'s public key). Amounts are in motes, 1 PROX = 1,000,000 motes.

### 3.1 No request (code 0)

`NoRequestTxBuilderCommand`, `req_noop.go`.

- **Layout.** Elements 0 to 2 only. Built by `ledger.NewTagAlongOutput` or `txbuildercore.NewTagAlongOutput`.
- **Parameters.** None.
- **Authorisation.** None: anybody may pay a sequencer.
- **Effect.** The output is consumed and its balance added to the sequencer chain's balance.
- **Attachment cost.** 1 (the consumed output).
- **Use.** The fee a wallet attaches to its own transaction so that a sequencer includes it in its past cone (`proxi node send`, `proxi node compact`, foundry retire, the consolidator, the miner's fee).

"No-op" means the sequencer carries out nothing beyond taking the tokens: the output is a fee or a gift to the sequencer chain. Two kinds of output are read this way:

- **A tag-along without element 3.** The sender is known and the windows of section 1.3 apply: the sequencer may take the fee within 30 slots, after that it goes back to the sender, and after 390 slots to anybody.
- **A plain `chainLock` output to the sequencer chain, with no element beyond the lock.** It belongs to the chain outright: it carries no sender and has no deadline, so the sequencer may take it at any time, it never returns to whoever paid it, and nothing else can be asked of it. Since no command could be authorised or bounded in time, the only reading is a no-op; one with more than three elements is refused, since the sequencer would not know how to unlock the rest. `proxi` does not make such outputs; paying with a tag-along keeps the payer's right to reclaim.

### 3.2 Withdraw from the sequencer chain (code 1)

`WithdrawFromChainTxBuilderCommand`, `req_withdraw.go`. Built by `NewWithdrawRequestOutput(seqID, sender, fee, amount, target)`. Sent by `proxi node sequencer withdraw <amount>`.

- **Layout.** Exactly four elements.
- **Parameters.**
  - `'a'` (`FieldWithdrawAmount`): the amount to withdraw, a big-endian `uint64` with leading zeros trimmed.
  - `'t'` (`FieldWithdrawTarget`): the lock of the output receiving the amount, as the wallet's source string (`ledger.Controller.Source()`, a signature lock or a chain lock with its ID in hex), parsed by `ledger.ControllerFromSource`.
- **Authorisation.** The sender must be the sequencer's own key. A request from anybody else is refused permanently.
- **Checks.** Both parameters present and parseable; the amount at least `MinimumAmountToRequestFromSequencer` (1,000,000, 1 PROX). Otherwise refused permanently. When applied, the chain balance accumulated so far in the milestone must be above the amount, or the request is refused permanently.
- **Effect.** The request is consumed and its fee added to the chain balance; a new output of the requested amount, locked as `'t'` says, is produced; the amount is taken off the chain balance.
- **Attachment cost.** 2 (the consumed request, the produced output).
- **Ledger guarantee.** None beyond the lock: no guarantor binds the sequencer to pay out. The controller is asking its own sequencer; if the request is not honoured within 30 slots, the controller reclaims the fee.

### 3.3 Set sequencer data (code 2)

`SetSequencerDataTxBuilderCommand`, `req_seqdata.go`. Built by `NewSeqDataCommandOutput(seqID, sender, fee, newParams)`. Sent by `proxi node sequencer set-params`, which reads the current data off the chain, applies the flags given and sends the result.

- **Layout.** Exactly four elements.
- **Parameters.**
  - `'d'` (`FieldSetSequencerDataBinary`): the complete sequencer data, `seqdata.SequencerData` serialised as compact JSON. Keys: `name`, `fee` (minimum tag-along fee), `profit_cut` (promille of delegation inflation the sequencer keeps), `greedy`, `pace`, `freeze_bounds`, `min_topup`, `max_frozen`, `max_frozen_delegations`. Keys this build does not know are kept and written back unchanged.
- **Authorisation.** The sender must be the sequencer's own key; otherwise refused permanently.
- **Checks.** The JSON must parse; otherwise refused permanently.
- **Effect.** The request is consumed and its fee added to the chain balance. The data in the request **replaces** the data carried on the sequencer chain output from this milestone on. It is not merged: a key left out of the request is removed from the chain. The sequencer node reads its declared parameters (minimum fee, minimum top-up, freeze caps, cut) back off its latest milestone, so a change takes effect without a restart.
- **Attachment cost.** 1.

### 3.4 Ask to stop a delegation (code 3)

`AskStopDelegationRequest`, `req_askstop.go`. Built by `NewAskStopDelegationReqOutput(seqID, sender, delegationID, fee, allowance)` or `NewSequencerRequestOutput` with `NewEnsureStopDelegationConstraint`. Sent by `proxi node delegate askstop <delegation ID> | all [max]` and by the consolidator.

A delegation frozen by its target cannot be taken back by its master before the freeze ends, because the target has already paid the delegator an advance on the inflation the freeze would earn. This command asks the target to end the freeze early and put the delegation on hold, so the master can take it back, in return for the part of the advance the remaining frozen span will no longer earn.

- **Layout.** Four elements, or five with `ensureStopDelegation(delegationID, allowance)` at element 4. The builders in this package and in `txbuildercore` always add it.
- **Parameters.**
  - `'i'` (`FieldRevokeDelegationID`): the delegation's 24-byte chain ID.
  - In the guarantor: the same chain ID, and the allowance, the most the sequencer may take out of the delegation's own balance as compensation (0: nothing, the request balance must cover all of it).
  - The request balance: the compensation the sender pays.
- **Authorisation.** The delegation's target must be this sequencer, and **the sender must be the delegation's master**. Otherwise refused permanently.
- **Checks** (all permanent when they fail):
  - the delegation exists in the baseline state and is a delegation output;
  - it is frozen in the milestone's slot (an unfrozen delegation needs no stop);
  - more than 6 slots remain until the freeze ends; closer to the end the request is refused, since the delegation reaches its safe revocation window on its own within about a minute;
  - when a guarantor is present: at element 4, naming the same delegation, with an allowance no larger than `DelegationOutput.AllowanceCeiling`;
  - the compensation the sequencer needs, `ChainInflationMultiStep(delegation balance, slot, slots left) * AdvanceShare / 1000` (the uncut inflation over the remaining span at the share the sequencer actually advanced, pinned in the delegation when it was frozen), is covered by the request balance plus the allowance.
- **Effect.** The request and the delegation are consumed, and the delegation's successor is produced on hold (`MakeDelegationRevokeOutput`). The request balance, the delegation's last slot of inflation and the allowance taken go to the sequencer chain; the delegation's frozen coverage leaves the chain's frozen coverage vector. When an allowance is taken, the delegate lock's unlock data carries a third byte naming the request, so the lock permits the balance decrease the allowance covers. Applying is postponed (temporary) when the same delegation is already an input of this milestone, as when two requests on one delegation are in the backlog at once.
- **Attachment cost.** 3 (the request, the delegation, the successor).
- **Ledger guarantee.** The guarantor `ensureStopDelegation` (`ensure.easyfl`): if the target consumes the request inside its window, it must produce the named delegation, and the produced delegation must be on hold (state 2). The allowance may not exceed the projected compensation measured from the delegation output's own slot, so an oversized value cannot drain the delegation whatever the wallet computed. The sender's tokens cannot be taken without the stop being carried out.

### 3.5 Top up a delegation (code 4)

`TopUpDelegationRequest`, `req_topup.go`. Built by `NewTopUpDelegationReqOutput(seqID, sender, delegationID, amount)` or `NewSequencerRequestOutput` with `NewEnsureTopUpDelegationConstraint`. Sent by `proxi node delegate topup <amount>`, the consolidator and the miner. Spec: `kb/delegation_topup.md`.

Adds the whole request balance to an existing delegation targeting this sequencer, in any state the target can act on. There is no fee: the sequencer is paid by the frozen coverage it gains.

- **Layout.** Exactly five elements: the payload at element 3 holds only the request code; element 4 is `ensureTopUpDelegation(delegationID)`.
- **Parameters.**
  - In the guarantor: the delegation's 24-byte chain ID.
  - The request balance: the amount added.
- **Authorisation.** The delegation's target must be this sequencer, and **the sender must be the delegation's master**. Otherwise refused permanently.
- **Checks.** Refused permanently when:
  - the layout is wrong;
  - the amount is below the minimum top-up, `MinimumTopUp()`: the `min_topup` the sequencer declares in its data, never below `txbuildercore.MinimumTopUpAmount` (90 PROX);
  - the delegation is missing from the baseline state or is not a delegation;
  - the delegation is frozen and the share pinned at its freeze is above what the sequencer now tolerates (`MaxDelegatorCut`), or it is unfrozen and the sequencer cannot compute an advance share for it.
  Postponed (temporary) when the delegation cannot be unlocked by its target in this slot (on hold, inside the safe revocation window, or too young), or, when applied, when: the delegation is already an input of this milestone; the sequencer freezes nothing (a freeze cap of 0); a fresh freeze needs the minimum sequencer balance; no epoch has room for the freeze; the chain balance cannot pay the advance; the freeze would pass the coverage upper bound (with `freeze_bounds`) or `max_frozen`.
- **Effect.** The request and the delegation are consumed and the successor produced. A delegation frozen in this slot stays frozen with its epoch and pinned share (`MakeDelegationTopUpOutput`); an unfrozen one is frozen in the same transition with the amount included (`MakeDelegationFreezeOutputWithTopUp`). The request balance passes straight into the delegation. The sequencer pays, from its chain balance, the advance on the newly frozen amount, and the successor's frozen coverage is added to the chain's frozen coverage vector. The delegate lock's unlock data carries a third byte naming the request.
- **Attachment cost.** 3 (the request, the delegation, the successor).
- **Ledger guarantee.** The guarantor `ensureTopUpDelegation` (`ensure.easyfl`, replaced from library upgrade 6 by `upgrade6.easyfl`): if the target consumes the request inside its window, the successor must be the named delegation, marked frozen (state 1), the consumed predecessor must be locked by `delegateLock`, and that lock must have been unlocked with a reference to this very request. The `delegateLock` requirement came with upgrade 6: before it, a chain under a lock that ignores its unlock parameters (a `sigLock`) carrying a `delegateLockState` met the other checks, and the request's tokens ended in that chain. That reference puts the delegate lock on its top-up path, which requires the successor balance to equal the predecessor plus inflation plus the request balance plus the advance on the newly frozen amount. Delivery of every token and the advance are both enforced by the ledger. The same path (`_topUpAuthorised` in `lock_delegate.easyfl`) requires the request to be a tag-along whose sender is the delegation's master, so the master-only rule is ledger law, not just sequencer policy: a sequencer that skipped the check would build an invalid milestone.

## 4. Adding a command

1. Pick the next free code (mind the codes reserved in section 2.3) and add a `req_<name>.go` with the `RequestCode...` and `Field...` constants, a parser, a type implementing `TxBuilderCommand`, and a `New...Output` builder.
2. Register the parser in `_cmdParsers` (`parse.go`).
3. Check the exact element count, and authorise by comparing `SenderID` with a ledger fact. Return `valid = false` only for what can never become applicable within the request's window; everything that may change before the window closes is a temporary refusal.
4. If the sender's tokens buy an effect the sequencer could otherwise skip while keeping the tokens, put a guarantor at element 4 that enforces the effect, and make it step aside from `constTagAlongSlots` on when the output's lock is `tagAlong`, as both existing guarantors do. Pin everything the effect depends on, the lock of every output it reads included: the upgrade 6 fix to `ensureTopUpDelegation` is the example of what a missing pin lets through. A new guarantor is a ledger change: a library upgrade.
5. Teach the wallet classifier (`ledger/txbuildercore/spendable_classify.go`, `isTagAlongRequest`) the new layout so an unclaimed request is swept back like any other tag-along.
