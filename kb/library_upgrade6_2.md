# Library upgrade 6.2: transaction-hash proof of work, reward paid as the miner decides

> SPEC, NOT BUILT. Draft for review, 2026-11-05. A library upgrade: new validity rules from an
> activation slot, no network reset. Prerequisite: library upgrade 5 active (slot 153000). Miner
> version 6. Figures marked *estimate* are reasoning, not measurement. Second review 2026-11-06
> folded in: settlement and indirect inclusion (2.2, 4.1), the order of solutions (2.3), the hash
> specified with its test vectors (2.1), copies sharing a txid (1.2), load bounds and emission (5).
>
> **What this upgrade claims.** It removes the known key-dependent shortcut and adopts a
> conventional proof of work bound to the payout. It does not make reward allocation fair by
> itself: the hash decides which solutions qualify, settlement decides who is paid, and settlement
> is sequencer policy (4.1).

## 0. Context

Proxima emits part of its supply through the **mine chain**: a single chain output that anyone may
spend once per slot (10.24 s) with a proof of work. The spending transaction, a *transit*, consumes
the mine output, produces its successor and mints a reward of A (95 PROX today, rising later by
schedule); today 1 PROX of it is a fixed inclusion fee to a sequencer. Difficulty B is retargeted on
chain; competing transits for one slot are settled by the sequencers, the smaller work value winning.

A **tag-along** is an output addressed to a chain, usually a sequencer: its sender is always the
transaction's signer, the target chain may consume it within 30 slots, the sender may take it back
after that, and anyone after 390 slots. It may carry a request to the sequencer (for example, to top
up a delegation). A sequencer includes a transit by consuming a tag-along addressed to it.

**Today's work** is the trailing-zero count of an ECVRF output (RFC 9381) computed under the miner's
own key over (predecessor output ID, slot, nonce). It was chosen to make mining non-outsourceable:
every attempt needs the private key, and the reward must go to that key's owner, in one of three
priced layouts (a small plain output, a new delegation, or a tag-along to a sequencer).

**The problem.** An attempt costs a scalar multiplication `x*H` under the secret scalar `x`. A miner
choosing `x = 1` skips it and runs 5-10 times faster; a scalar of 96 to 128 bits gives an
estimated 1.6 to 2 times, and nobody can tell such a key from an honest one. A scalar-1 key is
public, so its rewards should be up for grabs, but a miner running its own sequencer includes its
own transits and spends the reward in the same slot, before anyone can race for it. The result is a
5-10x edge for those who cheat and a reason to run a sequencer just to mine (68 sequencers on
2026-11-05, rising).

**Three remedies were considered:**

- **A.** Keep the VRF and pin every reward output so that it lands as a delegation. Does not remove
  the speedup or reach medium keys, and the own-sequencer shelter remains.
- **B.** Replace the VRF by `blake2b(predecessor ID, slot, signer public key, nonce)`, keeping A's
  forced delegation. Removes every key trick, but the forced delegation can be escaped (an unserved
  top-up request is reclaimed by its sender after 30 slots; a new delegation can be withdrawn at
  once, since a delegation origin cannot be frozen), so the forcing only charges a price.
- **C, this document.** The work is the transit's own transaction ID. The reward goes out in up to
  eight outputs, plain `sigLock` outputs or tag-alongs, in amounts the miner chooses; there is no
  fixed fee and nothing is forced.

## 1. The rule

From the activation slot a mine transit is valid when, besides the successor rules of today (the
successor pinned in full, R decremented, B and C retargeted):

- **Work**: the last 8 bytes of the transaction ID, read big-endian, end in at least
  `K = _mineRequiredK(B, M)` zero bits (the existing `_minePoWOK`). M is the slot gap from the
  predecessor; K is B at a gap of one slot and eases one bit per extra slot.
- **Nonce**: the transaction's only transaction-level element, `TxConstraints[0]`, an inline-data
  bytecode of exactly 8 bytes. The mine lock's own unlock parameters are empty.
- **Mint**: exactly A, always. The successor's inflation is A and R falls by A.
- **Layout**: output 0 is the successor; outputs 1 to k, 1 <= k <= 8, are the reward:

| Reward output | Elements | Amount |
|---------------|----------|--------|
| a plain `sigLock`, to any holder | 3: amounts, index values, `sigLock` | free, at least the storage deposit (9.25 PROX) |
| a tag-along, from the signer to any chain, with or without a request | as the tag-along lock allows, at most 5 | free, no storage deposit |

The covenant does not check the amounts. The ledger already requires every transaction to produce
exactly what it consumes plus its inflation, and only chain outputs may carry inflation (a chain
origin none), so the successor keeping its balance with inflation A leaves exactly A for the reward
outputs, however they are split.

### 1.1 Why the transaction ID is the work

