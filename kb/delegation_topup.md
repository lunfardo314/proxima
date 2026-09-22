# Top-up request: adding tokens to a delegation through its target

> **SPEC — take 1, not implemented.** A ledger change (hardfork), so it lives on
> `develop-take1` and ships with the next reset. Written 2026-09-22 to be implemented
> from. Replaces the askstop-and-re-delegate loop of
> `kb/archive/shipped/delegation_add_tokens.md` for the frozen case; the master-side add
> of that document stays for the states the master can consume.

## 1. Goal

A delegator adds tokens to an existing delegation with **one tag-along output** sent to
the delegation's target sequencer, whatever state the delegation is in, and the
delegation stays frozen throughout. No askstop, no unwind, no idle slots, no second
transaction from the wallet.

Today a top-up of a frozen delegation costs three transactions and a round trip: an
askstop request, the sequencer's milestone that puts the delegation on hold, and the
wallet's re-delegation with the added amount, after which the target freezes it again.
`proxi node delegate topup` and step 4 of the consolidator's placement rule do exactly
this. With ~7,700 mine payouts a day at pace 1, most of them landing on frozen
delegations, that loop is the wrong shape.

## 2. The request

A tag-along output to the target sequencer, in the shape the sequencer's request parser
already reads (`sequencer/txbuilder_seq/parse.go`):

| Index | Element |
|-------|---------|
| 0 | amounts: the amount to add, nothing else |
| 1 | index values: sender = the master's holder ID (pinned by `tagAlong` to the signer) |
| 2 | `tagAlong(targetSequencerID)` |
| 3 | inline request data: code `RequestCodeTopUpDelegation = 4`, field `i` delegation chain ID |
| 4 | `ensureTopUpDelegation(delegationID)` |

There is no fee. The whole balance of the request goes into the delegation; what the
sequencer earns is the frozen coverage of the added amount for the rest of the span,
which is what it freezes delegations for in the first place. The amount is the output's
balance, so it is not repeated in the request data. Element 4 is what makes the transfer
enforceable: the sequencer cannot take the tokens and leave the delegation as it was.

**Minimum top-up.** A tiny top-up costs the sequencer an input and an output in a
milestone for almost no coverage, so the sequencer enforces a minimum amount. It is a
sequencer setting published on the ledger like the minimum fee, `MinTopUp` in
`SequencerData` (`sequencer/seqdata/seqdata.go`, set with `proxi node seq set-params`),
with a hard floor of **100 PROX** in Go (`MinimumTopUpAmount`, beside
`MinimumAmountToRequestFromSequencer`): the effective minimum is the larger of the two,
and an absent setting means the floor. Wallets read it off the sequencer output before
building a request, as they read the fee.

The output is built wallet-side by a new `NewTopUpDelegationRequestOutput` in
`ledger/txbuildercore/helpers_seq.go`, beside `NewSequencerRequestOutput` and
`NewEnsureStopDelegationConstraint`; `sequencer/txbuilder_seq` gets the matching
`NewTopUpDelegationReqOutput` for tests, as askstop has.

## 3. Ledger changes

Three pieces, all in the constraint layer. The rule of the repository applies: the
covenant enforces, the builders follow, no Go assert duplicates a constraint.

### 3.1 `ensureTopUpDelegation(delegationID)` — new, in `ledger/def/ensure.easyfl`

Modelled on `ensureStopDelegation`. Produced arm: chain ID is 24 bytes. Consumed arm,
unlocked with one byte naming the produced delegation successor:

- the successor's chain constraint names `delegationID`;
- the successor is **marked frozen** (delegateLockState mark 1);
- the successor's balance is at least the predecessor's balance plus this output's own
  balance (`selfTokenBalanceValue`), the predecessor found through the successor's chain
  constraint as `_stopDelegationPredecessorIndex` does today. Inflation and the advance
  only add to it, so "at least" is enough here; the delegate lock pins the exact figure.

**When the check applies.** Only while the target can consume the output, i.e. while
`selfInputSlotPace < constTagAlongSlots` (30 slots). After that the tag-along lock
itself hands the output to the sender, and from `constTagAlongReclaimSlots` (390) to
anybody, and the constraint steps aside. This differs from `ensureStopDelegation`
today, which steps aside only at 390 slots: in the sender's exclusive window, 30 to
390, its consumed arm still demands a produced on-hold delegation, so the sender cannot
reclaim an askstop request at all and the output goes public at 390. That is a defect
to fix in the same hardfork: `ensureStopDelegation` gets the same escape at
`constTagAlongSlots`. Section 8 has the consequences for the wallet.

