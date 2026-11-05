# Library upgrade 6: a weak mining key is never a safe way to mine

> SPEC, not built (draft written 2026-11-05, revised the same day after review). Sixth live library
> upgrade, no reset. Activation slot and release not chosen. Prerequisite: upgrade 5 active (slot
> 153000). Mechanism: `ledger/upgrade.md`. Touches the mine lock (`ledger/def/lock_mine.easyfl`) and
> the top-up guarantor (`ledger/def/ensure.easyfl`); the reward layouts it changes are in
> `kb/mine_payout_options.md`.

## The problem

The mine chain's proof of work is the trailing-zero count of an ECVRF output under the miner's key
(`kb/mine_vrf_pow.md`). Each attempt costs a scalar multiplication `Gamma = x*H`, where `x` is the
secret scalar. A miner who picks `x = 1` skips that multiplication, since `Gamma = H`, and miners
report about five times the hashrate. Any small `x` gives most of the gain (`x < 2^k` costs about
`k` doublings).

The key is then public, so any output it controls can be spent by anyone. That ought to make a weak
key self-defeating, but the payouts leave ways out. The one in use: option 3 is one tag-along of A
(95 PROX) from the signer to any sequencer, with any request payload or none. A tag-along with no
request is simply consumed by its target into the sequencer's own chain. So a miner who sends option
3 to their own sequencer never holds anything under the weak key. The sender can only reclaim the
tag-along from `constTagAlongSlots` on, long after the sequencer has taken it, so the race with a
thief is never run.

Consequences:

- The proof of work no longer prices hashrate fairly between careful miners and weak-key miners.
- The only safe exit for a weak-key miner is their own sequencer. That pushes the network towards
  one sequencer per miner (68 sequencers on 2026-11-05, rising). The network handles that number, but
  the trend is driven by an exploit rather than by sequencing being worth doing.

## The goal, and what the upgrade cannot do

A weak key cannot be refused directly. Nothing in the public key reveals that its scalar is small,
and a list of banned keys could never be complete. Nor can the attempt be made to cost the same:
scalar 1 stays five times faster.

The goal is narrower: **a weak key must never be a safe way to mine.** Everything a transit mints
lands in an output whose only spending condition is the mining key, so with a known key every reward
is a race open to everyone. How well that deters weak keys depends on others actually racing for
them; it does not make weak-key mining impossible.

What this does to the other two concerns:

- **Fewer miner-run sequencers:** it removes the weak-key reason to run one. A strong-key miner
  running a sequencer for its earnings is legitimate and stays possible.
- **Pooling:** not affected either way. The VRF input does not commit to where the reward goes, so
  a worker who finds a nonce can claim the reward, weak key or not.

## Three ways a weak key stays safe today

"Lands under the mining key" means more than "the signer is named somewhere". Each of the following
leaves the reward safe from anyone else holding the key.

**1. A tag-along consumed into the sequencer's own chain.** Described above: option 3 with no
request.

**2. A fake delegation behind the top-up guarantor.** Making option 3 a top-up request is not enough
by itself. The delegate lock binds a top-up to the master: `_topUpAuthorised` in
`ledger/def/lock_delegate.easyfl` refuses a top-up unless the referenced output is a tag-along whose
sender (index value 0) is the delegation's master, and the tag-along lock makes the sender the
signer. But `_ensureTopUpDelegation` in `ledger/def/ensure.easyfl` does not make the top-up go
through the delegate lock. It checks that the successor carries the named chain ID, that its last
element is a `delegateLockState` marked frozen, and that the predecessor's lock was unlocked with
three bytes, the third naming the request. It never checks that the lock is `delegateLock`, and the
other two marks need none:

- `delegateLockState` is valid on any output: in the produced context it checks only its arguments
  and that it sits last.
- `sigLock` ignores its unlock parameters when consumed. Three bytes pass as long as the signer
  matches.

So a sequencer controlled by key S takes a top-up request into a chain C locked by `sigLock(S)` with
a trailing `delegateLockState(_, 1, _)`, the request naming C. The guarantor is satisfied, the
delegate lock never runs, and the reward ends under S. Today this harms no one, since only the
request's own sender can name C. Under this upgrade it would be the way around it.

