# Option A (rejected): VRF work with enforced payouts

> **REJECTED 2026-11-06.** Option A of three for the mine chain; option C was chosen and is
> specified in `kb/library_upgrade6_mining.md` (branch `feat/upgrade6-mining`). Why A was not
> taken: `kb/research/mining-algo/options.md`. Kept for the reasoning, not as a plan.
>
> Was: SPEC, option A of three. Built on `feat/upgrade6` (2026-11-05), never listed in `ledger.Upgrades`: activation slot and
> release not chosen. Sixth live library upgrade, no reset. Prerequisite: upgrade 5 active (slot
> 153000). Mechanism: `ledger/upgrade.md`. The rules are `ledger/def/upgrade6.easyfl`
> (`BuildUpgrade6Library`, `ledger/def_upgrade6.go`); they replace functions of the mine lock, the
> top-up guarantor (`ledger/def/ensure.easyfl`) and one of the delegate lock. The reward layouts it
> changes are in `kb/mine_payout_options.md`.

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

One principle: **every output a transit produces is pinned element by element, and the full reward
goes only into a plain delegation.** It is the rule upgrade 5 applied to the mine successor. The
rules apply from the activation slot on, to outputs made before it as well. Each layout is exactly
what `proxi node mine` and the wallet builders produce today (`NewSigLockOutput`,
`NewTagAlongOutput`, `NewDelegationInitOutput`, `NewSequencerRequestOutput`), so honest miners and
wallets are unaffected. **Top-ups other than a mined reward keep their meaning**: the plain-delegation
rule rides on a strict form of the guarantor that only the mine lock demands. The code is
`ledger/def/upgrade6.easyfl`; what follows is what it does and why.

### The payouts

`_mineOpening`, `_mineNewDelegation`, `_mineFull` and `_mineInclusionFee` are replaced, same arity:
as in genesis, plus an exact element count, and for option 3 the shape of elements 3 and 4.

| Output | Elements, exactly |
|--------|-------------------|
| option 1, opening | 3: amounts, index values, `sigLock` |
| option 2, new delegation | 5: amounts, index values, `delegateLock`, chain, `delegateLockState` |
| option 3, top-up request | 5: amounts, index values, `tagAlong`, request data (inline data), `ensureTopUpDelegation` in the strict form |
| inclusion fee (options 1, 2) | 3: amounts, index values, `tagAlong` |

- The tag-along lock already makes the sender the signer on every tag-along
  (`ledger/def/lock_tag_along.easyfl`), so `_mineFull` drops its own copy of that check.
- The element count comes first in each `and` and is required: `atPath` on an element that is not
  there panics, and the rule must refuse such an output, not crash on it. `and` and `or` evaluate
  their arguments in order and stop at the first that settles them (`evalAnd`, `evalOr` in easyfl).
- Element 3 must be inline data, so it can carry no script. Anything else is refused by
  `parseInlineData` itself ("not an inline data function"), the way the delegate lock uses
  `parseBytecode`'s refusal. The request code inside it is not checked: a wrong code is a request
  no sequencer takes, which harms only the miner.
- The fee is pinned for consistency rather than value: it is 1 PROX, and the opening and the
  delegation carry the reward.

### The guarantor

`ensureTopUpDelegation` takes its one argument in two forms, told apart by length:

- **24 bytes, the delegation ID**: today's form, used by every wallet, the consolidator and, before
  the slot, the miner. It gains one check: the predecessor is locked by `delegateLock`.
- **25 bytes, the ID followed by `0x01`, the strict form**: the same, and the predecessor is plain,
  five elements. Valid only from the slot on (the produced arm of genesis requires 24 bytes). The
  mine lock requires it for the full reward.

Replaced, same arity: `ensureTopUpDelegation` (accepts both forms when produced),
`_ensureTopUpDelegation` (compares the first 24 bytes with the successor's chain ID, then the two
checks) and the delegate lock's `_topUpAuthorised` (compares only the first 24 bytes of the
request's argument with the delegation ID; it compared the whole argument, so a strict request
would have failed there).

**The lock check has no effect on any existing use.** A top-up that honours the request always goes
through the delegate lock's top-up path, whose predecessor is a `delegateLock` output by definition,
a delegation origin included. The check refuses only the fake chain, which no wallet or sequencer
builds, and which also lets a sequencer misroute a user's top-up today.

**The plain check is confined to the strict form** because the 24-byte form reaches delegations that
carry extra elements by design: `proxi node delegate chain` delegates an existing chain and keeps
its extras, a foundry at element 4 and its policy at 5 (`proxi/node_cmd/delegate/chain.go`), and so
does the consolidator's re-delegation. A foundry chain is unique and cannot be re-created plain, so
a universal plain rule would have left a delegated foundry with no top-up at all.