**What this constraint does not check, and why that is enough.** It says nothing about
the inflation advance. The advance is enforced by the delegate lock (3.2, 3.3): on the
referenced path the successor balance must equal the predecessor plus one slot of
inflation plus the top-up plus the advance on the newly frozen amount, at the share
pinned in `delegateLockState`, which on a continuation is byte-equal to the
predecessor's and so is the rate agreed when the freeze began. The sequencer cannot
satisfy this constraint any other way: without the third unlock byte a frozen delegation
may only go on hold, which fails "marked frozen" here, and a fresh freeze must match the
plain exact advance on the predecessor balance alone, which fails once the top-up is
added. So consuming the request forces the referenced path, and that path prepays the
advance. Checking the advance here as well would duplicate the delegate lock.

### 3.2 `delegateLock`, target path — `ledger/def/lock_delegate.easyfl`

A third unlock byte on the target path today means "allowance": it names a consumed
output whose element 4 is an `ensureStopDelegation`. It now names a consumed output
whose element 4 is **either** an `ensureStopDelegation` or an `ensureTopUpDelegation`;
the prefix decides which rule applies. The top-up amount is the token balance of the
referenced output; on the plain 2-byte path it is 0.

Helpers, consumed context:

- `_topUpRef`: the referenced constraint, `consumedConstraintByIndex(byte 2, 4)`;
- `_hasTopUp`: 3-byte unlock and the prefix is `#ensureTopUpDelegation`;
- `_topUpAmount`: `tokenBalanceByOutputPath` of the referenced consumed output if
  `_hasTopUp`, else `u64/0`;
- the referenced output must carry `tagAlong` and its sender must equal the master, the
  same two requirements the allowance path already makes, and the constraint's chain ID
  must equal `_selfChainID`.

What changes in `_requireUnlockableByTheTarget`: when the consumed delegation is frozen
in this transaction, today the successor must be on hold. With a top-up reference the
successor must instead **stay frozen with the same delegateLockState** as the
predecessor: same last frozen epoch, same advance share. Pin it as byte equality of the
state constraint on both sides. Everything else in that function stays: on hold refuses
the target, the safe revocation window refuses the target, the successor is at least
one slot later.

What changes in `_validAmountOnSuccessor`: with a top-up reference the amount rule is
exact rather than "not decreasing":

```
successorBalance == predecessorBalance + successorInflation + topUp + advance(newlyFrozen)
```

where `successorInflation` is the successor's inflation amount, and `newlyFrozen` is
what this transaction freezes for the first time:

- **continuation** (predecessor frozen in tx): `newlyFrozen = topUp`, the advance is
  computed over the remaining frozen span, `_txFrozenSlots`, at the pinned share;
- **fresh freeze** (predecessor undef, or frozen but past its window and past the safe
  revocation window): `newlyFrozen = predecessorBalance + topUp`, over the new span at
  the share the successor pins.

Both are `requiredInflationAdvance(frozenSlots, txSlot, newlyFrozen, share)`, which
exists. The exactness is the same principle as today's freeze: the advance is a
prepayment, and an early stop unwinds the unearned part of it from the same numbers.

### 3.3 `_validInflationAdvanceProduced`, produced context

The successor's own check computes the advance on `predecessorTokenBalance` over
`_txFrozenSlots` and requires the balance difference to equal it exactly. It has to see
the top-up too, or every top-up fails on the produced side. Read the predecessor's lock
unlock parameters with `unlockParamsByConstraintIndex` at `selfChainPredInputIndex`;
if they are 3 bytes and reference an `ensureTopUpDelegation`, take `topUp` as the
balance of the referenced output and apply the rule of 3.2 with the same two cases, distinguishing continuation from fresh
freeze by the predecessor's state mark and last frozen epoch against `txSlot`. One
formula, evaluated on both sides from the same inputs, as the freeze does today.

`_validLimitsProducedFrozen` needs nothing new: a continuation keeps the last frozen
epoch, which is not in the past, and the count of frozen epochs does not grow.

### 3.4 Frozen coverage, `ledger/lock_delegate.go`

`evalEnforceFrozenCoverageOnDelegateOutput` already computes the expected vector from
the successor's own balance and frozen epochs, so a continuation successor with a
larger balance passes as it stands. It requires the predecessor's unlock parameters to
be at least 2 bytes, so the 3-byte form passes. The sequencer chain's vector is checked
by `evalEnforceFrozenCoverageOnNonDelegationChain` as deltas between consumed and
produced delegations in the transaction; verify it takes the difference rather than
assuming a consumed delegation had a zero vector, and add a test for the continuation.