**3. A script on the payout output.** `callRedeemer(<hash>, <fn>, ...)` can sit as an extra element
on any output (the DEX orders use it so, `ledger/def/lock_dex_orders.easyfl`). Its script can require
that a spend on the owner's path pays the balance to a key S. Anyone holding the weak key can still
sign, but only S is paid. It fits wherever the reward lands:

- **option 1:** `_mineOpening` does not fix the element count of the opening sigLock, so a script
  on it protects the opening outright, with no sequencer involved;
- **option 2 and the delegation option 3 tops up:** the delegate lock allows any elements between
  the chain constraint and the state (`_validStructureProduced` checks only the state), and the
  target must preserve them (`_targetPreservesTheRest`). A stock sequencer cannot serve such a
  delegation, since its transactions would have to commit the script (`redeemScript`); the miner's
  own sequencer can;
- **option 3, element 3 of the tag-along**, the request data, unchecked today. A transit enters the
  DAG without its tag-along being consumed when the next transit consumes its mine output. After
  `constTagAlongSlots` the sender reclaims the tag-along, and a script there forces that payment to S.

## The ledger rules

One principle: **every output a transit produces is pinned element by element, and a top-up lands
only in a plain delegation.** It is the rule upgrade 5 applied to the mine successor. Every rule
applies from the activation slot on, to outputs made before it as well. Each layout is exactly what
`proxi node mine` and the wallet builders produce today (`NewSigLockOutput`, `NewTagAlongOutput`,
`NewDelegationInitOutput`, `NewSequencerRequestOutput`), so honest miners and wallets are unaffected.

### The payouts

`_mineOpening`, `_mineNewDelegation`, `_mineFull` and `_mineInclusionFee` are replaced, same arity:
as today, plus an exact element count, and for option 3 the shape of elements 3 and 4. The tag-along
lock already makes the sender the signer on every tag-along (`ledger/def/lock_tag_along.easyfl`), so
`_mineFull` drops its own copy of that check.

| Output | Elements, exactly |
|--------|-------------------|
| option 1, opening | 3: amounts, index values, `sigLock` |
| option 2, new delegation | 5: amounts, index values, `delegateLock`, chain, `delegateLockState` |
| option 3, top-up request | 5: amounts, index values, `tagAlong`, request data (inline data), `ensureTopUpDelegation` |
| inclusion fee (options 1, 2) | 3: amounts, index values, `tagAlong` |

```
// Replaced: as in genesis, and the output is exactly the opening sigLock, three elements.
func _mineOpening :
and(
   require(equalUint(numProducedOutputs, u64/3), !!!mine_opening_payout_needs_three_outputs),
   require(equalUint(tupleLenAtPath(concat(pathToProducedOutputs, 1)), u64/3), !!!mine_opening_payout_must_have_three_elements),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #sigLock), !!!mine_opening_payout_must_be_sig_locked),
   require(equal(_mineOutIndexValue(1, 0), txHolderID(txSignatureData)), !!!mine_payout_must_go_to_the_transaction_signer),
   require(equalUint(_mineOutAmount(1), _prox(constMineOpeningAmountProx)), !!!mine_opening_amount_must_be_the_opening_amount),
   _mineInclusionFee(2)
)

// Replaced: as in genesis, and the delegation is plain: five elements, nothing
// between the chain constraint and the state.
func _mineNewDelegation :
and(
   require(equalUint(numProducedOutputs, u64/3), !!!mine_delegation_payout_needs_three_outputs),
   require(equalUint(tupleLenAtPath(concat(pathToProducedOutputs, 1)), u64/5), !!!mine_delegation_payout_must_be_a_plain_delegation),
   ... as in genesis ...
)

// Replaced: the full reward is a top-up request: five elements, the request data
// inline data and element 4 an ensureTopUpDelegation. The guarantor takes it only
// into a plain delegation whose master is the sender, and the tag-along lock makes
// the sender the signer.
func _mineFull :
and(
   require(equalUint(numProducedOutputs, u64/2), !!!mine_full_payout_needs_two_outputs),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #tagAlong), !!!mine_full_payout_must_be_a_tag_along),
   require(equalUint(_mineOutAmount(1), _mineAmount), !!!mine_full_payout_must_be_A),
   require(
      and(
         equalUint(tupleLenAtPath(concat(pathToProducedOutputs, 1)), u64/5),
         parseInlineData(atPath(concat(pathToProducedOutputs, 1, 3))),
         equal(parseBytecode(atPath(concat(pathToProducedOutputs, 1, 4)), 0x), #ensureTopUpDelegation)
      ),
      !!!mine_full_payout_must_be_a_top-up_request
   )
)

// Replaced: as in genesis, and the fee is a plain tag-along, three elements.
// $0 output index of the inclusion fee
func _mineInclusionFee :
and(
   require(equalUint(tupleLenAtPath(concat(pathToProducedOutputs, $0)), u64/3), !!!mine_inclusion_fee_must_be_a_plain_tag_along),
   ... as in genesis ...
)
```