Once the predecessor is a `delegateLock`, its delegate lock runs on the target path (three unlock
bytes rule out the master path), and there `_topUpAuthorised` requires the sender to be the master.
The delegate lock pins its own lock on the successor byte for byte, and `_targetPreservesTheRest`
pins the elements between, so the successor is a delegation of the same shape.

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

**The plain-delegation rule for every top-up.** One rule instead of two forms, but it would end
top-ups of delegated foundry chains, as described above.

**A separately named guarantor for mined top-ups**, `ensureMinedTopUp`, instead of the strict form.
A clearer name, but the tag-along lock allows no sixth element, so it would replace
`ensureTopUpDelegation` at element 4, and the delegate lock recognises a top-up only by the symbol
`#ensureTopUpDelegation` in `_predHasTopUp`, `_refIsTopUp` and `_topUpAuthorised`: all three, and
every Go parser of requests, would have had to accept both. A guarantor covering all three options
was considered too: options 1 and 2 are fully visible to the mine lock, so only option 3 needs one.

**A master argument on `ensureTopUpDelegation`.** A 56-byte form, the delegation ID followed by a
master holder ID, so the mine lock could require the master to be the signer. Not needed: once the
predecessor is a `delegateLock`, `_topUpAuthorised` makes sender equal master and the tag-along lock
makes sender equal signer.

## Node, miner and wallet

Built on `feat/upgrade6`:

- **Library**: `ledger/def/upgrade6.easyfl`, `BuildUpgrade6Library`. Not in `ledger.Upgrades` until
  the slot is chosen.
- **`ledger.EnsureTopUpDelegation`** carries `Strict`; the parser and `Source` handle both forms.
  `txbuildercore.NewEnsureStrictTopUpDelegationConstraint` builds the strict one.
- **Miner version 6** (`constMinerVersion` redefined in `upgrade6.easyfl`, `mine.MinerVersion`,
  `minerVersionStrictTopUp` in `proxi/node_cmd/mine/mine_upgrade.go`). A transit takes the rules of
  its own slot, as since upgrade 1: before the slot a top-up reward carries the 24-byte form, from it
  the strict form. A miner of an earlier release stops at the slot.
- **`proxi node mine`**: from version 6 an own sequencer (`SendOwn` with `SendTo`) is no longer paid
  by a plain tag-along; it becomes the pinned target of the reward's placement, so the rewards go
  into own delegations at it, opened with option 2 and topped up. The miner tops up only plain
  delegations (`ownDelegation.plain`), whatever the version.
- **Sequencer**: refuses a strict request naming a delegation with extra elements
  (`sequencer/txbuilder_seq/req_topup.go`), which would otherwise make it build an invalid
  milestone. A sequencer whose `MinTopUp` is above A (95 PROX) cannot receive option 3 rewards; the
  floor is 90.
- **Wallet and consolidator**: no change; they keep the 24-byte form.
- **`mine_verify.go`**: no change. The miner checks a foreign transit's amounts, not its lock kinds,
  and the node validates a transit in full before streaming it.
- **Docs site, still to do**: the mining page's description of the three payouts, and the advice for
  miners who run a sequencer.

## Tests

`ledger/tests/library_upgrade6_test.go`, each case on a ledger listing upgrades 1 to 6:

- `TestUpgrade6Library`: miner version 5 before the slot, 6 from it.
- `TestUpgrade6MinePayouts`, before and from the slot: the full reward as a plain tag-along or a
  24-byte request (valid, then refused), as a strict request (refused, then valid), with a script at
  element 3 (refused from the slot); options 1 and 2 as built (valid on both sides) and with an
  extra element, and the fee with an extra element (valid, then refused).
- `TestUpgrade6TopUpForms`: the 24-byte form into a plain delegation and into one carrying an extra
  element, valid on both sides; the strict form into a plain delegation valid, into one with an
  extra element refused; the strict form before the slot refused.
- `TestUpgrade6FakeDelegation`: a sequencer taking a 24-byte request into its own `sigLock` chain
  with a frozen `delegateLockState`: valid before the slot, refused from it.

`proxi/node_cmd/mine`: `TestTopUpFormFollowsSlotRules` (the form by slot), `TestPlaceRewardPlainOnly`
(no top-up of a delegation with extras). The existing top-up tests (`ledger/tests/delegate_topup_test.go`,
`tests/topup_request_test.go`) pass unchanged.

Not yet: a sequencer-level test of the strict refusal in `req_topup.go`, and a local rehearsal with
the miner switching layouts across the slot.

## Open

- **Measure first**: before choosing a slot, count option 3 transits with no request and their
  targets over a few thousand slots. That tells how many miners and sequencers depend on what the
  upgrade removes.
- Whether a weak-key miner's race through their own sequencer (askstop, then an immediate spend on
  the master path) wins often enough to keep the incentive alive. That can only be measured live.