### 3.5 What is not changed

The master path is untouched. On hold and the safe revocation window stay the master's:
the target cannot top up a delegation there, and the sequencer refuses the request
until the state allows it. The delegation lock, the index values and the constraints
between chain and state are still pinned across the transit (`_targetPreservesTheRest`).
The advance share stays pinned on a continuation, so a sequencer that has since raised
its cut still pays the share it agreed to, or refuses the request.

## 4. Sequencer

New `sequencer/txbuilder_seq/req_topup.go`, registered in `_cmdParsers`. Shape follows
`req_askstop.go`.

**Parse**, refusing permanently (blacklist) or temporarily (retry) as noted:

1. layout is the five elements above, `ensureTopUpDelegation` at index 4 naming the
   same delegation as the request data; else permanent;
2. the delegation output is fetched from the baseline state by chain ID; unknown chain,
   or not a delegation, or its target is not this sequencer: permanent;
3. the sender is the delegation's master: else permanent;
4. the request's balance is at least the effective minimum top-up (the larger of
   `MinTopUp` in the sequencer's data and the 100 PROX floor); else permanent. The
   minimum-fee gate that every other tag-along passes does not apply to this request:
   there is no fee;
5. the delegation is unlockable by the target in this slot (`IsUnlockableByTarget`:
   not on hold, not in the safe revocation window, at least one slot old); else
   temporary, the request waits in the backlog. A temporary refusal can only be
   retried within the 30-slot tag-along window; after it the parser reports "missed
   tag-along window" and the output belongs to the sender (section 8);
6. the advance is affordable and not loss-making: continuation uses the pinned share,
   which by construction is at least the delegator's cut, but the sequencer may refuse
   if the pinned share is above its current tolerance; fresh freeze uses
   `advanceShare()` as `FreezeDelegation` does; insufficient chain balance is temporary.

**Apply**:

- consume the request output, chain-lock unlock as every tag-along; `amount` is its
  balance;
- consume the delegation; produce the successor: continuation keeps last frozen epoch
  and share, balance `pred + inflation + amount + advance(amount, remaining span)`;
  fresh freeze goes through the epoch placement of `selectDelegationsToFreeze` with
  `pred + amount` as the amount to place, so the per-epoch cap and the load balancing
  apply to it; balance `pred + inflation + amount + advance(pred + amount, new span)`;
- unlock the delegation as target with the third byte pointing at the request output;
  unlock element 4 of the request with the successor index, as askstop does;
- chain balance: `- advance` (the request balance passes straight through to the
  delegation); frozen coverage vector:
  add the successor's vector less the predecessor's (zero on a fresh freeze).

`AttachmentCostDelta` is 3, as askstop. One top-up per delegation per milestone: a
delegation is consumed once, further requests for the same chain wait.

**Ordering with the freeze pass.** `insertTagAlongInputs` runs before
`insertDelegations`. A top-up on an unfrozen delegation freezes it inside the tag-along
pass; the freeze pass must skip a delegation already consumed in the proposal, which
the pool snapshot and `IsConsumedInThePastPath` should already give. Verify.

**Delegation pool.** `milestoneTransitions` records any produced frozen delegation as a
freeze with its new balance and epoch, so a continuation is recorded as a freeze that
replaces the entry's amount. Verify the per-epoch load in `Snapshot` reads the entry's
amount rather than accumulating, so the load reflects the new balance once.

**Refusals visible to the wallet.** Refused requests are reported through the existing
tag-along warning topic; the wallet learns of a permanent refusal only by the request
being reclaimable after `constTagAlongReclaimSlots`. The consolidator already handles
that for askstop and needs nothing new.

## 5. Wallet: `proxi`

Rule for adding `D` to a delegation, replacing the state-dependent choice of
`delegation_add_tokens.md` section 3:

- the master can consume it now (on hold, undef, or inside the safe revocation window):
  master-side add through `delegate chain --add`, fee only, immediate;
- otherwise: the top-up request, carrying the amount and nothing else; it must reach
  the target's minimum top-up. The sequencer has 30 slots to take it; after that the
  wallet reclaims it (section 8).

Before sending, the wallet checks what the sequencer will check, so that a request is
not sent into a predictable refusal: the target is active, the amount clears its
minimum top-up, the delegation is frozen or undef and not in its safe revocation window
for the next 30 slots, the target's present tolerance covers the pinned share. Every
refused request is an output the wallet has to get back.