The element count comes first in each `and` and is required: `atPath` on an element that is not
there panics, and the rule must refuse such an output, not crash on it. `and` evaluates its
arguments in order and stops at the first false one (`evalAnd` in easyfl), so the later elements are
read only once they are known to exist. `parseInlineData` refuses anything that is not inline data
(`EvalParseInlineData` in easyfl), so element 3 can carry no executable constraint. The request code
inside it is not checked: a wrong code is a request no sequencer takes, which harms only the miner.
Element 3 is pinned only so that it can carry no script; the 5-element count matches the tag-along
lock's own limit (`selfNumConstraints < 6`).

The fee is pinned for consistency rather than value: it is 1 PROX, and the opening and the
delegation carry the reward.

### The guarantor

`_ensureTopUpDelegation` is replaced, same arity: as before, and the predecessor of the successor it
checks is a plain delegation, locked by `delegateLock`, five elements. Its delegate lock then runs on
the target path (three unlock bytes rule out the master path), and there `_topUpAuthorised` requires
the sender to be the master. The delegate lock pins its own lock on the successor byte for byte, and
`_targetPreservesTheRest` pins the elements between, so the successor is a plain delegation too.

```
// Replaced: as in genesis, and the predecessor is a plain delegation: locked by
// delegateLock, five elements. The frozen mark and the 3-byte unlock alone are
// satisfied by any chain carrying a delegateLockState and a lock that ignores its
// unlock parameters (sigLock), and an extra element could carry a script that
// decides where the delegation goes when its master spends it.
// $0 delegation chain ID
func _ensureTopUpDelegation :
and(
  require(
     equal(parseInlineDataArgument(_topUpSuccessorChainConstraint, 0, #chain), $0),
     !!!ensureTopUpDelegation:_delegationID_is_wrong
  ),
  require(
     and(
        equal(parseBytecode(consumedConstraintByIndex(_topUpPredecessorIndex, lockConstraintIndex), 0x), #delegateLock),
        equalUint(tupleLenAtPath(concat(pathToConsumedOutputs, _topUpPredecessorIndex)), u64/5)
     ),
     !!!ensureTopUpDelegation:_predecessor_must_be_a_plain_delegation
  ),
  require(
     equal(parseInlineDataArgument(_topUpSuccessorState, 1, #delegateLockState), 1),
     !!!ensureTopUpDelegation:_delegation_successor_is_not_frozen
  ),
  require(
     and(
        equal(len(_topUpPredecessorLockUnlock), u64/3),
        equal(byte(_topUpPredecessorLockUnlock, 2), selfOutputIndex)
     ),
     !!!ensureTopUpDelegation:_delegation_must_be_unlocked_with_a_reference_to_the_request
  )
)
```

The plain-delegation rule applies to every top-up, not only mined ones: one rule rather than two
forms of the guarantor. A delegation carrying extra elements, a delegated foundry for instance, can
no longer be topped up; its master re-creates it instead. Every top-up wallets and sequencers make
today names a plain delegation.

