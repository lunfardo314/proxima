# Library upgrade 6: transaction-hash proof of work, reward paid as the miner decides

> **SPEC, CHOSEN 2026-11-06, BUILT ON `feat/upgrade6-mining`, NOT LISTED.** Steps 1 to 5 built
> 2026-11-07; rehearsed 2026-11-07 (section 8), four bugs found, three fixed. Planned: release 2026-11-08, activation
> 2026-11-10 08:00 UTC, mining resuming 1,000 slots later.
> Code written for it names this document (`kb/library_upgrade6_mining.md`) in its comments, never a
> section number. Library upgrade 6: new validity rules from an activation slot, no network reset,
> listed as index 6 in `ledger.Upgrades` (`ledger/upgrade.md`). Prerequisite: library upgrade 5
> active (slot 153000). Miner version 6. Figures marked *estimate* are reasoning, not measurement.
> **Not only mining:** from the slot the transaction ID of every transaction commits to its signer
> (1.6), so every wallet must be upgraded before the slot.
>
> History: written 2026-11-05 as option C of three, "library upgrade 6.2"; second external review
> folded in 2026-11-06; reviewed against `develop` the same day; security review folded in 2026-11-07
> (one input, the essence with element boundaries, canonical signature data, eligible rivals). The comparison and the two rejected
> options are in `kb/research/mining-algo/` (`options.md`, `option_a_vrf_enforced.md`,
> `option_b_key_hash.md`), with an unplanned stop-gap that pauses mining (`mining_pause.md`).
>
> **What this upgrade claims.** It removes the known key-dependent shortcut and adopts a
> conventional proof of work bound to the payout. It does not make reward allocation fair by
> itself: the hash decides which solutions qualify, settlement decides who is paid, and settlement
> is sequencer policy (4.1).

## 0. Context

Proxima emits part of its supply through the **mine chain**: a single chain output that anyone may
spend once per slot (10.24 s) with a proof of work. The spending transaction, a *transit*, consumes
the mine output, produces its successor and mints a reward of A (95 PROX today, rising later by
schedule). Difficulty B is retargeted on
chain; competing transits for one slot are settled by the sequencers, the smaller work value winning.

A **tag-along** is an output addressed to a chain, usually a sequencer: its sender is always the
transaction's signer, the target chain may consume it within 30 slots, the sender may take it back
after that, and anyone after 390 slots. It may carry a request to the sequencer (for example, to top
up a delegation). A sequencer includes a transit by consuming a tag-along addressed to it.

**Today's work** is the trailing-zero count of an ECVRF output (RFC 9381) computed under the miner's
own key over (predecessor output ID, slot, nonce). It was chosen to make mining non-outsourceable:
every attempt needs the private key, and the reward must go to that key's owner, in one of three
priced layouts chosen by the payout option O on the successor (`kb/mine_payout_options.md`): an
opening `sigLock` of 15 PROX or a new delegation of 50 PROX, each with a fixed 1 PROX inclusion fee
tag-along, or one tag-along of the full A. The transit mints what the layout takes.

**The problem.** An attempt costs a scalar multiplication `x*H` under the secret scalar `x`. A miner
choosing `x = 1` skips it and runs 5-10 times faster; a scalar of 96 to 128 bits gives an
estimated 1.6 to 2 times, and nobody can tell such a key from an honest one. A scalar-1 key is
public, so its rewards should be up for grabs, but a miner running its own sequencer includes its
own transits and spends the reward in the same slot, before anyone can race for it. The result is a
5-10x edge for those who cheat and a reason to run a sequencer just to mine (68 sequencers on
2026-11-05, rising).

**Three remedies were considered** (`kb/research/mining-algo/options.md`); A and B are rejected:

- **A** (`option_a_vrf_enforced.md`). Keep the VRF and pin every reward output so that it lands as a delegation. Does not remove
  the speedup or reach medium keys, and the own-sequencer shelter remains.
- **B** (`option_b_key_hash.md`). Replace the VRF by `blake2b(predecessor ID, slot, signer public key, nonce)`, keeping A's
  forced delegation. Removes every key trick, but the forced delegation can be escaped (an unserved
  top-up request is reclaimed by its sender after 30 slots; a new delegation can be withdrawn at
  once, since a delegation origin cannot be frozen), so the forcing only charges a price.
- **C, this document.** The work is the transit's own transaction ID. Beyond the mine chain the
  ledger leaves the transit's shape to its builder: any outputs, in amounts the builder chooses;
  there is no fixed fee and nothing is forced.

## 1. The rule

From the activation slot a mine transit is valid when, besides the successor rules of today (the
successor at output 0, pinned in full, R decremented, B and C retargeted):

- **Work**: the last 8 bytes of the transaction ID, read big-endian, end in at least
  `K = _mineRequiredK(B, M)` zero bits (the existing `_minePoWOK`). M is the slot gap from the
  predecessor; K is B at a gap of one slot and eases one bit per extra slot.
- **Mint**: exactly A, always. The successor's inflation is A and R falls by A.
- **One input**: the mine output, nothing else (1.2).
- **Not a sequencer transaction**: no sequencer output, hence no endorsements either (only a
  sequencer transaction may endorse, `tx_integrity_validator.easyfl`).
- **The slot timing of today**: the start slot, the pace, and the timestamp at or after the
  settlement tick minus the sequencer pace (upgrade 1).

**Nothing else.** The number and kinds of the outputs, the contents of `TxConstraints`, the unlock
parameters of the mine input and where the nonce sits are left to whoever builds the transaction. The
ledger checks them as it checks any transaction: signatures, locks, storage deposits, size limits and
the balance rule. The balance rule alone fixes the sum: every transaction produces exactly what it
consumes plus its inflation, only chain outputs carry inflation, and the successor keeps its balance
with inflation A, so the other outputs receive exactly A, however they are split.

### 1.1 Why the transaction ID is the work

The transaction ID is the BLAKE2b-256 hash of the transaction's elements in order, with the
signature element skipped (`HashEssence`, `ledger/txbuildercore/sign.go`); its first 6 bytes are
then overwritten with the timestamp and the output count, and the last 8 bytes are the hash's own.
From the slot it hashes the signer's holder ID first and then every element but the signature data,
each preceded by its length, so element boundaries are included (1.6).

| In the hash | Effect |
|-------------|--------|
| the signer's holder ID, first (1.6) | the work is bound to the key that signs: re-signed by another key, the transit is another transaction, and its work is gone |
| the input, the predecessor mine output | no work before the predecessor exists, no stockpiling |
| the timestamp | an attempt is for one slot; the pace and the retarget keep their meaning |
| the outputs | **the work commits to every payee, every amount and every chain paid**, whatever the layout. Change any and the work is gone, so a solution cannot be redirected |
| whatever the builder varies (the nonce) | the search |
| not the signature | a signature has free randomness of its own; kept out, it cannot be ground |

`txID` is an existing EasyFL builtin and the node computes every transaction's ID anyway, so
checking the work costs nothing extra.

### 1.2 Why the outputs are free, and the two rules that are not

**The shape protects nothing.** The rules of the former options fixed the outputs to price where
the reward lands (A, B) or to make one attempt cost the same for every layout (the first draft of
this option). Neither needs a ledger rule here:
- **No cheaper attempt.** The work is the whole transaction ID. Whatever bytes are varied, an attempt
  costs at least one BLAKE2b compression of the final block, and a builder who varies the last bytes
  gets exactly that. Where the nonce sits is the builder's efficiency, not anyone's advantage.
- **No redirection.** Every payee, chain and amount is in the hash in any layout.
- **No new load or dust.** Size limits, storage deposits and tag-along reclaim apply as to any
  transaction. A transit with many tiny tag-alongs is what anyone can send with their own funds.

