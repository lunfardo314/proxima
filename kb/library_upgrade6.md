# Library upgrade 6: a mined reward always lands under the mining key

> SPEC, not built (draft written 2026-11-05). Sixth live library upgrade, no reset. Activation
> slot and release not chosen. Prerequisite: upgrade 5 active (slot 153000). Mechanism:
> `ledger/upgrade.md`. Touches the mine lock only (`ledger/def/lock_mine.easyfl`); the reward
> layouts it changes are in `kb/mine_payout_options.md`.

## The problem

The mine chain's proof of work is the trailing-zero count of an ECVRF output under the miner's key
(`kb/mine_vrf_pow.md`). Each attempt costs a scalar multiplication `Gamma = x*H`, where `x` is the
secret scalar. A miner who picks `x = 1` skips that multiplication, since `Gamma = H`, and miners
report about five times the hashrate. Any small `x` gives most of the gain (`x < 2^k` costs about
`k` doublings).

The key is then public, so any output it controls can be spent by anyone. That ought to make a weak
key self-defeating, but payout option 3 leaves a way out. Option 3 is one tag-along of A (95 PROX) from the
signer to any sequencer, with any request payload or none. A tag-along with no request is simply
consumed by its target into the sequencer's own chain. So a miner who sends option 3 to their own
sequencer never holds anything under the weak key. The sender can only reclaim the tag-along from
`constTagAlongSlots` on, long after the sequencer has taken it, so the race with a thief is never
run.

Consequences:

- The proof of work no longer prices hashrate fairly between careful miners and weak-key miners.
- The only safe exit for a weak-key miner is their own sequencer. That pushes the network towards
  one sequencer per miner (68 sequencers on 2026-11-05, rising). The network handles that number, but
  the trend is driven by an exploit rather than by sequencing being worth doing.

A weak key cannot be refused directly. Nothing in the public key reveals that its scalar is small,
and a list of banned keys could never be complete. The fix is economic: whatever the transit mints
must land somewhere the mining key controls, so a known key loses its reward to whoever is fastest.

## What already holds

Options 1 and 2 already pin the signer:

- **1, opening**: a sigLock to the signer (`_mineOpening`). Anyone holding the key can spend it.
- **2, new delegation**: a delegation origin whose master is the signer (`_mineNewDelegation`).

Option 3's tag-along has the signer as sender, which the tag-along lock itself enforces on every
tag-along (`ledger/def/lock_tag_along.easyfl`; `_mineFull` checks it a second time), but nothing pins
what the target does with it.

The delegate lock already binds a top-up to the master. `_topUpAuthorised` in
`ledger/def/lock_delegate.easyfl` refuses a top-up unless the referenced output is a tag-along
whose sender (index value 0) is the delegation's master. `ensureTopUpDelegation` in
`ledger/def/ensure.easyfl` forces the target onto that path, and on no other path, while the target
can consume the output. Sender equals signer and master equals sender, so a top-up request produced
by a transit can only grow a delegation whose master is the mining key.

## The ledger rule

**Option 3 is a top-up request.** `_mineFull` is replaced, same arity (none): output 1 must carry
`ensureTopUpDelegation` at element 4.

```
// Replaced: the tag-along of the full reward is a top-up request, element 4 an
// ensureTopUpDelegation. The delegate lock accepts a top-up only from the
// delegation's master, and the tag-along lock pins the sender to the signer, so
// the reward can only grow a delegation the mining key controls.
func _mineFull :
and(
   require(equalUint(numProducedOutputs, u64/2), !!!mine_full_payout_needs_two_outputs),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #tagAlong), !!!mine_full_payout_must_be_a_tag_along),
   require(equalUint(_mineOutAmount(1), _mineAmount), !!!mine_full_payout_must_be_A),
   require(
      and(
         equalUint(tupleLenAtPath(concat(pathToProducedOutputs, 1)), u64/5),
         equal(parseBytecode(atPath(concat(pathToProducedOutputs, 1, 4)), 0x), #ensureTopUpDelegation)
      ),
      !!!mine_full_payout_must_be_a_top-up_request
   )
)
```