The transaction ID is the BLAKE2b-256 hash of the transaction's elements in order, with the
signature element skipped (`HashEssence`, `ledger/txbuildercore/sign.go`); its first 6 bytes are
then overwritten with the timestamp and the output count, and the last 8 bytes are the hash's own.

| In the hash | Effect |
|-------------|--------|
| the input, the predecessor mine output | no work before the predecessor exists, no stockpiling |
| the timestamp | an attempt is for one slot; the pace and the retarget keep their meaning |
| the outputs | **the work commits to every payee, every amount and every chain paid.** Change any and the work is gone, so a solution cannot be redirected |
| `TxConstraints` | the nonce |
| not the signature | a signature has free randomness of its own; kept out, it cannot be ground |

`txID` is an existing EasyFL builtin and the node computes every transaction's ID anyway, so
checking the work costs nothing extra.

### 1.2 Inclusion and the signer, without a rule

**No tag-along is required.** A sequencer includes a transit by consuming one of its tag-alongs, so
a miner who wants its transit included pays one; a transit paying no sequencer is left out, unless
the miner later consumes its own outputs in a transaction that pays one (indirect inclusion, 2.2). Requiring a tag-along would
add a rule for what the miner's own interest already does.

**The signer is free when there is no tag-along.** The signature is not in the hash. A tag-along's
lock requires the sender written in it, which is inside the hash, to be the transaction's signer,
so a transit with a tag-along cannot be re-signed by another key. A transit with only `sigLock`
outputs can: the copy has the same transaction ID and pays the same holders the same amounts, so
nothing is taken from anyone. The miner's identity, where it matters (the monitor's per-miner
figures), is the signer of the copy the ledger keeps.

Copies sharing a transaction ID but not signature data are a node concern for every transaction
type, not specific to mining: the node's deduplication and its handling of a copy that fails
validation must never let an invalid copy arriving first suppress the valid one. Making the work the
transaction ID raises what rides on that, so the upgrade ships with a regression test for it
(section 6).

### 1.3 Why only the nonce is free, and why it sits last

Every byte of the transit is fixed before the search, the payout included; the nonce is the only
value varied per attempt. Payees and amounts could in principle be varied, but each change pays
someone else or pays differently, so varying them gives value away.

The elements are hashed in the order version, timestamp, sequencer data, input commitment, explicit
baseline, input IDs, unlock data, outputs, endorsements, transaction constraints
(`ledger/def_constants_path0.go`). With the nonce in the last one, a miner hashes everything before
it once per transit and keeps the BLAKE2b state; each attempt then compresses only the final one or
two 128-byte blocks. **The cost of an attempt does not depend on the layout.** Had the nonce stayed
in the unlock data, before the outputs, every attempt would rehash all outputs: about 3 blocks with
one reward output and about 6 with eight (*estimate*), a tax on exactly the layouts pools need.

Endorsements are empty for a transit, and `TxConstraints` holds the nonce and nothing else, so no
arbitrary element can be appended.

### 1.4 What the miner decides

Everything about the payout: who is paid, how much, and through which kind of output.

- **Plain `sigLock`**: tokens to any address, such as a cold address while signing with a hot key.
  Each pays the storage deposit every output pays (9.25 PROX for a plain `sigLock`), a rule the
  ledger already enforces, so it is for amounts above that.
- **Tag-along**: tokens to a chain, of any size. To the miner's own sequencer, to a pool's sequencer,
  to any sequencer as payment for inclusion, or with a request, for example a top-up of the miner's
  own delegation straight from the transit. A tag-along not consumed within 30 slots goes back to
  its sender, the signer.

The ledger does not and cannot bound what goes where: it sees addresses, chains and amounts, not
who is a pool, a worker or the miner's own sequencer. It fixes the count (at most eight), the kinds
(plain `sigLock` or tag-along) and, through the balance rule, the sum.

### 1.5 EasyFL (sketch, not compiled)

Replaced in the upgrade library, same arity; the rest of the mine lock stays.