**One input.** Sequencers defer to the best validated transit of a slot (2.2). A transit spending an
output of its own besides the mine output can be made impossible to include after it is published:
its maker spends that output in another transaction, and every sequencer whose past cone holds that
one can no longer take the transit. The best transit of the slot would then be one nobody can
include, and the sequencers deferring to it leave the slot empty, repeatably, for as long as they
keep it as a rival. With the mine output as the only input, a transit conflicts only with rival
transits, which the order of solutions settles (2.3). The cost is small: a transit cannot bundle a
consolidation of the miner's other outputs.

**Not a sequencer transaction.** The one-input rule already excludes it, since a milestone always
consumes its own chain output; the explicit check states the reason. A sequencer putting the transit
inside its own milestone would grind it there and publish it only inside the milestone that settles
it. No other node would see that transit before it wins: private mining, the hidden-transit
advantage of the 2026-10-19 analysis (`kb/mine_settlement_upgrade.md`). A transit must be a
transaction of its own, which every node can see and rank before the settlement window.

### 1.3 Inclusion without a rule, the signer bound by the ID

**No tag-along is required.** A sequencer includes a transit by consuming one of its tag-alongs, so
a miner who wants its transit included pays one; a transit paying no sequencer is left out, unless a
later transaction that pays one spends its outputs (indirect inclusion, 2.2). Requiring a tag-along
would add a rule for what the miner's own interest already does.

**The signer is bound in every layout.** The signature is not in the hash, but the signer's holder
ID is (1.6). A transit re-signed by another key is another transaction with another ID, so a solution
belongs to the key that signed it. The miner's identity, where it matters (the monitor's per-miner
figures), is the transit's signer.

A copy with other signature data under the same key, an invalid signature, still shares the ID.
That is a node concern for every transaction type: the node's deduplication and its handling of a
copy that fails validation must never let an invalid copy arriving first suppress the valid one. The
upgrade ships with a regression test for it (section 6).

### 1.4 What the builder decides

Everything but the input and the successor: who is paid, how much, through which kind of output, how
many outputs, and how the search varies the transaction.
The ledger does not and cannot bound what goes where: it sees addresses, chains and amounts, not who
is a pool, a worker or the miner's own sequencer. Typical outputs:

- **A `sigLock`**: tokens to any address, such as a cold address while signing with a hot key, at
  least the storage deposit (9.25 PROX for a plain `sigLock`).
- **A tag-along**: tokens to a chain, of any size, no storage deposit. To the miner's own sequencer,
  to a pool's sequencer, to any sequencer as payment for inclusion, or with a request, for example a
  top-up of the miner's own delegation straight from the transit. A tag-along not consumed within 30
  slots goes back to its sender, the signer.
- **Anything else the ledger accepts**: a delegation origin, a chain, an output under a script.

The reference miner's template is in 2.4.

### 1.5 EasyFL (built in `ledger/def/upgrade6.easyfl`)

Replaced in the upgrade library, same arity; the rest of the mine lock stays. **Built**, with the
canonical `signaturePublicKey` of 1.6; tests `TestUpgrade6HashTransit`,
`TestUpgrade6VRFTransitRefusedFromTheSlot`, `TestUpgrade6CanonicalSignatureData`. A V or O written
as an expression is refused when `_mineSuccArg` parses it ("not an inline data function").

```
// the work: low 64 bits of the transaction ID (from this upgrade on it also commits to the signer)
func _mineWork64 : tail(txID, 24)

// the work at K; nothing else of the transaction is read. $0 B
func _minePaceAndPoW :
   require(_minePoWOK(_mineWork64, _mineRequiredK($0, _mineM)), !!!insufficient_mine_proof_of_work)

// every transit mints the full A; the balance rule leaves exactly A to the other outputs
func _mineMinted : _mineAmount

// the payout is not checked: the successor and the balance rule are all the covenant needs
func _minePayout : 0xff

// $0 R. As in upgrade 1 without the output count, and not a sequencer transaction
func _mineShape :
and(
   selfIsConsumedOutput,
   require(lessOrEqualThan(uint8Bytes(constDisableMiningUntilSlot), uint8Bytes(txSlot)), !!!mining_is_disabled_until_the_start_slot),
   require(lessOrEqualThan(uint8Bytes(constMineMinPace), uint8Bytes(_mineM)), !!!mine_pace_below_minimum),
   require(
      lessOrEqualThan(
         sub(sub(sub(sub(ticksPerSlot64, 1), constPreBranchConsolidationTicks), constMineSettlementWindowTicks), constTransactionPaceSequencer),
         uint8Bytes(txTick)
      ),
      !!!mine_transit_stamped_before_the_settlement_tick_minus_the_sequencer_pace
   ),
   require(equalUint(numInputs, u64/1), !!!mine_transaction_must_have_exactly_one_input),
   require(not(isSequencerTransaction), !!!mine_transit_must_not_be_a_sequencer_transaction),
   require(equalUint(len(selfSiblingUnlockParams(chainConstraintIndex)), u64/1), !!!mine_chain_must_continue),
   require(isZero(_mineSuccIdx), !!!mine_successor_must_be_at_output_index_0),
   require(equal(parseBytecode(_mineSuccLock, 0x), #mineLock), !!!mine_successor_must_carry_mineLock),
   require(lessOrEqualThan(uint8Bytes(_mineMinted), uint8Bytes($0)), !!!mine_chain_is_exhausted)
)

// $0 R  $1 B  $2 C. As in upgrade 5, and V and O pinned as inline data on the successor
func _mineSuccessorState :
and(
   <the checks of upgrade 5>,
   require(equalUint(_mineSuccArg(3), constMinerVersion), !!!wrong_miner_version_on_mine_successor),
   require(lessOrEqualThan(uint8Bytes(_mineSuccArg(4)), uint8Bytes(u64/3)), !!!wrong_payout_option_on_mine_successor)
)
```

Replaced: `_minePaceAndPoW`, `_minePayout` and `_mineMinted` from `ledger/def/lock_mine.easyfl`,
`_mineShape` from its current definition in `ledger/def/upgrade1.easyfl`. Today's `_mineOpening`,
`_mineNewDelegation`, `_mineFull` and `_mineInclusionFee` fall out of use. `_mineSuccessorState` of
upgrade 5 is replaced, keeping its checks: it pins the successor to `_mineMinted`, now always A, and
now also pins V and O as inline data. The produced arm of `mineLock` evaluates V and O as expressions,
and only the payout functions dropped here read O through `_mineSuccArg`; without the new checks a
winner could write either as an expression, harmless to the ledger, which never reads them on the
spending side, but misread by every parser of the mine output. The payout option O is no longer read;
the miner writes 1. `constMineTagAlongFee` is no longer used by the
mine lock.

One fix outside mining can ride in the same upgrade: the top-up guarantor (`ensureTopUpDelegation`)
should require its predecessor to be locked by `delegateLock`. Today a chain with any lock and a
trailing delegation-state element passes it without the delegation lock ever running. With requests
allowed on reward tag-alongs, this matters more. **Built on this branch** (taken from rejected
option A without its strict form): `_ensureTopUpDelegation` replaced in `ledger/def/upgrade6.easyfl`,
tested by `TestUpgrade6FakeDelegation`.

### 1.6 The transaction ID: the signer, element boundaries, canonical signature data

From the slot the essence the transaction ID hashes is
`blake2b(holderID || len(e) || e || ...)` over every element `e` in order but the signature data,
each length 4 bytes big-endian, where the holder ID is
`blake2b(signature type || public key)`, the value `txHolderID(txSignatureData)` computes in EasyFL
and `base.Signature.HolderID` in Go, both reading the type byte of the signature data first. The ID then names who signed as well as what was signed: a
copy signed by another key is another transaction. This applies to **every transaction** from the
slot, not only to transits.

