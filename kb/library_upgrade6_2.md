# Library upgrade 6.2: transaction-hash proof of work, one plain reward

> SPEC, NOT BUILT. Branch `feat/upgrade6-1` (2026-11-05). Option C of three
> (`kb/library_upgrade6_options.md`): A is upgrade 6 as built (`kb/library_upgrade6.md`, VRF work,
> enforced delegation), B is upgrade 6.1 (`kb/library_upgrade6_1.md`, a key-bound hash, enforced
> delegation). C drops the VRF and the delegation rules: the work is the transit's own transaction ID,
> and the reward is one plain `sigLock` plus the fixed 1 PROX fee to one sequencer. Replaces upgrade
> 6, which is neither listed nor released: one upgrade, one slot, miner version 6. Prerequisite:
> upgrade 5 active (slot 153000).

## 1. The rule

From the activation slot a mine transit is valid when, besides the shape and the successor state of
today:

- **Work**: the last 8 bytes of its transaction ID, read big-endian, end in at least
  `K = _mineRequiredK(B, M)` zero bits (the existing `_minePoWOK`). The mine input's unlock
  parameters are the 8-byte nonce.
- **Mint**: exactly A (`_mineAmount`), always. The successor's inflation is A and R falls by A.
- **Payout**: exactly three outputs:

| Index | Output | Elements | Amount |
|-------|--------|----------|--------|
| 0 | the mine successor | as today, pinned in full by upgrade 5 | unchanged |
| 1 | the reward: a plain `sigLock`, to any holder | 3: amounts, index values, `sigLock` | A − `constMineTagAlongFee` (94 PROX) |
| 2 | the inclusion fee: a plain tag-along from the signer to one sequencer | 3: amounts, index values, `tagAlong` | `constMineTagAlongFee` (1 PROX) |

- **No transaction-level constraints** (`TxConstraints` empty), so the nonce is the only free field
  (section 1.2).

### 1.1 Why the transaction ID is the work

The transaction ID is the BLAKE2b hash of the transaction tuple with the signature element skipped
(`HashEssence` in `ledger/txbuildercore/sign.go`), its first 6 bytes then overwritten with the
timestamp and the output count; the last 8 bytes are the hash's own. The hashed part holds what
matters:

| In the hash | Effect |
|-------------|--------|
| the input, the predecessor mine output | no work before the predecessor exists, no stockpiling |
| the timestamp | an attempt is for one slot; the pace model and retarget keep their meaning |
| the outputs | **the work commits to the payee and to the sequencer paid.** Change either and the work is gone, so a copied solution cannot be redirected |
| the unlock data | the nonce |
| not the signature | the free signature nonce that sank the first work function (`kb/mine_vrf_pow.md` 1) is out of the hash |

`txID` is an existing builtin (`evalTxID`), and the node computes every transaction's ID anyway, so
checking the work costs nothing.

The signer is still bound: the tag-along lock requires its sender to be the transaction's signer, so
a transit re-signed by another key fails (`kb/library_upgrade6_options.md`, S5).

### 1.2 Why only the nonce is free

With the layout pinned, every byte the miner could vary per attempt is fixed before the search
except the nonce. The reward's holder could in principle be varied, but each value is someone's
address, so varying it gives the reward away. The empty `TxConstraints` closes the one place an
arbitrary element could be appended after the outputs.

The nonce sits in the unlock data, which the tuple serializes before the outputs
(`ledger/def_constants_path0.go`), so every attempt rehashes the unlock data and the three outputs,
a few BLAKE2b blocks, while the prefix up to the unlock data is hashed once per transit. That cost is
the same for every miner: there is no layout trick to make it cheaper.

### 1.3 Who receives the reward

Any holder. The work already binds it, so tying it to the signer as upgrade 6 does adds nothing. It
lets a miner be paid at a cold address while signing with a hot key. Pinning it to the signer
(`txHolderID(txSignatureData)`, as `_mineOpening` does) is one extra line if preferred.

### 1.4 EasyFL