The sender check of today's `_mineFull` is dropped as redundant: the tag-along lock's produced arm
already requires the sender (index value 0) to be `txHolderID(txSignatureData)`.

The element count is checked first and is required: `atPath` on an element that is not there
panics, and the check must refuse a tag-along without the guarantee, not crash on it. `and`
evaluates its arguments in order and stops at the first false one (`evalAnd` in easyfl), so the
guarantee is parsed only when element 4 exists. Equal to 5 rather than at least 5, since the
tag-along lock allows no more than five elements anyway.

Element 3, the request data, is not checked. A tag-along with the wrong request code is one no
sequencer takes. A transit is included only when a sequencer consumes its tag-along, so the miner
only harms themselves. The same holds for a top-up naming a delegation that does not exist or does
not target the addressee.

Pure EasyFL, no new embedded function. Delegation, sequencer request and `ensureTopUpDelegation`
outputs are unchanged, and so are the top-up requests wallets already produce.

## Why it closes the gap

After the upgrade every layout ends under the mining key:

| Option | Lands as | Who can take it, given the key |
|--------|----------|--------------------------------|
| 1 | sigLock to the signer | anyone, at once |
| 2 | delegation, master the signer | anyone, while it is not frozen and in each safe revocation window |
| 3 | top-up of a delegation whose master is the signer | the same |

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
instead of a path nobody can enter. What it removes is the safe weak-key exit through a sequencer,
which is what made "one miner, one sequencer" pay.

## Considered and dropped: a master argument on `ensureTopUpDelegation`

The first idea was a second form of `ensureTopUpDelegation` with one 56-byte argument, the
delegation ID (24 bytes) followed by a master holder ID (32 bytes), told apart from today's form by
length, so the mine lock could require the master to be the signer. It is not needed:
`_topUpAuthorised` already makes sender equal master, and `_mineFull` makes sender equal signer. It
would also have cost more than the constraint itself. `_topUpAuthorised` compares the whole argument
with the delegation ID, so a 56-byte argument would fail there and the delegate lock would have to
be replaced too. The Go parser (`ledger/ensure.go`) and the sequencer's request parser would also
have had to change.

## Node, miner and wallet

- **`proxi node mine`**: the mode that sends the full reward to the own sequencer with no request
  (`SendOwn` with `SendTo`, `proxi/node_cmd/mine/mine_payout.go`) is valid only before the activation slot. From the slot
  on, option 3 is a top-up of an own delegation. The miner already builds that (`choosePayout`), and
  already reads the rules of the slot it builds for (`kb/library_upgrade1.md`). An operator running
  their own sequencer keeps a delegation to it, opened with option 2, and tops that up.
- **`mine_verify.go`**: the check of a foreign transit follows the slot's rules, refusing option 3
  without the request from the slot on.
- **Sequencer**: no change. Transits with top-up requests are taken today. A sequencer whose
  `MinTopUp` is above A (95 PROX) cannot receive option 3 rewards. The floor is 90.
- **Miner version**: `constMinerVersion` redefined to 6 in `upgrade6.easyfl`, as upgrade 1 did with
  5, together with the miner's own version, so a miner from an earlier release refuses to run past
  the slot.
- **Docs site**: the mining page's description of the three payouts, and the advice for miners who
  run a sequencer.

## Tests

In `./ledger/...`, one test each, before and after the activation slot:

- option 3 with no request: valid before, refused after;
- option 3 with an `ensureTopUpDelegation` request at element 4: valid on both sides;
- option 3 with the guarantee missing or at another index: refused after;
- a top-up of that request into a delegation whose master is not the signer: refused by the
  delegate lock on both sides (pins the property the upgrade relies on).

Then a local rehearsal with the miner switching layouts across the slot.

## Open

- **Measure first**: before choosing a slot, count option 3 transits with no request and their
  targets over a few thousand slots. That tells how many miners and sequencers depend on the path.
- Whether to pin element 3 to request code 4 as well. It would protect nothing beyond the miner's own
  transit; left out for that reason.
- Whether a weak-key miner's race through their own sequencer (askstop, then an immediate spend on
  the master path) wins often enough to keep the incentive alive. That can only be measured live.