Askstop is no longer part of placing tokens. It stays for the two things only the
master can want: folding a delegation into another and moving one to a different
target.

Changes:

- `proxi/node_cmd/delegate/topup.go`: builds the request for a frozen delegation
  instead of askstop; keeps `delegate chain --add` for the consumable case; picks the
  smallest delegation by default as now, frozen or not;
- `proxi/node_cmd/consolidate/delegate.go`: `pickPlacement` no longer needs
  "consumable" for steps 1 and 3; a frozen delegation is topped up by request. Step 4
  (askstop) is deleted from placement. `manageDelegations` keeps askstop for fold and
  retarget;
- `ledger/txbuildercore`: `NewTopUpDelegationRequestOutput`, a parser for the request
  for display, the `ensureTopUpDelegation` constraint builder; `helpers_delegate.go`
  gets `AdvanceForAmount(...)` if the wallet wants to show what the sequencer will pay;
- `kb/consolidate.md`: the placement rules and the "delegation mode" section;
  `proxi/node_cmd/consolidate/delegate.go` header comment.

## 6. Miner

`proxi node mine` places its payouts through the consolidator; the treasury loop
(`mine_treasury.go`, `mine_topup.go`, `mine_treasury_test.go`) is retired, which
`kb/consolidate.md` already schedules. Nothing in the miner knows about the request.
With pace 1 the consolidator's `threshold_prox` and `compact_at` defaults are lowered
so that ~125 PROX payouts are swept at a sensible cadence; the top-up request is what
makes sweeping into a frozen delegation cheap enough to do often.

## 7. Economics

- **Delegator pays** nothing: no fee, no unwind, no idle slots, no storage deposit
  beyond the request output, which is consumed.
- **Sequencer pays** the advance on the added amount over the remaining frozen span at
  the pinned share, exactly what it would pay if that amount had been part of the
  delegation at freeze time. On a fresh freeze it pays the ordinary advance on the
  whole. It gains the frozen coverage of the added amount for the rest of the span,
  and its minimum top-up keeps the per-request cost worth that gain.
- **Nothing to game.** The amount goes from the master's signed output into the
  master's delegation; the sequencer cannot divert it (`ensureTopUpDelegation`) and
  cannot underpay the advance (exact amount rule). The delegator cannot use the request
  to shorten or lengthen a freeze, or change the share.

## 8. Edge cases

- **Delegation in the safe revocation window** when the request arrives: the target
  may not touch it; the request waits. After the window the delegation is unfrozen and
  the sequencer freezes it with the top-up as a fresh freeze. If the master meanwhile
  consumed it (re-delegated, folded), the request finds the chain's latest output: undef
  → fresh freeze with top-up; on hold → wait; chain gone → permanent refusal, reclaimed.
- **Request not taken within 30 slots.** The output belongs to the sender for the next
  360 slots, then to anybody. See the reclaim rule below.
- **Two requests for one delegation in one slot.** The first consumes the delegation;
  the second finds the delegation output gone from the baseline and waits for the next
  milestone, where it is a continuation on the new output.
- **Per-epoch cap on a continuation.** The epoch is fixed by the predecessor; the
  count of frozen delegations in it does not change, the amount does. No cap applies
  by count; the coverage upper bound (`enforceFreezeUpperBound`) does and refuses
  temporarily.
- **Pinned share above the sequencer's present tolerance.** The sequencer may refuse;
  the request is reclaimed and the consolidator's retarget rule moves the delegation
  elsewhere in time. Reporting it as permanent is right: it will not change within the
  window.

## 8a. Reclaim: the rule for automated processes

A tag-along output that is neither consumed by its target nor reclaimed by its sender
becomes **claimable by anybody** at `constTagAlongReclaimSlots`, 390 slots, about an
hour after it was sent. Every tag-along proxi emits is exposed to this; a top-up request
is the largest one a wallet ever emits, at least 100 PROX with no fee to soften it, so
the rule is not optional:

- **the consolidator reclaims every reclaimable tag-along of the wallet, first thing
  every tick**, including request outputs. It already sweeps plain tag-alongs past
  `tag_along_slots`; request outputs are excluded today because
  `ClassifySpendable` (`ledger/txbuildercore/spendable_classify.go`) marks any output
  with an element beyond the lock as `SpendUnknown`. The classifier learns the request
  shape: a tag-along whose element 3 is inline data and whose element 4, if present, is
  an `ensureStopDelegation` or `ensureTopUpDelegation`, is `SpendSimple` for the sender
  once `tagAlongSlots` have passed. With the escape of 3.1 the ledger lets the sender
  spend it then;