- **Element boundaries.** Each element is hashed after its length: one ID, one byte encoding of
  everything but the signature. Lengths rather than the serialized tuple, so the ID does not depend
  on how a tuple encoder chooses its length prefixes. The ID's first 6 bytes are still overwritten
  with the timestamp and the output count. **Built** (`TxVersionSignerBoundID` = 6,
  `ledger/txbuildercore/sign.go`; `txid_signer_test.go`, `TestUpgrade6SignerBoundTxID`).
- **Canonical signature data.** The signature data is outside the hash, so its encoding must leave
  no freedom either: from the slot it must be exactly the canonical encoding of its type, for
  ED25519 `type || signature || public key`, 97 bytes. Go refuses anything else while deriving the
  ID (through the strict `base.SignatureFromBytes`, returning an error, never panicking, since it
  runs on raw incoming bytes), and the upgrade library replaces `signaturePublicKey` (same arity,
  built) to check the same length; `validSignature` and `txHolderID` read the key through it, so Go and EasyFL accept exactly the same
  signature data. Every wallet already produces exactly 97 bytes. Only the key's owner can still make a
  second valid signature for the same transaction, which is harmless; Go's `ed25519.Verify` already
  refuses non-canonical signature scalars.
- **First, not last.** The reference template puts the nonce last (2.4). A holder ID after it would
  have to be hashed on every attempt and sent to every seeker; first, it is part of the state a miner
  keeps.
- **By version.** `TxIDFromTree` derives the ID by the transaction's own `TxVersion`, which `tx.Parse`
  already checks against the library of the transaction's slot: version 6 and above with the holder
  ID, older ones without. Stored IDs, the transaction store and snapshots are unchanged.
- **No EasyFL change.** The `txID` builtin returns the ID the node computed.
- **Signing.** The signature signs the transaction ID. `SignED25519` computes the ID before the
  signature data exists, so it takes the holder ID from the key it signs with.
- **Cost.** One 32-byte block more per ID plus 4 bytes per element, and the holder-ID hash, which
  validation computes anyway.
- **Every signer must upgrade before the slot.** A transaction signed the old way is refused from
  the slot, never lost. The upgrade already moves nodes, sequencers and miners; this adds `proxi`
  (which stamps the version by slot through `glb.SetTimestamp` but would sign the old ID), the wasm
  wallet and anything else that signs. The wallet release goes out well ahead of the slot.

**Other signature types.** The ID depends on the signature data only through the holder ID, so a
new type, say BLS as type 1, needs no change to the essence: the holder ID definition is extended,
and the ID follows. What a new type has to bring, none of it caused by this rule:
- **One fixed canonical encoding with the public key inside**, as ED25519 has it (`type ||
  signature || public key`, 97 bytes, `base.SignatureDataED25519`): a BLS type would be, for
  example, 1 + 96 + 48 = 145 bytes; post-quantum schemes are fixed per parameter set too, bounded
  only by the transaction size limit. Variable-length encodings are out: ECDSA in DER form is both
  variable and malleable, so such a type uses the fixed 64-byte `r || s` with low S. A scheme that recovers the key from the signature (secp256k1
  ECDSA with recovery) cannot be used as it is: the key would be recovered from a signature over
  the ID, and the ID needs the key. Such a type carries the key explicitly.
- **The same holder ID in Go and in EasyFL.** The Go side (`base.SignatureFromBytes`,
  `Signature.HolderID`) derives the ID and is not part of the library; the EasyFL side
  (`signaturePublicKey`, `txHolderID`) is what locks check. Both are extended for the type, the Go
  side gated by `TxVersion`, so a transaction of the type is valid only from the upgrade that adds
  it, and a test keeps the two equal. An unknown type gives no ID, and the transaction is refused
  at parse.
- **A verify builtin and a dispatching `validSignature`.** Today `validSignature`,
  `signatureProper` and `signaturePublicKey` (`def_general_func0.json`) accept only ED25519; a
  library upgrade replaces them, same arity.
- **Not the VRF.** The branch bonus (`stemLock`) verifies an ECVRF proof under the signer's ED25519
  key (`vrfVerify(signaturePublicKey(txSignatureData), ...)`), so a chain signed with another type
  cannot branch until the bonus has a proof for that type. Mining no longer depends on it after this
  upgrade.
