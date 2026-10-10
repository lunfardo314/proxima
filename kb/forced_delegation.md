# Forced delegation of dormant capital

> **SPEC, not built** — written 2026-10-10 after the reset testnet showed one
> operator mining with a fresh wallet per restart and never sweeping: in three
> hours 250 payouts, about 23,500 PROX, on eleven abandoned wallets, idle for
> good. Replaces the 2026-05 research draft (now
> `kb/archive/superseded/forced_delegation_v1.md`), which put the rule in a Go
> bypass of lock validation; here the rule lives in the locks themselves. A
> ledger change, so a hardfork: ships with a reset. Read before touching
> `sigLock`, `delegateLock` or the sequencer's delegation pool.

## 1. Why

A branch is healthy only when its coverage reaches `7/12` of the total supply.
Every token counts in the supply from the moment it exists, whether or not
anyone can still move it. A token on a plain sigLock output contributes nothing
to coverage. So dead capital, tokens nobody delegates, or whose keys are lost,
is a permanent hole in the health margin: it raises the denominator and never
the numerator.

In the launch phase that hole is cheap to dig. Mining issues 95 PROX per slot,
about 820,000 PROX a day, for electricity and nothing else, and a token that
cost nothing can be thrown away for nothing. Today honest active capital is
about 60 million PROX, almost all of it the bootstrap capital, and the network
stops producing healthy branches once total supply exceeds `12/7` of that,
when about 43 million PROX are idle or lost. A miner holding 90 percent of the
hashrate reaches that around day 58 of the flat phase by simply never keeping
its keys. Observed on 2026-10-10: one operator at about half the hashrate, so
not there, but the mechanism is real and the attacker needs no tokens to start.

Routing mined payouts straight into delegations (a mine-lock rule) closes the
mining path but not the hole: an attacker who keeps its keys can withdraw
later and idle the tokens then, and so can anyone with any tokens of any
origin. The fix has to work on dead capital wherever it comes from.

**Forced delegation** lets any sequencer take an output that has sat untouched
long enough and wrap it into an ordinary delegation to itself, frozen, with
the original holder as master. The holder loses nothing but the option of
leaving the tokens idle past the dormancy. Lost keys stop being a hole: their
tokens secure the ledger under whichever sequencer got there first, forever.
Hoarded keys stop being an attack: the hole becomes a leak with a known rate
(§7).

## 2. The rule

An output is **dormant** when it has not been consumed for
`constDormantSlots` since it was produced. A dormant output locked by a plain
`sigLock` may be consumed without its holder's signature by a transaction that