- the reclaim goes back to the wallet as a plain sigLock output and is placed again on
  a later tick, so a refused top-up costs one round trip and nothing else;
- a wallet that sends requests and is then switched off is at risk after an hour. The
  consolidator is a permanent process, which is the point; the manual
  `proxi node delegate topup` prints the deadline and tells the operator to run
  `proxi node compact` if the request is not taken;
- the same rule already covers askstop requests once `ensureStopDelegation` gets the
  same escape; until then they are reclaimable only after 390 slots, in a race.

The 10-second tick against a 360-slot exclusive window leaves ample margin; what
matters is that the process runs.

## 9. Tests

- `ledger/tests/delegate_test.go`: continuation top-up validates; fresh-freeze top-up
  validates; wrong balance (underpaid advance, diverted amount) rejected; state change
  on continuation rejected; top-up in the safe window rejected; top-up on an on-hold
  delegation rejected; 3-byte unlock pointing at a non-ensure output rejected; sender
  not the master rejected; `ensureTopUpDelegation` skipped after the reclaim window.
- `tests/txbuilder_seq_test.go`: the request parsed and applied, frozen coverage
  vectors on the sequencer chain correct on continuation and fresh freeze.
- consolidator unit tests: placement picks a frozen delegation for top-up; askstop
  never chosen for placement; an amount under the target's minimum top-up is held
  until it grows, not sent.
- request below the minimum refused permanently by the sequencer parser.
- reclaim: the sender spends a top-up request (and an askstop request) at slot 30
  and later; a stranger cannot before 390; `ClassifySpendable` reports a request
  output as `SpendSimple` for the sender past `tagAlongSlots` and `SpendNotForAccount`
  for anybody else.
- an end-to-end run on a local standalone node: create, top up while frozen, observe
  balance and epoch unchanged apart from the amount, top up again after the window.

## 10. Implementation map

| Where | What |
|-------|------|
| `ledger/def/ensure.easyfl` | `ensureTopUpDelegation`; both ensure constraints step aside at `constTagAlongSlots` |
| `ledger/txbuildercore/spendable_classify.go` | request outputs `SpendSimple` for the sender past the window |
| `ledger/def/lock_delegate.easyfl` | top-up reference on the target path; exact amount rule; state pinned on continuation; produced-side advance check reads the top-up |
| `ledger/ensure.go` | Go type `EnsureTopUpDelegation`, registration |
| `ledger/lock_delegate_util.go` | `MakeDelegationTopUpOutput` (continuation), `MakeDelegationFreezeOutput` taking an added amount |
| `ledger/lock_delegate.go` | verify the frozen coverage checks on continuation |
| `sequencer/txbuilder_seq/req_topup.go`, `parse.go` | request code 4, parser, `Apply`, `MinimumTopUpAmount` |
| `sequencer/seqdata/seqdata.go`, `proxi/node_cmd/seq_cmd` | `MinTopUp` setting, `set-params --min-topup`, shown by `seq info` |
| `sequencer/task/proposal.go` | freeze pass skips delegations consumed by a top-up |
| `sequencer/delegationpool` | verify load accounting on continuation |
| `ledger/txbuildercore/helpers_seq.go` | request output and constraint builders, parser |
| `proxi/node_cmd/delegate/topup.go` | request path for frozen delegations |
| `proxi/node_cmd/consolidate/delegate.go` | placement without askstop |
| `proxi/node_cmd/mine*.go` | treasury loop retired |
| `kb/consolidate.md`, `ARCHITECTURE.md` index, `CLAUDE.md` kb index | documentation |

## 11. Decisions

Settled 2026-09-22, folded into the sections above:

- **No fee.** The request balance is the amount; the sequencer is paid in frozen
  coverage (section 2).
- **Minimum top-up** enforced by the sequencer: `MinTopUp` in its data, hard floor
  100 PROX (section 2).
- **Share on continuation is pinned.** The successor keeps the share agreed when the
  freeze began; a sequencer that has since raised its cut pays that share or refuses
  the request (sections 3.2, 3.5).
- **Fresh freeze happens in the request's own transaction**, through the epoch
  placement of the freeze pass, not in a later milestone (section 4).
- **Reclaim is the wallet's duty.** Ensure constraints step aside once the target's
  window closes, the classifier recognises request outputs, and the consolidator
  reclaims them every tick (sections 3.1, 8a).