Replaced in the upgrade library, same arity. `_mineShape` and the rest of `lock_mine.easyfl` stay:
`_mineShape` already allows three outputs, and its exhaustion check reads `_mineMinted`.

```
// the work: low 64 bits of the transaction ID, the hash of the transaction without its signature
func _mineWork64 : tail(txID, 24)

func _minePaceAndPoW :
and(
   require(equalUint(len(selfUnlockParameters), u64/8), !!!mine_unlock_params_must_be_the_nonce),
   require(_minePoWOK(_mineWork64, _mineRequiredK($0, _mineM)), !!!insufficient_mine_proof_of_work)
)

// every transit mints the full A
func _mineMinted : _mineAmount

// one plain sigLock reward of A - T, one plain tag-along fee of T, nothing else
func _minePayout :
and(
   require(equalUint(numProducedOutputs, u64/3), !!!mine_transit_must_have_three_outputs),
   require(equalUint(tupleLenAtPath(concat(pathToProducedOutputs, 1)), u64/3), !!!mine_reward_must_be_a_plain_sigLock),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #sigLock), !!!mine_reward_must_be_sig_locked),
   require(equalUint(_mineOutAmount(1), sub(_mineAmount, constMineTagAlongFee)), !!!mine_reward_must_be_A_minus_the_fee),
   _mineInclusionFee(2),
   require(isZero(tupleLenAtPath(pathToTxConstraints)), !!!mine_transit_takes_no_transaction_constraints)
)
```

`_mineInclusionFee` is upgrade 6's: a plain tag-along, three elements, of exactly
`constMineTagAlongFee`. The payout option O on the mine lock is no longer read by the consumed arm;
the produced arm still requires `O <= 3`, and the miner writes 1, the code of the old opening, the
layout this one resembles.

Upgrade 6's other payout functions, both guarantor forms and its miner and sequencer changes are not
used. Its one fix outside mining, the top-up guarantor requiring its predecessor to be a
`delegateLock` output (`kb/library_upgrade6.md`, "The guarantor"; it closes the fake-delegation
route), is worth keeping on its own and can ride in the same upgrade.

## 2. Consequences in the code