```
// at most this many reward outputs
func constMineMaxRewardOutputs : u64/8

// the work: low 64 bits of the transaction ID, the hash of the transaction without its signature
func _mineWork64 : tail(txID, 24)

// the nonce: the transaction's only transaction-level element, 8 bytes of inline data
func _mineNonce : parseInlineData(atPath(concat(pathToTxConstraints, 0)))

func _minePaceAndPoW :
and(
   require(isZero(len(selfUnlockParameters)), !!!mine_lock_takes_no_unlock_params),
   require(equalUint(tupleLenAtPath(pathToTxConstraints), u64/1), !!!mine_transit_takes_exactly_the_nonce),
   require(equalUint(len(_mineNonce), u64/8), !!!mine_nonce_must_be_8_bytes),
   require(_minePoWOK(_mineWork64, _mineRequiredK($0, _mineM)), !!!insufficient_mine_proof_of_work)
)

// every transit mints the full A; the balance rule leaves exactly A to the reward outputs
func _mineMinted : _mineAmount

// $0 output index
func _mineOutPresent    : lessThan(uint8Bytes($0), uint8Bytes(numProducedOutputs))
func _mineOutIsTagAlong : and(_mineOutPresent($0), equal(parseBytecode(_mineOutLock($0), 0x), #tagAlong))

// $0 output index: absent, a tag-along (its own lock caps it at five elements), or a plain sigLock
func _mineRewardShapeAt :
or(
   not(_mineOutPresent($0)),
   _mineOutIsTagAlong($0),
   and(
      require(equal(parseBytecode(_mineOutLock($0), 0x), #sigLock), !!!mine_reward_must_be_a_sigLock_or_a_tag_along),
      require(equalUint(tupleLenAtPath(concat(pathToProducedOutputs, $0)), u64/3), !!!mine_sigLock_reward_must_be_plain)
   )
)

func _minePayout :
and(
   _mineRewardShapeAt(1), _mineRewardShapeAt(2), _mineRewardShapeAt(3), _mineRewardShapeAt(4),
   _mineRewardShapeAt(5), _mineRewardShapeAt(6), _mineRewardShapeAt(7), _mineRewardShapeAt(8)
)
```

`_mineShape` is replaced too: its output count becomes 2 to `constMineMaxRewardOutputs + 1`
instead of 2 or 3; the rest of it (start slot, pace, settlement tick, one input, successor at index
0) stays. The payout option O on the mine lock is no longer read; the produced arm still requires
`O <= 3` and the miner writes 1. `constMineTagAlongFee` is no longer used by the mine lock.

One fix outside mining can ride in the same upgrade: the top-up guarantor (`ensureTopUpDelegation`)
should require its predecessor to be locked by `delegateLock`. Today a chain with any lock and a
trailing delegation-state element passes it without the delegation lock ever running. With requests
allowed on reward tag-alongs, this matters more.

## 2. Consequences in the code

| Where | Change |
|-------|--------|
| `ledger/def/upgrade6.easyfl` | the functions of 1.5, `constMinerVersion` 6 |
| `ledger/transaction/tx.go` | `MineWorkValue()` by slot: the last 8 bytes of the txid from the slot, the VRF output before |
| `core/core_modules/txinput_queue` | the floor gate reads the txid: exact and free. **The unknown-sender exemption, today only for the small plain-output layout, must cover this layout**: a new miner has no funds. The first-copy-of-a-solution gate, keyed today on the VRF output, keys on the txid, so a solution is one transaction; copies with other signature data are handled as in 1.2. Load under floods is bounded and tested (section 5). Core: run under `-race` |
| `sequencer/mine` | inclusion is no longer only a tag-along consumed by a milestone (2.2): the registry, the settlement rule ("settle a transit only when no eligible validated transit ranks above it") and the deviation check follow every path by which a transit enters a milestone's past cone, and define which transits are eligible rivals. The order of solutions is one complete order used everywhere (2.3); **what a transit pays is not part of it** |
| `sequencer` backlog | reward tag-alongs, with or without requests, are ordinary tag-alongs once the transit is included |
| `api/monitor` | the miner is the signer, not the holder of output 1; the rewards table counts A per settled transit; a new figure: settled transits that were not the canonical winner, by sequencer (section 4.1) |
| `proxi/node_cmd/mine` | the hot loop: build the transit once, keep the BLAKE2b state up to the nonce, one or two compressions per attempt; sign only the winner. Payout options and their pricing go; a profile section lists the reward outputs (kind, holder or chain, amount; default: one tag-along of A to the miner's chosen sequencer, as today's option 3 with no request) |
| seeker protocol, `nonce_seeker/` (Rust) | the job carries the BLAKE2b state (or the bytes) before the nonce, the bytes after it, and K; **no key**. The Rust seeker drops the VRF and the keystore |
| `util/vrf` | stays for the branch inflation bonus; the split prover and its Rust twin serve only mining and can go after the slot |

Size: the VRF proof (80 bytes) leaves the transit; a one-output transit is about 600 bytes instead
of 700, each further output adds 50 to 80.

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

Section 1.2 lets a transit pay no sequencer. It still reaches the ledger when any transaction a
milestone includes depends on it: the miner spends a plain reward in an ordinary transaction that
pays a sequencer, or the next mine transit consumes it as its predecessor. Today's code does not see
these paths: `InspectInclusions` (`sequencer/mine/deviation.go`) walks the slot's milestones and
inspects only transits they consume directly, skipping ordinary transactions and never following a
transit back to its predecessor transit; the registry keys every transit on the one sequencer its
tag-along targets.