Checking the master in the guarantor (predecessor index value 0 equal to the request's sender) was
considered instead of the lock type. It is weaker: whether index value 0 is the owner depends on the
lock, so the check would have to be argued afresh for every lock. Checking the lock type hands the
question to the delegate lock, which already answers it.

## Why it closes the gap

After the upgrade every reward lands in an output whose only spending condition is the mining key:

| Option | Lands as | Who can take it, given the key |
|--------|----------|--------------------------------|
| 1 | a plain sigLock to the signer | anyone, at once |
| 2 | a plain delegation, master the signer | anyone, while it is not frozen and in each safe revocation window |
| 3 | a top-up of a plain delegation whose master is the signer, or the reclaimed tag-along | the same; the tag-along by anyone from `constTagAlongSlots` on |

A frozen delegation is not out of reach of the key for long. The target can unlock a frozen
delegation only to put it on hold or to continue a freeze with a top-up. It cannot unlock it in the
`constDelegationSafeRevocationSlots` (60) slots after a freeze ends, and there the master path is
open (`_requireUnlockableByTheTarget`, `_masterUnlockedConsumed`). An askstop allowance cannot drain
it either: `ensureStopDelegation` caps the allowance at the projected compensation, the unearned
advance, never the principal.

The result is **contestable, not confiscated**. A weak-key miner who runs their own sequencer can
still try to be first: send an askstop under the key, have their sequencer put the delegation on
hold, and spend it on the master path straight away. Anyone else holding the key can send the same
askstop to the same target, or spend in the revocation window, and the conflicting spends are
settled by coverage. Each reward becomes a race open to everyone, repeated for every transit,
instead of a path nobody else can enter.

## Considered and dropped

**A master argument on `ensureTopUpDelegation`.** A second form with one 56-byte argument, the
delegation ID followed by a master holder ID, told apart by length, so the mine lock could require
the master to be the signer. Not needed: once the guarantor requires a plain `delegateLock`,
`_topUpAuthorised` makes sender equal master and the tag-along lock makes sender equal signer. It
would also have cost more: `_topUpAuthorised` compares the whole argument with the delegation ID, so
the delegate lock, the Go parser (`ledger/ensure.go`) and the sequencer's request parser would all
have had to change.

**A strict form of the guarantor for mined top-ups only**, selected by argument length, leaving
today's form free to top up delegations with extra elements. Rejected for one rule over two.

## Node, miner and wallet

- **`proxi node mine`**: the mode that sends the full reward to the own sequencer with no request
  (`SendOwn` with `SendTo`, `proxi/node_cmd/mine/mine_payout.go`) is valid only before the
  activation slot. From the slot on, option 3 is a top-up of an own delegation. The miner already
  builds that (`choosePayout`), and already reads the rules of the slot it builds for
  (`kb/library_upgrade1.md`). An operator running their own sequencer keeps a delegation to it,
  opened with option 2, and tops that up.
- **`mine_verify.go`**: the check of a foreign transit follows the slot's rules.
- **Wallet**: top-ups from `proxi node delegate topup` (`proxi/node_cmd/delegate/topup.go`) and the
  consolidator (`consolidator/delegate.go`) skip a delegation with extra elements from the slot on;
  the command says to re-create it.
- **Sequencer**: no change. Transits with top-up requests are taken today. A sequencer whose
  `MinTopUp` is above A (95 PROX) cannot receive option 3 rewards. The floor is 90.
- **Miner version**: `constMinerVersion` redefined to 6 in `upgrade6.easyfl`, as upgrade 1 did with
  5, together with the miner's own version, so a miner from an earlier release refuses to run past
  the slot.
- **Docs site**: the mining page's description of the three payouts, and the advice for miners who
  run a sequencer.

## Tests

In `./ledger/...`, each case before and after the activation slot:

- option 3 with no request: valid before, refused after;
- option 3 as built by the miner (request data, `ensureTopUpDelegation` at element 4): valid on both
  sides;
- option 3 with the guarantee missing or at another index, or element 3 not inline data: refused
  after;
- option 1 and option 2 as built by the miner: valid on both sides; with an extra element: valid
  before, refused after;
- the inclusion fee with an extra element: valid before, refused after;
- a top-up into a delegation whose master is not the sender: refused by the delegate lock on both
  sides (pins the property the upgrade relies on);
- a top-up taken into a chain locked by `sigLock` with a trailing frozen `delegateLockState`: valid
  before, refused after;
- a top-up of a delegation with an extra element: valid before, refused after;
- an ordinary top-up of a plain delegation: valid on both sides.

Then a local rehearsal with the miner switching layouts across the slot.

## Open

- **Measure first**: before choosing a slot, count option 3 transits with no request and their
  targets over a few thousand slots, and list delegations with extra elements. That tells how many
  miners, sequencers and delegators depend on what the upgrade removes.
- Whether a weak-key miner's race through their own sequencer (askstop, then an immediate spend on
  the master path) wins often enough to keep the incentive alive. That can only be measured live.