- transits a sequencer chain (so it is signed by that sequencer's controller),
- produces, at the index named in the dormant input's unlock parameters, a
  **wrap**: a delegation origin whose master is the dormant output's holder,
  whose target is that sequencer chain, whose amount equals the dormant
  output's amount, whose required cut is at least `constForcedDelegationMinCut`,
  and whose state is frozen,
- is stamped no earlier than the sequencer's **turn** for that output (§4).

A delegation that has sat **unfrozen** for `constDormantSlots` (on hold, or
never refrozen by a target that stopped) may likewise be consumed by any
sequencer on a **takeover** path: the successor keeps the master, moves the
target to the taking sequencer, keeps the amount, and is frozen. The owner's
stop and the safe revocation window are untouched.

Nothing else changes. The wrap is a delegation in every respect from its
origin on: the same `delegateLock`, the same freeze grid, the same `askstop`,
the same index under the master. A sequencer never merges, splits or ends an
output it does not own: one dormant output, one wrap, byte-equal amount.

## 3. Where the rule lives: a second arm in the locks

The research draft skipped the consumed output's lock from Go when a wrap was
present. That is a rule outside the covenant, and it breaks the invariant that
every consumed lock passes. Here the lock itself passes, on its own second arm,
and checks everything that makes the override legitimate. No Go dispatch.

### 3.1 `sigLock`, consumed side

Today the consumed arm is `equal(holderID, txHolderID(txSignatureData))`. It
gains an alternative:

```
// $0 holderID of this output
func _sigLockForced :
and(
   // dormant, and this sequencer's turn has come (§4)
   greaterOrEqualThan(selfInputSlotPace, _forcedTurn($0)),
   // unlock parameters name the wrap: one byte, a produced output index
   equal(len(selfUnlockParameters), u64/1),
   // the wrap is a delegation origin to a chain this transaction transits,
   // with me as master and my amount, at no worse than the floor cut, frozen
   require(equal(parseBytecode(_wrapLock, 0x), #delegateLock), !!!forced_wrap_must_be_a_delegation),
   require(isChainOriginID(parseInlineDataArgument(_wrapChain, 0, #chain)), !!!forced_wrap_must_be_a_delegation_origin),
   require(equal(_wrapIndexValue(0), $0), !!!forced_wrap_master_must_be_the_dormant_holder),
   require(_chainLock(_wrapIndexValue(1)), !!!forced_wrap_target_must_be_transited_by_this_transaction),
   require(equalUint(_wrapAmount, selfTokenBalanceValue), !!!forced_wrap_must_carry_the_dormant_amount),
   require(lessOrEqualThan(uint8Bytes(constForcedDelegationMinCut), uint8Bytes(_wrapCut)), !!!forced_wrap_cut_below_the_floor),
   require(_wrapIsFrozen, !!!forced_wrap_must_be_born_frozen)
)
```

`_wrapLock`, `_wrapChain`, `_wrapIndexValue`, `_wrapAmount`, `_wrapCut` and
`_wrapIsFrozen` read the produced output at
`concat(pathToProducedOutputs, byte(selfUnlockParameters, 0))`, the way
`lock_mine.easyfl` reads its successor. `_chainLock(target)` is the existing
"this transaction transits that chain" check the tag-along lock uses for its
target path, which is what ties the wrap to a sequencer that signed the
transaction. The reference-unlock shortcut (`unlockedByReference`) stays as it
is: it proves consent only for the plain signer path, and a forced input must
never be referenced by another input, which the shortcut's own
same-lock-same-holder check already prevents from mattering, since a
referenced input's holder did not sign either way. To be exact about it, the
forced arm requires the unlock parameters to be one byte, and the reference
unlock requires a one-byte index too, so the two are told apart by the lock
type of the output at that index: a consumed input for the reference, a
produced wrap for the force. The spec pins it: **a forced input's unlock byte
is a produced index, and it may not itself be referenced.**

### 3.2 `delegateLock`, consumed side

The target path today requires the delegation to be consumed by its own
target chain. It gains the takeover alternative, for a delegation whose state
is not frozen (`on hold` or `undef`) and which has sat so for the dormancy:

```
func _delegateLockTakeover :
and(
   not(_selfIsFrozen),
   greaterOrEqualThan(selfInputSlotPace, _forcedTurn(selfIndexValue(0))),
   // the successor on the chain unlock keeps master and amount, moves the
   // target to a chain this transaction transits, and is frozen
   require(equal(successorIndexValue(0), selfIndexValue(0)), !!!takeover_must_keep_the_master),
   require(_chainLock(successorIndexValue(1)), !!!takeover_target_must_be_transited_by_this_transaction),
   require(equalUint(successorAmount, selfTokenBalanceValue), !!!takeover_must_keep_the_amount),
   require(lessOrEqualThan(uint8Bytes(constForcedDelegationMinCut), uint8Bytes(successorCut)), !!!takeover_cut_below_the_floor),
   require(successorIsFrozen, !!!takeover_must_freeze)
)
```

`successorIndexValue`, `successorAmount`, `successorCut` and `successorIsFrozen`
are readers of the successor output on the chain unlock, to be written beside
the existing `successorConstraint`; `_txSequencerChainID` and the `_wrap*`
readers of §3.1 are new as well. Everything else named here exists today.

A takeover is a chain transition, not an origin: the delegation chain keeps
its ID, so the owner's wallet still finds it. The master's own paths are
untouched; a master that asked to stop and is inside its safe window is not
dormant, since the stop itself is a consumption that resets the pace.

### 3.3 Produced side

Nothing new. The wrap and the takeover successor are produced delegation
outputs validated by `delegateLock`'s existing produced arm and by
`delegateLockState`. The one addition is that a delegation may be **born
frozen**: today only a target transition sets `frozen`, and an origin is
`undef`. The produced arm allows `frozen` at origin when the transaction
transits the target chain (the same `_chainLock` check), so a voluntary
delegation cannot fake a freeze.

## 4. Turns: deterministic staggering

The ledger cannot list the active sequencers, so it cannot assign an output to
one of them. It can give every sequencer its own earliest slot per output:

```
// $0 holderID (or master) of the dormant output
// -> the slots this output must have sat for before THIS sequencer may take it
func _forcedTurn :
add(constDormantSlots,
    mod(slice(blake2b(concat(inputIDByIndex(selfOutputIndex), _txSequencerChainID)), 0, 3), constForcedStaggerSlots))
```

Each (output, sequencer) pair gets a delay of 0 to `constForcedStaggerSlots`
on top of the dormancy. The sequencer with the earliest turn usually gets the
output; two sequencers race only when their turns fall within the propagation
delay of each other, and that race is an ordinary conflict the tangle settles.
A sequencer that is down simply misses its turn, and the next one's comes. No
coordination, no list, no state. `_txSequencerChainID` is the chain ID of the
sequencer chain the transaction transits, which the sequencer constraint
already exposes.

## 5. The wrap, from the owner's side

- Indexed under the master, so `proxi node balance` and the delegation
  listings show it like any delegation, marked as forced by sequencer X.
- The master can `askstop` at any time; the stop takes effect at the next
  safe revocation window, as for any frozen delegation. The master then has
  the tokens back on a sigLock output, and the dormancy clock starts again.
- The cut is at least the floor, so being forced is never a worse deal than
  the cheapest voluntary delegation on the market. The sequencer earns the
  rest, as it would on a voluntary delegation.
- After the freeze the delegation unfreezes and the target may refreeze it,
  as with any delegation. If the target does not, dormancy makes it available
  for takeover, so a dead sequencer does not become a new hole.

## 6. Coverage accounting

The grab is a sequencer milestone: it transits the sequencer chain, consumes
the dormant output and produces the wrap frozen. The sequencer's frozen
coverage, carried on its chain output and telescoped into the stem's
`frozen_coverage`, must grow by the wrap's amount in that milestone, exactly
as it does when the sequencer freezes a voluntary delegation. The node-side
accounting (`SequencerFrozenCoverageDelta` in `core/vertex/past_cone.go`)
derives the delta from the delegation outputs the milestone consumes and
produces, so a born-frozen origin is a new case for it: a produced frozen
delegation with no consumed predecessor. That is the one piece of Go this spec
touches, and it has to be verified rather than assumed.

## 7. What the attack becomes

Let D be `constDormantSlots`, W the stop window, F the freeze. An attacker who
withdraws, idles, is grabbed, stops at the window and withdraws again has the
tokens idle for D + W out of every D + W + F.

| D | Idle fraction of a hoard | Hoard needed for a 43 M hole |
|---|---|---|
| 1 day (8,640 slots) | about 1/5 with F = 60 epochs, 4.3 days | about 215 M, more than the supply |
| 1 hour | about 1/100 | out of reach |

The hole is no longer irreparable; it is a leak whose rate D sets. Lost keys
leak nothing at all: their wraps are never stopped.

## 8. Dust, the open question

One wrap per output means an attacker can fragment a hoard into outputs just
above the storage floor, 9.25 PROX, and a sequencer gains little from grabbing
nine PROX at a time. Two mitigations that keep the one-wrap rule: a grab
transaction may carry many wraps, so the cost per output is bytes and not a
transaction; and forced coverage counts in the health rule like any other, so
every sequencer gains from the network's health, not only from the cut. Merging
a holder's outputs into one wrap would solve dust outright but ends outputs
the sequencer does not own, which this spec refuses. Left open; measured on
the testnet before deciding.

## 9. Node side

- **Scan.** A sequencer loop over sigLock outputs older than D, and over
  delegations unfrozen longer than D, ordered by amount, batched into grab
  transactions on the sequencer's own milestones at the outputs' turns. The
  state has no age index; the census already makes a full pass in
  milliseconds per few hundred outputs, and a pass per epoch is enough. This
  is the first consumer of `kb/research/state_scan_paging.md`.
- **Config.** `sequencer.forced_delegation.enabled`, default on once shipped:
  the point is that nobody has to decide to run it. `min_amount` for the
  scan's own threshold, default the storage floor.
- **Metrics.** Forced wraps and takeovers submitted and settled, forced
  coverage held.
- **proxi.** Delegation listings mark forced wraps; `askstop` works unchanged;
  the consolidator treats a forced wrap as it treats any delegation, so a
  wallet that comes back to life can fold them.

## 10. Constants

| Constant | Default | Why |
|---|---|---|
| `constDormantSlots` | 8,640 (one day) | the leak rate of §7, and a day's grace for a holder who is about to delegate |
| `constForcedStaggerSlots` | 600 (one epoch) | long enough that turns rarely collide, short enough that an output is grabbed within an epoch of its dormancy |
| `constForcedDelegationMinCut` | 900 promille to the owner | the default a sequencer leaves delegators today, so forcing is never a worse deal than delegating |
| freeze of a wrap | the normal `constDelegationMaxFrozenEpochs` | no special state |

## 11. What changes where

- `ledger/def/lock_signature.easyfl`: the forced arm and the wrap readers.
- `ledger/def/lock_delegate.easyfl`: the takeover arm, born-frozen origin on
  the produced arm.
- `ledger/def/def_constants0.json`, `ledger/def_constants0.go`,
  `ledger/constants.go`, `ledger/txbuildercore/constants.go`: the three
  constants.
- `core/vertex/past_cone.go`: the born-frozen case of the frozen coverage
  delta (§6).
- `sequencer/`: the scan loop, the grab and takeover builders in
  `txbuilder_seq`, the config, metrics.
- `ledger/txbuildercore`: wrap and takeover builders for tests and for proxi's
  display, the `ClassifyChain` and delegation views marking a forced wrap.
- `proxi`: display only.
- Tests: `ledger/tests`: a dormant output is wrapped by a sequencer and
  refused before dormancy, before the turn, with the wrong master, amount,
  target, cut or state; the owner stops a wrap; a dormant delegation is taken
  over and refused while frozen or inside the owner's window; the reference
  unlock cannot ride on a forced input. `tests/`: a sequencer scans, grabs,
  gets the coverage, the owner recovers.

## 12. Decisions taken and open

Taken: the rule lives in the locks, not in Go; one wrap per output, byte-equal
amount; sigLock only in the first version; forced wraps are ordinary
delegations; staggering by hash, no coordination; takeover of dormant
delegations in the same version, since a dead sequencer is otherwise a new
hole.

Open: dust (§8); whether `chainLock`-locked balances and reclaimable request
outputs are dormant capital too, deferred; the exact born-frozen state values
(`lastFrozenEpoch`, advance share) the wrap carries, to be set with the freeze
arithmetic in `lock_delegate.easyfl` in hand.

## 13. Relation to the draft

The 2026-05 draft (`kb/archive/superseded/forced_delegation_v1.md`) had the
same goal and the same wrap. It differs in three things, each of which this
spec reverses: the override was a Go exception in stage-3 validation, here it
is an arm of the lock; sequencers raced freely, here each has a turn; a
delegation abandoned by its target had no path, here it is taken over.