Replacing that one target by the set of chains a transit's tag-alongs target is not enough. The
settlement and deviation checks must cover every mine transit newly entering a milestone's past
cone, through ordinary transactions and through later transits, and the spec must say when a transit
with no tag-along to an active sequencer counts as an eligible rival: either it never does (only a
transit some active sequencer can include directly defers others), or it does from the moment a node
has validated it, which lets a transit no sequencer was paid for block a paid one. Open (section 7).

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
| Direct payout | up to 8: the pool's cut and the recent share-holders | nobody holds the workers' tokens; a running tally of what each worker is owed decides who is paid in each transit | P2Pool on Monero, OCEAN on Bitcoin (payouts in the reward transaction) |
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
- **Emission can be bought with rented hashrate.** It cannot reorder the ledger, since the mine chain
  takes no part in consensus, and liveness is unaffected when it leaves: K falls to the floor as the
  slot gap grows.
- **Custom hardware becomes worthwhile sooner.** *Estimate*: the GPU lead per card over an 8-core CPU
  grows by about an order of magnitude beyond the VRF's 10x. BLAKE2b chips exist for another chain's
  80-byte header; whether they can be reused for this message is not verified.

No other exploit is specific to this option: only the nonce is free and it sits last, so no layout
makes an attempt cheaper; every payee, chain and amount is in the hash, so no redirection; a
re-signed copy pays the same outputs, and with a tag-along it is invalid (1.2).

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

  Delegation becomes a default the miner opts into rather than a rule, so the share of mined supply
  in consensus depends on how miners pay themselves: to be measured after activation.
- **Sequencers are paid what miners choose to pay**, not a fixed 1 PROX. The reference miner's
  default, one tag-along of A to a chosen sequencer, is today's most common layout.
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
proven. What remains: a 10-bit floor (`constMineFloorDifficulty`) is cheap to meet, signature checks
and validation still cost, and a transit on an unproven predecessor still reaches storage. The
upgrade ships with flood tests showing bounded CPU, memory and storage, including across the
activation slot, when the floor is far below the live difficulty.

No reseed rule is proposed; one follows measurement if needed, not by default. If measurement before
the slot shows a gap that would take hours to close, the upgrade can set a fixed B on the first
transit after the slot. Seed high rather than low: an overshoot costs one empty slot per surplus bit,
an undershoot 8 full slots per missing bit.

## 6. Tests

`ledger/tests/library_upgrade6_test.go`:

- a VRF transit valid before the slot, refused from it; a transaction-hash transit refused before,
  valid from it;
- from the slot, valid: one tag-along of A; one `sigLock` of A; a tag-along and a `sigLock` to
  another holder; a tag-along with a top-up request; eight outputs; a tag-along of a few motes
  beside a `sigLock` of the rest; a transit with only `sigLock` outputs re-signed by another key
  (same payouts);
- refused: no reward output, nine, a reward with another lock, a `sigLock`
  with an extra element, a `sigLock` under the storage deposit, rewards not adding up to A (the
  balance rule), `TxConstraints` empty or with a second element, a nonce not 8 bytes, non-empty
  unlock parameters on the mine lock, a payout changed after solving (the work is gone), too few
  zero bits, a transit with a tag-along re-signed by another key (the tag-along's sender);
- `MineWorkValue` equals what the covenant tests, on both sides of the slot;
- the guarantor fix, if it rides along.

`core/core_modules/txinput_queue` (`-race`): the unknown-sender exemption for this layout, the floor
gate; a copy with the same txid and other signature data arriving first does not suppress the valid
transaction (a regression test for every transaction type, not only transits); floods of
floor-meeting transits, on proven and unproven predecessors and across the activation slot, keep
CPU, memory and storage bounded.

`sequencer/mine` (`-race`): a transit addressed to several sequencers; a transit included through an
ordinary transaction spending its reward, and through the next mine transit, is seen by settlement
and the deviation check; the eligibility rule for transits with no tag-along; ties on the work value
broken by the remaining hash bytes, the same order in registry, settlement, deviation and miner.

`proxi/node_cmd/mine`: the stored hash state and one-to-two-block attempts against plain BLAKE2b-256
on vectors around block boundaries (2.1), the same vectors for the Rust seeker; the seeker job
format; payout profiles.

## 7. Open

- The cap on reward outputs: eight as specified; four would cover solo, pool cut and own sequencer,
  not direct payout.
- Requests on reward tag-alongs: allowed as specified; forbidding them keeps reward tag-alongs plain
  but loses the top-up straight from the transit.
- Measure after activation how often a settled transit is not the canonical winner (4.1), and what
  share of mined rewards ends in delegations (4.2).
- Difficulty at the switch (section 5): measure CPU and GPU rates before choosing the slot.
- When a transit with no tag-along to an active sequencer is an eligible rival (2.2).
- Settlement incentives (4.1): whether anything beyond measurement is needed if deviation is
  observed.