| Where | Change |
|-------|--------|
| `ledger/def/upgrade6.easyfl` | the functions of 1.4 and upgrade 6's `_mineInclusionFee`, `constMinerVersion` 6; upgrade 6's other payout functions removed |
| `ledger/transaction/tx.go` | `MineWorkValue()` by slot: the last 8 bytes of the txid from the slot, the VRF output before |
| `core/core_modules/txinput_queue` | floor gate reads the txid, exact and free. **The unknown-sender exemption, today only for the opening (option 1), must cover this layout**: a new miner has no funds. Duplicate-solution gate: the txid already deduplicates a solution. Core: `-race` |
| `sequencer/mine` | winner rule: older slot, then smaller work value, then txid; the work value is part of the txid, so it reduces to the txid's last bytes. Settlement policy unchanged: one tag-along addressed to the sequencer, fixed fee |
| `proxi/node_cmd/mine` | hot loop hashes the unsigned transaction: build the template once, hash the prefix up to the unlock data once, rehash from there per nonce. The template machinery deleted with the VRF change (`mineTemplate`, `buildEssence`, before `6d217554`) comes back in shape. Payout options and their pricing go; a profile key names the reward holder (default the wallet's own address). Signs only the winner |
| seeker protocol, `nonce_seeker/` | the job carries the hashed prefix state or the template bytes and the nonce offset; keyless |
| `ledger/txbuildercore/helpers_mine.go` | the VRF message and 88-byte unlock layout no longer used from the slot |
| `util/vrf` | stays for the branch bonus; the split prover and Rust twin serve only mining and can go after the slot |

The smallest change of the three in the ledger; the miner and seeker changes are about those of 6.1.

## 3. Downsides

### 3.1 Exploits

**C1. Pools, rented hashrate, hardware**: as in 6.1 (`kb/library_upgrade6_options.md`, B1-B4). No
key is involved in the work, so pooling needs nothing. One reward output per transit means a pool
cannot pay its workers on chain from the transit itself: pools are custodial, as under B. Unlike B,
the pool's rewards are plain outputs, not delegations: it decides where they go only if it chooses to
delegate them.

**C2. Settlement bias by an own sequencer** (S3 of the options): unchanged from today. With the fee
fixed and one tag-along, it cannot be bought, so it is the same as under A and B.

No other exploit is specific to this option: the fee is fixed, so inclusion cannot be bought; one
tag-along, so no spraying; only the nonce is free, so no layout advantage; the payee and the
sequencer are in the hash, so no redirection; the signer is the fee's sender, so no re-signing.

### 3.2 Strategy

- **Mined tokens land idle; delegating them is the holder's choice.** The payout pricing
  (`kb/mine_payout_options.md`) and upgrade 6's forced top-up existed so that mined tokens back
  sequencers. Here every reward is a plain output of 94 PROX. Three things make this a smaller loss
  than it looks:
  - **The enforcement it gives up can be bypassed anyway.** Under A and B a miner who wants idle
    tokens takes option 2 and withdraws the delegation at once (a delegation origin cannot be
    frozen), or names a delegation that does not exist and reclaims the full reward after 30 slots
    (`kb/library_upgrade6_options.md`, S1 and S2). Forcing delegation only charges a price to those
    who would not delegate.
  - **The tooling to delegate already exists.** `proxi node consolidate` (`kb/consolidate.md`) is a
    permanent wallet process that sweeps an account's `sigLock` outputs, once above its threshold,
    into delegations to targets drawn by the sequencer rating, topping up and re-delegating as
    needed. A miner who runs it next to
    `proxi node mine` delegates every reward without thinking about it.
  - **Holders already know why to delegate**: delegation is how capital earns, and the network's
    users have learned it.

  What remains is that delegation becomes a default the miner opts into rather than a rule, so the
  share of mined supply in consensus depends on how many miners run the consolidator. It is worth
  measuring after activation rather than enforcing in advance.
- **The delegation market loses its inflow.** Sequencers no longer receive a steady stream of mined
  delegations; their weight rests on capital that holders choose to delegate.
- **The plainest proof of work**: one fixed transaction shape, one hash, nothing Proxima-specific in
  the work. Easy to explain and audit. The fair launch becomes "hardware buys emission", stated
  plainly.
- **The forced-delegation escapes stop mattering** (options S1, S2): nothing is forced, so there is
  nothing to close.
- **Mining from nothing works as today**: the layout is the old opening with the full reward, so a
  new miner needs no funds.

### 3.3 Behaviour to expect

- Miners take 94 PROX liquid per transit. Those running the consolidator delegate it once their
  plain outputs pass its threshold (`threshold_prox`, `compact_at`); others hold or sell.
- Pools form, custodial; GPU farms take most transits, as under 6.1.
- The reason to run a sequencer just to mine shrinks to the settlement bias (C2).

## 4. Tests

`ledger/tests/library_upgrade6_test.go`, rewritten for this option:

- a VRF transit valid before the slot, refused from it; a transaction-hash transit refused before,
  valid from it;
- from the slot: the layout as specified valid, with the reward to the signer and to another holder;
  refused: two or four outputs, the reward not a `sigLock` or with an extra element, the reward
  amount off by one mote, the fee not a tag-along or not exactly T or with an extra element, a
  transaction-level constraint present, unlock parameters not 8 bytes; a payout changed after
  solving refused (the work is gone); too few zero bits refused; a transit re-signed by another key
  refused (the fee's sender);
- `MineWorkValue` equals what the covenant tests on both sides of the slot;
- if kept, `TestUpgrade6FakeDelegation`.

`core/core_modules/txinput_queue`: the unknown-sender exemption for this layout, the floor gate
(`-race`). `proxi/node_cmd/mine`: template hashing, seeker job format.

## 5. Open

- Measure after activation what share of mined rewards ends in delegations (3.2). If it is low,
  the remedy is the docs site recommending the consolidator for every miner, or `proxi node mine`
  running it by default, not a ledger rule.
- The reward holder: any (as specified) or pinned to the signer.
- Difficulty at the switch: about as in 6.1 section 6.1.