- **Wallet and key handling**, ED25519 throughout (`ledger.SigLock` from a public key, the
  keystore, the wasm wallet, the monitor's signer parse).

Shipped here rather than in a later upgrade because the mining work is the transaction ID: together,
the work function is defined once, and miners and seekers migrate once.

### 1.7 Also in this upgrade

Added 2026-11-07 from the ledger upgrade backlog. Each is an EasyFL constant or rule in
`ledger/def/upgrade6.easyfl`; the slot-dependent constants are filled from the slot the upgrade is
listed at (`ledger.BuildUpgrade6LibraryAt`).

- **Mining paused for 1,000 slots after the activation** (`constDisableMiningUntilSlot` = slot +
  `ledger.Upgrade6MiningPauseSlots`). Miners, seekers and wallets move to the new rules before the
  first transit under them. The existing start-slot check of `_mineShape` and the ingress gate
  enforce it, both reading the slot's library. The reference miner takes the start slot from the
  rules of the slot it mines for and waits (`successorSlot`). It also settles the difficulty at
  the switch (section 5).
- **Sequencer pace from 3 to 6 ticks** (`constTransactionPaceSequencer`): fewer milestones in the
  rush after the branch. Every reader (the parse-time pace check, the attacher, the ingress, the
  sequencer versions, the miner's transit tick) takes the constant from the library of its slot, so
  no Go change. The earliest transit tick moves from 83 to 80.
- **Two minimum own balances, separate constants so a later upgrade can move either.**
  - *Hard, for a new chain* (`constMinSequencerOriginBalanceProx` = 5,000 PROX,
    `_sequencerOriginMinBalance`): a sequencer chain whose origin slot is at least the upgrade slot
    holds it on its origin and on every later sequencer output, so one stake cannot be recycled
    into many chains a few slots apart. Since upgrade 5 a sequencer starts only at a chain origin
    and the origin slot is carried unchanged, so chains started before the slot are untouched.
  - *Soft, for every sequencer* (`constMinSequencerBalanceProx` = 10,000 PROX,
    `Constants.MinSequencerBalance` in motes): below it a sequencer cannot produce a branch or
    issue a milestone in the pre-branch zone (`_sequencerMinBalance`, on the produced sequencer
    output), nor freeze a delegation afresh (a replaced `_validLimitsProducedFrozen`, on the
    target chain's consumed input, the balance at the start of the milestone, since the inflation
    advances a freeze pays come out of it). A top-up continuing a freeze stays allowed
    (`_isContinuation`); a top-up of an unfrozen delegation freezes it and is refused. Below the
    soft bound a sequencer still issues milestones elsewhere in the slot and takes tag-alongs,
    and it regains everything at the bound.

  Measured on 2026-11-07: 59 of 83 active sequencers hold less than 10,000 PROX, with 2.14M PROX
  frozen to them (3.1% of all frozen coverage); their freezes run out within an epoch, are topped
  up meanwhile, and the consolidators then move them. Node side: a sequencer under the soft bound
  switches to no-branch mode and skips targets in the pre-branch zone (`belowMinBalance` in each
  sequencer version) and makes no fresh freeze (`SeqTxBuilder.BelowMinSequencerBalance`, in the
  freeze pass and the top-up of an unfrozen delegation). Wallet side: `SequencerCandidate.CanFreeze`
  keeps such a sequencer out of new delegations, the miner's top-ups of unfrozen delegations and
  `proxi node delegate`; the consolidator marks an unfrozen delegation at it stale and re-delegates
  it, and leaves a frozen one alone.

Not here: a smaller branch bonus counted several times in coverage. It needs a node-side change of
the coverage calculation gated on an upgrade, and a measurement first; planned for upgrade 7.

Tests: `TestUpgrade6MiningPause`, `TestUpgrade6SequencerPace`,
`TestUpgrade6SequencerOriginMinimumBalance`, `TestUpgrade6SequencerMinimumBalanceZone`,
`TestUpgrade6FreezeNeedsMinimumBalance`, `TestUpgrade6TopUpOfFrozenBelowMinimumBalance`,
`TestConsolidatorUnderMinimumSequencerBalance`, and `TestSuccessorSlotFollowsUpgradePause` in the miner.

## 2. Consequences in the code

| Where | Change |
|-------|--------|
| `ledger/def/upgrade6.easyfl`, `ledger/def_upgrade6.go` | the functions of 1.5 (shape, work, mint, payout), `constMinerVersion` 6; `BuildUpgrade6Library` on the library of upgrade 5, as `def_upgrade5.go` does; listed in `ledger.Upgrades` with its slot |
| `ledger/txbuildercore/sign.go` | `HashEssence` and `TxIDFromTree` by `TxVersion`: from 6 (`TxVersionSignerBoundID`) the holder ID first, then every element but the signature data after its 4-byte length, and signature data accepted only in its canonical encoding, an error otherwise (1.6); `SignED25519` takes the holder ID from its key. Shared by the node, `proxi` and the wasm wallet. **Built** |
| wallets: `proxi`, the wasm wallet, the consolidator, every signer | rebuilt on the new `txbuildercore` and released well ahead of the slot; nothing else changes for them (1.6) |
| `ledger/transaction/tx.go` | **Built.** `IsMiningTransaction` recognises a transit as one input, not a sequencer transaction, output 0 continuing the mine chain; **not by output count** (formerly 2 or 3), or a transit with more outputs would escape the floor gate, the registry, settlement and the deviation check. `MineWorkIsTxID` (the transaction version of upgrade 6 or later) chooses the work: `MineProofOfWork64` is the last 8 bytes of the txid, the VRF output before. `MineSolutionKey` (slot, work, remaining hash bytes) is the order of 2.3, compared as bytes. `MineVRFOutput` and `MinePayoutOption` are not read for a hash transit; the callers in `sequencer/mine` (registry, `TransitOf`, `checkLocked`, deviation) and in every sequencer version move in step 4 |
| `core/core_modules/txinput_queue` | **Built** for hash transits; VRF transits keep today's handling. The floor gate reads the txid: exact and free. **The unknown-sender exemption, today only for the small plain-output layout, covers every hash transit within 8 KB and 32 outputs** (`mineOpeningTx`): its one input is the mine output, and a new miner has no funds. The first-copy-of-a-solution gate steps aside: a solution is one transaction, deduplicated by its ID; copies with other signature data are handled as in 1.3. Load bounds (section 5): only a strict improvement by `MineSolutionKey` on the best validated transit seen for its predecessor and successor slot is stored, relayed and streamed, a later slot starting a new contest (`improvesBestMineTransit`); a transit on a predecessor not proven on this node, an older one included, is dropped, not stored, and a milestone that needs it pulls it. Core: run under `-race` |
| `sequencer/mine` | **Built, except two parts below.** The descriptor's work is `Work`: the VRF output, or `MineSolutionKey` without the slot for a hash transit, so one order (older slot, smaller work, then the rest) serves both (2.3). `Targets` is the set of chains a transit's tag-alongs address; a rival counts when any is an active sequencer (`addressedTo`), so a transit with no tag-along to an active sequencer never defers others. The registry keeps the best 16 hash transits per predecessor (`maxHashTransitsPerPred`). The deviation check walks the ordinary transactions of the milestone's slot too, and settlement ranks a tag-along of an ordinary transaction spending a hash transit's output as that transit (`Registry.TransitSpentBy`, every sequencer version); both only for hash transits, so VRF transits keep today's rule. **Not built:** following a transit back to its predecessor transit (a worse transit taken in under the next one), which needs the candidate's baseline state to tell a transit already settled from one newly taken in; and the deferral limit to the rival's own settlement window, unnecessary while a transit has one input (it cannot be made unincludable) and the miner builds on the best transit |
| `sequencer` backlog | reward tag-alongs, with or without requests, are ordinary tag-alongs once the transit is included |
| `api/monitor` | the miner is the signer, not the holder of output 1, which may be anything; the rewards table counts A per settled transit; a new figure: settled transits that were not the canonical winner, by sequencer (section 4.1) |
| `proxi/node_cmd/mine` | the reference miner of 2.4: the template, the stored hash state, the per-slot switch from VRF to transaction-hash work, the one-payout choice (own sequencer, top-up, new delegation with an inclusion fee), signing only a solution. Payout options and their pricing go |
| seeker protocol, `nonce_seeker/` (Rust) | version 2 of the job protocol (2.4): message bytes, nonce offset, K, nonce range; **no key**. The Rust seeker drops the VRF and the keystore and hashes with a SIMD BLAKE2b |
| `util/vrf` | stays for the branch inflation bonus; the split prover and its Rust twin serve only mining and can go after the slot |

Size: the VRF proof (80 bytes) leaves the transit; the reference template (one tag-along) is about
600 bytes instead of 700, each further output adds 50 to 80.

### 2.1 Off-the-shelf hashing

A seeker needs to know nothing about Proxima: it hashes a byte string with an 8-byte nonce at a known
offset under BLAKE2b-256 and tests the trailing zero bits of the last 8 bytes. Existing BLAKE2b GPU
kernels (written for other chains' block headers) need only a different message length, a stored
starting state and a different target test, not a new design. That is a feature for reviewability
and a cost for hardware fairness (4.1).

The hash is BLAKE2b-256 as in RFC 7693: unkeyed, digest length 32 in the parameter block, no salt
or personalisation, the byte counter and the final-block flag as the RFC sets them. Keeping the
state up to the nonce is only an optimisation: whatever the nonce's position relative to a 128-byte
block boundary, a miner may also batch changes to the nonce bytes in an earlier block and amortise
that compression. The optimised Go and Rust paths are tested against plain full hashing on vectors
placing the nonce before, across and after a block boundary, and on messages ending exactly at a
boundary, where the last full block is the final one.

### 2.2 Indirect inclusion and eligible rivals

Section 1.3 lets a transit pay no sequencer. It still reaches the ledger when any transaction a
milestone includes depends on it: the miner spends a plain reward in an ordinary transaction that
pays a sequencer, or the next mine transit consumes it as its predecessor. Today's code does not see
these paths: `InspectInclusions` (`sequencer/mine/deviation.go`) walks the slot's milestones and
inspects only transits they consume directly, skipping ordinary transactions and never following a
transit back to its predecessor transit; the registry keys every transit on the one sequencer its
tag-along targets.

The same blind spot exists today: a worse transit whose reward is spent in an ordinary tag-along
transaction to an honest sequencer enters that sequencer's milestone as an ordinary transaction, and
neither settlement nor the deviation check notices.

**Seeing every path.** Settlement and the deviation check compare the mine chain output in a
candidate's (or milestone's) past cone with the one in its baseline state, and walk the mine chain
back from the first to the second, applying the order of 2.3 to every transit on the way. A transit
taken in through an ordinary transaction, or under the next transit, is then ranked like one taken
in directly.

Built so far (step 4): the ordinary-transaction path, in settlement and in the deviation check, for
transaction-hash transits. The path under the next transit is not built yet: telling a transit
newly taken in from one already settled needs the candidate's baseline state.

**Eligible rivals.** A transit defers others only if it can be included directly by a sequencer
that is expected to: it pays a tag-along to an active sequencer. And it defers them only until the
settlement window of its own slot ends, not across later slots. A transit with no such tag-along
still lands when someone includes it, and wins the slot if it is the best one actually included; it
just does not stop anyone else. Without these two limits, the maker of the best solution could leave
it where no sequencer takes it (no tag-along, a tag-along to an inactive chain, or to an own
sequencer that does not include it) and empty the slot each time it wins the work, or keep the
predecessor blocked for as long as rivals are remembered.

### 2.3 The order of solutions

The work is the last 8 bytes of the txid with at least K low bits zero, so only 64 - K bits tell
qualifying solutions apart: at the ceiling K = 56 (`constMineMaxDifficulty`), 256 values. Ties are
therefore not rare. Today's `betterSolutionThan` (`sequencer/mine/transit.go`) leaves out the txid
tie-break because one VRF solution could appear in different transactions; with the txid as the work
a solution is one transaction, and that reason is gone.

One complete order, used by the registry, the settlement rule, the deviation check and the miner:
older slot, then smaller work value, then the remaining hash bytes of the txid (those not in the
work and not overwritten by the timestamp and output count), compared as bytes. Metadata is not used
for ties: a miner chooses its timestamp and output count, not the hash.

### 2.4 The reference miner and seeker

The ledger fixes only the work; the reference miner fixes a template. It ships as two programs, as
today: `proxi node mine` and the Rust nonce seeker. The recommendation to miners stays the same:
keep `proxi node mine`, which places every reward and costs next to nothing beyond the search, and
replace only the seeker if a faster one is wanted (`kb/external_nonce_seeker.md`).

**The template.** What `proxi node mine` builds for each attempt round:
- **Inputs**: the mine output alone, so mining needs no funds and the unknown-sender exemption
  applies (section 2).
- **Outputs**: [0] the successor, then **one payout**, chosen before every round as today
  (`choosePayout` and `placeReward`, `mine_payout.go`, by the consolidator's placement rules and the
  `consolidate` section of the profile), with the same targets but without the prices:
  1. **To an own sequencer**, when one is configured and active: one tag-along of A to it, with no
     request. The payout is its own inclusion payment.
  2. **A top-up of an own delegation**: one tag-along of A to the delegation's target, carrying the
     top-up request (code 4, `kb/delegation_topup.md`). Its own inclusion payment again, for the one
     sequencer that can take it; the sequencer's minimum top-up (`MinTopUp`, floor 90 PROX) is
     below A. First the smallest delegation below the target size, a pinned target first.
  3. **A new delegation**, while there are fewer than the target count: a delegation origin with the
     signer as master and the target drawn by the rating (`sequencer_rating.md`). It is not a
     tag-along, so no sequencer would include the transit for it: the transit adds an inclusion fee,
     a tag-along to the new delegation's target sequencer, as today. The fee is the wallet's: the
     larger of `tag_along.fee` (in motes; 1 in the generated profile) and the minimum fee that
     sequencer declares. The delegation gets A minus the fee instead of today's 50 PROX.
  4. Otherwise a top-up of the smallest delegation that can take one, and failing that a new one.

  Today's opening (a small `sigLock`) goes: it existed so that an unknown account could pass the
  node's unknown-sender gate, and every transit now passes it, whatever its outputs. A miner who wants liquid tokens sets the payout to a `sigLock` to an address of its
  choice (`mine.payout_address`, below), with the same inclusion fee. A sequencer that is down or let a transit stall is replaced as
  today (`excludeStalledTargets`).

  **The reward must be taken, not only the transit.** A transit can be confirmed without its reward
  tag-along being consumed: it lands under the next transit while its target, which can raise its
  minimum top-up above A, fill its freeze caps or simply ignore requests, never takes the request.
  An unconsumed tag-along goes back to its sender only in the sender's own window, from 30 slots,
  and anyone may take it from 390. So the miner checks, for every confirmed transit, that its reward
  tag-along was consumed by its target; if not, it excludes that target from its choice and reclaims
  the tag-along itself as soon as its window opens, without relying on the optional consolidator. A profile list of several outputs, for pools, is a later
  extension of the same template; the ledger already accepts it.
- **The nonce**: 8 bytes of inline data as `TxConstraints[0]`, the last element hashed, so an attempt
  compresses only the final one or two 128-byte blocks whatever the payout. Every element of
  `TxConstraints` is run as a transaction-level constraint and must return a non-empty value
  (`validateTxLevelConstraints`, `ledger/transaction/validate.go`); inline data returns its own
  bytes, so any nonce passes, all zeros included.
- **The mine input's unlock parameters** empty, **the timestamp** at tick 83, the earliest the
  settlement rule of upgrade 1 allows.
- **The signer's holder ID** is hashed first (1.6), so it is part of the stored state, not of each
  attempt. The miner signs only a solution it submits.

**Built so far (step 5a, `proxi/node_cmd/mine`):** miner version 6; the switch by the rules of each
slot (`Constants.MineWorkIsTxID`); the template (`buildTransit`, O written as 1, the mint always A, a
new delegation of A minus the fee plus the fee tag-along); the search on a saved hash state
(`hashTemplate`, built from `txbuildercore.SignerBoundPreimage` and checked to end with the nonce);
signing only a solution, with an assertion that the signed ID carries the work found; the tree and
the round ranking by the same work bytes (`work`, the slot-free `MineSolutionKey` under hash work);
verification of streamed hash transits (`verifyMineTransit`: outputs free, the ID's work at K). The
opening layout is no longer chosen under hash work. Seekers get no jobs under hash work until
protocol version 2 (step 5c). Tests: `mine_hash_test.go`.

**Built (step 5b):** the inclusion fee under hash work, the larger of `tag_along.fee` and the target's
minimum fee (`minePayout.fee`); `mine.payout_address`, a `sigLock` of A less the fee to that holder,
the fee as a tag-along to the `tag_along.sequencer_id` sequencer or one drawn by the rating while it
is not active; the reward watch (`mine_reward.go`): the reward tag-along of every confirmed own
transit paying a tag-along (full reward or top-up, under VRF work too) is looked up every 20 s; a
target that has not taken it 3 slots after the transit's slot is left out of the choice for the
reclaim window, and once the sender's window opens (`TagAlongSlots`) the miner reclaims it, the
reward back to itself less the fee. Tests: `mine_reward_test.go`.

**Built (step 5c):** seeker protocol version 2 on the same endpoints (`kb/external_nonce_seeker.md`,
section 3.7): a job of `version: 2` carries the ID preimage without the nonce (`message`,
`nonce_offset`), K and the beat as a 26-byte work key; the miner verifies a result from its own
template. A version 1 seeker refuses such a job, which names no key. The Rust seeker takes both:
version 2 without a key on `blake2b_simd` (a hash state per job, cloned per attempt), version 1 only
with a key. Shared vectors: `mine_hash_vectors_test.go` and `GO_VECTORS` in the seeker, messages
ending before, on and after block boundaries. `TestNonceSeekerBinaryHashWork` runs the built binary
keyless against the miner's server. Not yet: a cross-check that the ledger accepts a miner-built
transit, and measured hashrates (the rehearsal).

**Configuration: today's profiles work unchanged.** The miner reads the keys it reads today, with
the same meaning:

| Key | Meaning, unchanged |
|-----|--------------------|
| `wallet.key_file`, `wallet.sequencer_id` | the signing key; the own sequencer, which `send_to_sequencer` must name to be paid |
| `consolidate.send_to_sequencer` | mode 1, the full reward to the own sequencer while it is active |
| `consolidate.autodelegate` | the pinned delegation target, or `random` for the rating |
| `consolidate.target_delegations`, `consolidate.target_delegation_prox` | the count and size of the delegation set, modes 2 to 4 |
| `tag_along.sequencer_id` | where the inclusion fee goes, `random` for the rating |
| `mine.sequencers_refresh_slots` | how often the active sequencer list is refetched |
| `mine.seeker.listen`, `.token`, `.spawn`, `.binary`, `.threads` | the seeker job server and a spawned seeker; a spawned or external seeker must be the version 2 build from the slot |

Changed under hash work: the inclusion fee, a ledger constant today (`constMineTagAlongFee`, 1 PROX),
becomes the wallet's rule, the larger of `tag_along.fee` (in motes) and the target's declared minimum
fee. A generated profile carries `tag_along.fee: 1`, one mote, so the sequencer's minimum decides.
New and optional:
`mine.payout_address`, a holder ID; when set, the payout is a `sigLock` to it instead of a delegation
placement. A profile without it behaves as today. No key is removed.

**`proxi node mine`, Go.** Keeps everything but the work loop: following the mine chain on the
mining stream, the rules of each slot it builds for (`constsAt`, `mine_upgrade.go`, switching from
VRF to transaction-hash work at the slot), the payout profile, signing, submitting each improvement
until the settlement tick, and the job server for seekers. Its built-in workers keep the BLAKE2b
state of everything before the final block and copy it per attempt: `golang.org/x/crypto/blake2b`
supports `MarshalBinary`/`UnmarshalBinary`, so no hash code of our own is needed.

**The seeker, Rust.** `nonce_seeker/` drops the VRF and the keystore and needs no key, so it can run
on hardware the miner does not trust with its key. It hashes with `blake2b_simd` (SIMD, a clonable
state). A GPU seeker is left to others; the protocol is published for it.

**Seeker protocol, version 2.** A job carries the full message bytes (about 600), the offset of the
8-byte nonce in them, K, and a nonce range; the seeker returns a nonce and the hash it found, and the
miner checks both before signing. The job carries bytes rather than a hash state, since states are
library-specific; a seeker computes its own once per job. From the activation slot the miner sends
version 2 jobs only; a version 1 (VRF) seeker gets none and says so.

**Tests shared by both.** Go and Rust run the same vectors (as `TestRustEquivalence` does for the VRF
and the keystore today): the nonce before, across and after a block boundary, messages ending exactly
at one, a solution found by the seeker accepted by the covenant.

## 3. Pools

A pool exists to smooth income: a solo miner is paid rarely and unpredictably, a pool pays each
worker steadily for its share of the search. Workers prove their search by sending **shares**,
solutions to an easier target on a template that pays the pool; now and then a share meets the real
target and is submitted. Because every payee is inside the hash, a worker cannot redirect a full
solution; the only cheat open to it is withholding one, which harms the pool, not the network.

**The natural shape is a pool that runs a sequencer.** Its transits pay a tag-along to its own
sequencer, which is at once the inclusion payment and the pool's income, to be shared out among the
workers. Its income sits on its own chain, in the open. It settles by the canonical rule like any
reference sequencer: if its transit is not the best one in the slot, it loses like anyone else.

What the free layout allows, from most to least trust in an operator:

| Model | Reward outputs | Who holds what | Precedent |
|-------|----------------|----------------|-----------|
| Pool with a sequencer | one tag-along of A to the pool's sequencer | the pool holds its income on its chain until it pays the workers | most pools on proof-of-work chains (custodial) |
| Finder plus pool cut | the finder's output, a tag-along of the cut to the pool | the finder is paid at once; the pool holds only its cut, which it shares out among everyone who sent shares, or keeps as a fee. Any cut size: a tag-along pays no deposit | "solo" pools on Bitcoin (one fee output to the operator) |
| Direct payout | the pool's cut and the recent share-holders, as many as the transaction size allows | nobody holds the workers' tokens; a running tally of what each worker is owed decides who is paid in each transit | P2Pool on Monero, OCEAN on Bitcoin (payouts in the reward transaction) |
| Solo | one tag-along to a sequencer, the rest to the miner | the miner | |

Either side may build the template; the pool only checks that its outputs are in every share's
template.

**Small payments.** A worker is paid by plain `sigLock` only once what it is owed passes the 9.25 PROX
deposit; the tally carries the rest to a later transit, so small workers wait longer but lose
nothing. A worker with a chain of its own can be paid any amount by tag-along. A pool paid by
tag-along must consume it within 30 slots, or the finder, as its sender, can take it back; a pool
running a sequencer does so at once.

The existing job protocol by which `proxi node mine` hands its search to external seekers over HTTP
becomes a pool protocol once seekers need no key; a pool adds a share endpoint and its accounting.

**Economics.** A pool is paid in PROX, like sequencers and miners, so it stands with them: its
income is worth what PROX is worth. A pool's running costs, or the hardware it rents, are paid in
other money, so a pool makes sense only once PROX has a price. Until then solo mining on hardware
people already own prevails, since its only cost is electricity. What pools could concentrate
grows with PROX's value, not ahead of it.

## 4. Downsides

### 4.1 Exploits

- **Settlement deviation, and with it paid inclusion.** Which competing transit wins a slot is
  sequencer policy, not a ledger rule: the canonical winner (older slot, then smaller work value).
  The reference sequencer applies it whatever a transit pays. A modified sequencer can include
  another transit instead, its own or one that pays it more, and that transit wins the slot whenever
  the sequencer's branch wins. The slot's emission then follows branch wins rather than hashrate,
  and among tied branches the random branch bonus decides, so the incentive is to run more
  sequencers, not more hashrate.

  A fixed fee would not close this. A miner can pay a sequencer off-chain or by a separate
  transaction anyway; a fixed fee only stops the payment inside the transit, which is the kind that
  is trustless and on the record. Nor does a payment inside the transit buy emission with outside
  capital: it comes out of the reward the miner's own work earned, so it needs a valid solution
  first. The defence is the settlement policy and its measurement: each sequencer already records
  every validated competing transit, so a settled transit that was not the best one it observed is
  visible, and the monitor reports how often and by whom (section 2).

  The measurement is not proof. A node that saw a better solution cannot show that the settling
  sequencer received it before the deadline. Reports say "deviation from the candidates this node
  observed", never that a sequencer cheated. Changing the work function leaves this incentive as it
  is: as long as preferential inclusion pays, running a sequencer to mine pays. It is the largest
  open question of this option, not whether BLAKE2b is a suitable work function.
- **Pools, and capital gathered by them.** Pooling needs nothing from the key. A pool holds its
  income between payouts and decides, while it holds it, whether to delegate it; once paid, each
  worker decides. The free layout lets workers avoid custody (section 3).
- **The usual pool risks**, off-chain: a custodial pool can cheat its workers, workers can withhold
  full solutions while sending shares.
- **Dust from small tag-alongs.** A tag-along pays no storage deposit (`ledger/sdeposit.go`), so a
  transit may split A into tag-alongs of a few motes. Bounded by the transaction size limit, one
  transit per slot, each reclaimable by its sender after 30 slots and by anyone after 390: what
  anyone can already do with their own funds.
- **Emission can be bought with rented hashrate.** It cannot reorder the ledger, since the mine chain
  takes no part in consensus, and liveness is unaffected when it leaves: K falls to the floor as the
  slot gap grows.
- **Custom hardware becomes worthwhile sooner.** *Estimate*: the GPU lead per card over an 8-core CPU
  grows by about an order of magnitude beyond the VRF's 10x. BLAKE2b chips exist for another chain's
  80-byte header; whether they can be reused for this message is not verified.

No other exploit is specific to this option: an attempt costs at least one compression of the final
block in any layout (1.2), so no layout makes it cheaper; every payee, chain and amount is in the hash, so no redirection; the
signer is in the hash, so a re-signed copy is another transaction without the work (1.3, 1.6).

**Reviewed 2026-11-07 for halting, theft and validity splits.** What the review found, and where
the spec closes it:
- a best transit made impossible to include after publication, by spending an extra input elsewhere,
  emptying slots: one input (1.2);
- a best transit left where no sequencer takes it, emptying slots or blocking the predecessor:
  eligible rivals only, and only until their own slot's settlement window ends (2.2);
- a worse transit taken in through an ordinary transaction or under the next transit, unranked and
  unflagged: settlement and deviation walk the mine chain from the baseline (2.2);
- a transit not recognised as one because of its output count, escaping every mining check:
  recognition by role, not shape (section 2);
- one ID, one encoding: the essence with element boundaries, and canonical signature data checked
  alike on the Go and the EasyFL side (1.6);
- V and O written as expressions on the successor, misread by every parser: pinned inline (1.5);
- a reward tag-along its target never takes, open to anyone after 390 slots: the miner checks and
  reclaims it (2.4);
- unbounded ingress and registry load at activation or after a difficulty drop: strict improvements
  only, a bounded registry, transits on unproven predecessors dropped, size caps (section 5).
Checked and closed: re-signing by another key, redirecting a solution, minting more than A, a second
mine output, unsigned transactions, transactions straddling the activation slot, consuming a transit
before the settlement window.

### 4.2 Strategy

- **Mined tokens land where the miner sends them; delegating them is the holder's choice.** The
  priced layouts of today exist so that mined tokens back sequencers. Here nothing is priced and
  nothing is forced. A smaller loss than it looks:
  - **Forced delegation can be escaped anyway** (section 0, B), so forcing it only charges a price to
    those who would not delegate.
  - **A tag-along with a top-up request** puts a reward into the miner's own delegation from the
    transit itself.
  - **The tooling exists.** `proxi node consolidate` is a permanent wallet process that sweeps an
    account's plain outputs, once above its threshold, into delegations to sequencers chosen by a
    rating, topping up and re-delegating as needed. A miner running it next to `proxi node mine`
    delegates every plain reward without further thought.
  - **Holders know why to delegate**: delegation is how capital earns.

  - **The reference miner delegates by default**: it keeps today's placement (own sequencer,
    top-ups, new delegations, 2.4), and only a miner who changes it pays itself otherwise.

  Delegation becomes a default the miner opts into rather than a rule, so the share of mined supply
  in consensus depends on how miners pay themselves: to be measured after activation.
- **Sequencers are paid what miners choose to pay**, not a fixed 1 PROX. The reference miner pays
  for inclusion with the payout itself (a tag-along to an own sequencer or a top-up), and adds the
  configured fee only for a new delegation or a `sigLock`.
- **The plainest proof of work**: one standard hash, nothing Proxima-specific in the work. Easy to
  explain and audit. The fair launch becomes "hardware buys emission", stated plainly.
- **Mining from nothing works as today**: no funds are needed to send a transit.

### 4.3 Behaviour to expect

- Pools run sequencers and take most transits; solo miners pay a sequencer of their choice.
- Workers of pools that pay directly are paid in turns, by tally.
- GPU farms take most hashrate.
- Whether sequencers deviate from the canonical winner is the figure to watch (4.1).

## 5. Difficulty at the switch

On 2026-10-13 the network ran about 8.5 MH/s of VRF attempts. The same hardware does 150 to 500 times
more BLAKE2b attempts on a CPU, and GPUs gain more than CPUs (a VRF attempt is heavy elliptic-curve
arithmetic, a hash is light integer work). Expected jump: **about 9 bits for a CPU fleet, likely 12
or more with GPUs** (*estimate*).

The retarget hardens one bit per 8 full slots, so 12 bits take at least 96 slots, about 16 minutes.
Meanwhile:

- **The cap on emission is unchanged**: one transit per slot whatever the difficulty. Actual
  issuance is not: every transit now mints the full A, while today the opening and new-delegation
  layouts mint less (`kb/mine_payout_options.md`). Issuance rises by however much miners used those
  layouts. That, the free payout and optional delegation are deliberate economic changes, separate
  from replacing the VRF.
- **Every slot is full and contested.** The winner is the first solution in the order of 2.3 that
  the sequencers hold at the settlement tick, a draw weighted by hashrate.
- **Honest load is small.** An honest miner submits each improvement on the best solution it knows,
  so a slot sees a few extra transits network-wide. This is not a bound against an adversary.

**Load under attack.** The ingress already checks the floor before the signature, validates in full
against a proven predecessor before relaying, and does not relay a transit whose predecessor is not
proven. What remains: the floor (`constMineFloorDifficulty`), raised by this upgrade from 10 to 24 bits
(below), still costs only about a second of one CPU, signature checks
and validation still cost, a transit on an unproven predecessor still reaches the transaction store
and is attached, the registry of a predecessor grows without bound, and a transit may be as large as
any transaction. At activation, and whenever K has fallen far below the hashrate, honest miners alone
find thousands of valid solutions per slot. The bounds of section 2: relay and register only a strict
improvement on the best transit known for the predecessor (a few per slot, not one per solution),
keep the best few per predecessor in the registry, drop a transit on a predecessor not proven on
this node instead of storing it (a milestone that needs it pulls it), and cap by policy the size and
output count of a transit taking the unknown-sender exemption (8 KB, 32 outputs). Built in the input
queue, except the registry bound, which is step 4. Limits per signer are useless: keys cost nothing. The upgrade
ships with flood tests showing bounded CPU, memory and storage, including across the activation
slot, when the floor is far below the live difficulty.

No reseed rule. The pause of 1.7 decides the start: the first transit after it carries a gap of
about 1,000 slots, so B eases to the floor of 24, then hardens one bit per 8 full slots. At an
equilibrium around 36 bits that is about 100 slots, 17 minutes, every one of them a full slot paying
one transit, a draw weighted by hashrate. Live B was 35 to 37 on 2026-11-07, so without the pause the
switch would have started near the hash equilibrium anyway.

**Measured 2026-11-07** on one AMD Ryzen 7 PRO 8700GE (8 cores, 16 threads), a 600-byte preimage:
the Rust seeker 10.5 M attempts/s on one thread and 99 M/s on all 16; the Go miner's own workers
7.5 M/s per core (133 ns an attempt, no allocation, the hash state restored per attempt). Of the
difference, BLAKE2b itself is about 1.2 times faster in Rust (95 against 113 ns for the one final
block an attempt hashes); the rest is the state restore and Go's interface calls. One such machine thus
outruns the whole VRF network of 2026-10-13 (8.5 M/s) about tenfold. A hundred of them, 10 G/s,
would settle K near log2(10^10 x 10.24) = 36.6 bits; GPUs not measured.

**The floor at 24 bits** (built). With hashing, a 10-bit solution costs about a thousand hashes,
microseconds, so the VRF-era floor no longer keeps junk out of the ingress floor gate. The upgrade
raises `constMineFloorDifficulty` to 24: about 16,000 times dearer per junk transit, while 2^24
attempts still take one CPU about a second, so the chain stays solvable when slots go empty.
Honest difficulty after the switch is expected around 35 bits (*estimate*); live B was 37 on
2026-11-07, read from the mine output. B can be below the raised floor at the switch (it falls one
bit per empty slot), so `_mineRequiredK` and `_mineEased` are replaced to compare B with
`floor + relief` instead of subtracting the floor from B, which would fail every transit and halt
the chain; K is then the floor. The Go mirror `MineRequiredK` does the same. Test:
`TestUpgrade6DifficultyFloor`. The other ledger tests run upgrade 6 with the floor patched back to
the test ledger's 6 (`ledger.BuildUpgradeFromSource`), since 24 is above the test band [6, 10].

## 6. Tests

`ledger/tests/library_upgrade6_test.go`:

- a VRF transit valid before the slot, refused from it; a transaction-hash transit refused before,
  valid from it;
- from the slot, valid: the reference template; one `sigLock` of A; a tag-along and a `sigLock` to
  another holder; a tag-along with a top-up request; many outputs; a delegation origin; a second
  the nonce in the mine input's unlock parameters instead of `TxConstraints`; no nonce element at all
  (the search varying something else);
- refused: a second input spent alongside the mine output; a sequencer transaction consuming the mine
  output (a milestone carrying the transit); V or O written as an expression on the successor;
  outputs not adding up to A (the balance rule), a `sigLock` under the storage
  deposit (the general rule), a payout changed after solving (the work is gone), too few zero bits,
  a transit re-signed by another key (another ID, the work gone), a transit stamped before the
  settlement tick minus the sequencer pace;
- `MineProofOfWork64` equals what the covenant tests, on both sides of the slot;
- the transaction ID (1.6): any transaction of version 6 has the ID with the holder ID first, one
  of version 5 the ID without; a transaction signed under the old rule is refused from the slot and
  valid before it; the same transaction signed by two keys has two IDs; the ID of version 6 equals a
  plain BLAKE2b-256 of the holder ID and the length-prefixed elements (built); signature
  data not in its type's canonical encoding is refused, by Go at parse and by EasyFL alike;
- the guarantor fix (`TestUpgrade6FakeDelegation`, built); the library carries miner version 6 from
  its slot (`TestUpgrade6Library`, built).

`core/core_modules/txinput_queue` (`-race`): a transit with four or more outputs recognised as a
transit; the unknown-sender exemption for every transit, within the size and output caps; only
strict improvements relayed; a transit on an unproven predecessor held in memory, not stored; the
floor gate; a copy with the same txid and other signature data arriving first does not suppress the valid
transaction (a regression test for every transaction type, not only transits); floods of
floor-meeting transits, on proven and unproven predecessors and across the activation slot, keep
CPU, memory and storage bounded.

`sequencer/mine` (`-race`): a transit addressed to several sequencers; a transit included through an
ordinary transaction spending its reward, and through the next mine transit, is seen by settlement
and the deviation check; a best transit with no tag-along, or one to an inactive or silent sequencer,
does not defer others, and no rival defers past its own slot's settlement window; the registry keeps
only the best few per predecessor; a hash transit across the activation slot is included by every
sequencer version (`v1`, `v2`, `v3`); ties on the work value
broken by the remaining hash bytes, the same order in registry, settlement, deviation and miner.

`ledger/txbuildercore`: `TxIDFromTree` against a plain BLAKE2b-256 of holder ID and elements, for
both versions; the wasm wallet build signs a version 6 transaction the node accepts.

`proxi/node_cmd/mine` and `nonce_seeker/` (2.4): the stored hash state and one-to-two-block attempts
against plain BLAKE2b-256 on vectors around block boundaries (2.1), the same vectors in Go and Rust;
the version 2 job format and a solution from the Rust seeker accepted by the miner and the covenant;
the switch from version 1 to version 2 jobs at the slot; the payout choice in each case of 2.4 (own
sequencer, top-up below the target size, new delegation under the count with its inclusion fee, top-up
of the smallest, a stalled target replaced), the transit of each case accepted by the covenant; a
confirmed transit whose reward tag-along its target never took: the target excluded and the reward
reclaimed at 30 slots.
A profile generated by today's `proxi` mines unchanged across the slot; `mine.payout_address` switches
the payout to a `sigLock`.

## 7. Open

- The rehearsal (section 8) before listing the upgrade.
- **A genesis folding upgrade 6 gets the old rules.** The transaction ID (1.6) switches on the
  transaction version, and the mine work on `MineWorkIsTxID`, both at 6. The version is the
  upgrade index counted from the genesis library, so a new genesis folding upgrades starts again
  at 0: folding 1 to 5 puts upgrade 6 at index 1, folding all six leaves the index at 0. Its nodes
  then compute the old ID under the new mine covenant, and no transit is valid. The take 1 network
  folds none, so the index there is 6. To decide before any new genesis folds it: switch on the
  upgrade number (folded plus index) instead of the version, or let the version count the folded
  upgrades. Found by the rehearsal.
- Measure after activation how often a settled transit is not the canonical winner (4.1), and what
  share of mined rewards ends in delegations (4.2).
- The lead time of the wallet release before the slot (1.6): long enough for every user of `proxi`
  and the wasm wallet to upgrade.
- Settlement incentives (4.1): whether anything beyond measurement is needed if deviation is
  observed.

## 8. Rehearsal

Three layers, each catching what the one before cannot. The upgrade 1 rehearsal ran one sequencer
and missed the competing-branch case at the slot; this one runs every sequencer version, several
miners and the cases of the security review.

1. **In-process, in the repository** (`tests/`, `-race`): sequencers `v1`, `v2` and `v3` producing
   competing branches across the slot; hash transits from the end of the pause, included by every
   version; wallet transfers on both sides of the slot.
2. **A local network on the dev box** (kit outside the repository): a fresh genesis folding
   upgrades 1 to 5, upgrade 6 listed a few minutes ahead by a scratch patch; sequencers of the
   three versions with different capital; an access node serving miners; a node syncing from
   genesis after the slot, one restored from a snapshot taken before it, one on the previous
   release; miners with built-in workers, with the Rust seeker (keyed before, keyless after), with a
   payout address, and one of version 5. Checked by script: activation and one lineage on every
   node, transits settled after the pause, the old node and miner stopping rather than forking, the
   ingress counters, the floor; and the abuse cases of section 4.1, each with its expected result.
3. **Before listing:** the activation slot and the wallet release lead time.

**Results, 2026-11-07.**
- **Layer 1** (`tests/upgrade6_sequencers_test.go`, `-race`, about 9 minutes): sequencers
  `v1`, `v2`, `v3` and the bootstrap producing branches across the slot, every branch holding the
  upgrade UTXO, no BAD milestone; hash transits after the pause settled, each version including
  transits addressed to it; transfers confirmed on both sides of the slot.
- **Layer 2** (hdev, nine nodes on 10.24 s slots, genesis folding nothing, upgrades 1 to 5 at
  slots 10 to 90, upgrade 6 at 150, pause shortened to 30 slots): all ten checks passed.
  Activation on every node and one lineage on all seven synced nodes (one from genesis, one
  from a snapshot taken before the slot); v1.5.3 and the version 5 miner stopping at the slot;
  109 VRF transits before it, none in the pause, transits in 35 of 43 slots after it at
  difficulty 25 to 28. New sequencer of 5,000 PROX refused, of 10,000 accepted. The abuse cases
  of section 4.1 and the copies of an ID behaved as expected. The refused top-up was not
  reached (no miner held a delegation at that sequencer); the reward watch is covered by unit
  tests only.

**Bugs found and fixed:**
- The reference miner computed K and the retarget with the constants read at start, not the
  rules of the slot mined for; across the floor raise every transit would have been refused.
- A sequencer validated a transit against its predecessor's stored transaction, so a transit
  spending the genesis mine output, which no stored transaction produced, was never validated:
  a fresh mine chain could not start. The registry now reads such an output from the latest
  reliable state.
- The ingress gate of strict improvements ranked any later slot below the best transit on a
  predecessor, so a best transit nobody settles would have blocked honest transits on that
  predecessor for about 30 slots. Each successor slot is now a contest of its own.

Not fixed: the folded genesis of section 7.
