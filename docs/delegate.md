## Delegation

### Why delegate?

_Delegation_ is a way to participate in the _cooperative consensus_ on the Proxima ledger **without the need of running a sequencer**.

Each token holder can delegate their tokens to a _sequencer_. The sequencer then will be able to generate inflation from the delegated tokens. 
It will not be able to steal delegated tokens though. From the other side, the sequencer will increase its ledger coverage with the delegated tokens.  

Through _delegation_ one can generate inflation from their tokens by outsourcing all the trouble of running node and sequencer to somebody else.

In Proxima, each token holder is incentivized to earn from inflation. If token holder is lazy and its holding stay in a passive address, 
he/she will suffer the opportunity cost of inflation (dilution).

This is by design: we want every token will be in the ledger coverage. Delegation will contribute to the security of the ledger
and will be rewarded by inflation tokens.

So, the general rule for the token holder is to keep tokens delegated all the time they are not needed for transacting. 
Delegating is win-win situation for the token holder and the network.

It must be noted, that delegation is subject to natural limits of minimum amounts. As per immutable inflation rules encoded 
into the ledger, less than minimum amount when delegated will not generate inflation. In the testnet version meaningful amounts
start from `~700.000.000` upwards.

### How to delegate?

At the core of _delegation_ is the chain mechanism: inflation tokens are created "out of thin air" by building chains.
User must create a new chain output with all the tokens he wants to delegate. This can be achieved with the following command:

`proxi node delegate amount <amount of tokens> -q <target sequencer ID>`

The `<target sequencer ID>` is hex-encoded chain ID of the chosen target sequencer. 
Normally, the target sequencer for delegation is selected based on its market parameters such as uptime and effective inflation rate. 
In the testnet you can find all sequencer chains with the following command:

`proxi node allchains -q -d`

Choose a chain which is run by some sequencer, preferably the one active in the last several slots from now and not too many of delegations already. 
It will be a delegation target.

For example:
```text
Command line: 'proxi node allchains -q -d'
using profile: ./proxi.yaml
using API endpoint: http://5.180.181.103:8001, default timeout
successfully connected to the node at http://5.180.181.103:8001
Latest reliable branch (LRB) ID: [16577|0br]01140c94ab7255fb121c8a3a7bdea536366740b0452b72369e1e6f, 1 slot(s) from now. Now is 16578|121

Sequencers with delegation totals:
   $/35e5c2f9bbaf07df23676cead81539e2cabb04e9a27921834e17cb99d8e6f083   # delegations:   3, total delegated amount:       30_015_308_699, last active: 2 slots ago
   $/779a59583ec045b5c8ddea2782f1f9a5bf7ec77e7378149195118ee1f1184e10   # delegations:   1, total delegated amount:      500_170_828_886, last active: 2 slots ago
   $/41659dc34f5c61796c014ad4339469eb2a5364a7d5e6f4caa124f55e6098c0c8   # delegations:   1, total delegated amount:      500_274_999_500, last active: 2 slots ago
   $/6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc   # delegations:   1, total delegated amount:    1_000_511_831_978, last active: 2 slots ago
   $/dcdd9ddc7b42dd0c925aba67c3a2c37338bc5d1c1c316879d03f69b42a499a72   # delegations:   0, total delegated amount:                    0, last active: 1 slots ago
---------------
TOTAL DELEGATED AMOUNT: 2_030_972_969_063
```

### How to check my delegations?
Commands `proxi node mychains -d` or `proxi node mychains -d -v` lists all chains, which are delegations controlled 
from the current wallet. For example:

```text
Command line: 'proxi node mychains -d -v'
using profile: ./proxi.yaml
using API endpoint: http://5.180.181.103:8001, default timeout
successfully connected to the node at http://5.180.181.103:8001
Latest reliable branch (LRB) ID: [8250|0br]01122b0d3a6f1470aaf14d0f0ecefc486c89d5ffbb58144ac22a70, 1 slot(s) from now. Now is 8251|147

List of delegations in account a(0x43ceee694015e327a85c66c9c1a0c0bb8c7de37f19d5e8a9ec86d1eb81931d98)

$/948626bceef1971ac75b57f6c4b29a2b97f98fe5237e47eb557051dfe06090e2   500_105_069_384            -> c(0x779a59583ec045b5c8ddea2782f1f9a5bf7ec77e7378149195118ee1f1184e10)
        inflation +104_069_384 since 1937|52 (6314 slots), avg 16_482 per slot, start amount 500_001_000_000, annual rate: ~10.15%

$/ce290b09402ac5b9decbc9013703ed7fae6bba4ccfa49984b458dd8e8b88c06f   1_000_237_222_622                  -> c(0x6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc)
        inflation +237_222_622 since 1056|224 (7195 slots), avg 32_970 per slot, start amount 1_000_000_000_000, annual rate: ~10.15%
```

By repeating commands `proxi node mychains -s` or `proxi node mychains -s -v` you will see how newly created tokens are 
coming to the balance of your delegated chain roughly each 20 sec. If target sequencer is not producing new tokens, 
it means either it is down, or delegated amount is too small.

### How to reclaim delegated tokens back?
It works by simply by destroying the delegation chain with the command:

`proxi node killchain <delegation ID hex>`.

For example:

```text
Command line: 'proxi node killchain 19de6d99662eca0e16209e4922dfbbacf1832dd7727738eea8b0cbff7eeafd16'
using profile: ./proxi.yaml
using API endpoint: http://5.180.181.103:8001, default timeout
successfully connected to the node at http://5.180.181.103:8001
using tag_along sequencer: 6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc
discontinue chain $/19de6d99662eca0e16209e4922dfbbacf1832dd7727738eea8b0cbff7eeafd16? (Y/n)


attempt #1. Submitted transaction [8219|51]014cf1ee1fab53a815d0c3c2cff923355aacce010270dc9288c579. LRB (latest reliable branch) is -423 ticks, -1 slots, -16.92s behind.
attempt #2. Submitted transaction [8219|51]017aadb67e759b50567e93fb96401db9e194c17f7f3a3830ae89a8. LRB (latest reliable branch) is -271 ticks, -1 slots, -10.84s behind.
attempt #3. Submitted transaction [8219|51]014c7527b9bda574970df22b7119fd89c87a690a8ade44760d3ca2. LRB (latest reliable branch) is -272 ticks, -1 slots, -10.88s behind.
chain $/19de6d99662e.. not found. LRB (latest reliable branch) is [8221|0br]01c992a83bf21052f11507fd47e8d2d8d3c2affa22867d577782d9 (1 slots behind from now)
```

All tokens from the chain are returned to the normal wallet's `ED25519` address.

**Important!**

The `proxi node killchain` command when applied to delegation-locked chained outputs will look for the most convenient liquidity window
for the delegated fund. It may take some time, often a minute or so. This is by intention. Wallet tries to avoid race condition with
sequencer who keeps moving the delegated tokens whenever possible, and issues many transactions until first one hits the liquidity
window (which alwasy exist)

### How it works?

The new delegation chain output is locked with so-called **delegation lock**, a *EasyFL* script, one of many. 
It essentially is a _smart contract_, which enforces logic of interaction between owner of tokens and the delegation target. 
The *delegation lock* script allows two different private keys (addresses) to unlock the chain, but each under different conditions:

- the _original owner_ can unlock and consume the output any time without limitations
- the _delegation target_ (sequencer) can unlock and consume output only under following conditions:
  - if consuming transaction is on an even slot (i.e. every second slot)
  - if chain successor contains at least the same amount of tokens as predecessor (no possibility of stealing)

Race condition can occur when owner consumes transaction in the same slot as delegation target. 
Wallet software can prevent it even if delegation target is malicious.  

### What is minimum amount of delegation?

It is limited by the amount of inflation which can be generated in 1 slot. In the first slot since genesis `33.000.000` 
will generate 1 token of inflation per slot. Less than that will generate 0 inflation due to rounding. 
Due to adjustment of the inflation rate over time, this minimum amount will grow. 

Normally, meaningful amount of delegation is from `700.000.000` tokens upwards. 

### How much inflation is generated by delegation?

Maximal inflation generated by the non-sequencer chain is defined by the ledger rules. In the testnet it is tail inflation
corresponding to ~10% annual inflation first year, later gradually diminishing.

In theory, delegation target can take all the generated inflation for itself and leave 0 for the owner (it cannot steal delegated tokens though).
In that case, owner can reclaim its delegation back and delegate it to another, less greedy sequencer. 

The point is, the fraction of the generated inflation delegation target takes from newly created amounts to itself is up to the market. 
In the extreme (though realistic) case %100 of inflation will be returned to the owner. 
Why? Because sequencer has inherent interest to attract more capital to its ledger
coverage, so sequencers will compete by price to more attractive to token holders who want to delegate their holdings.

In the testnet, sequencer "shaves" 1/10th of the generated inflation and takes for itself (it is configurable in the node in `sequencer` section).
So, maximum possible 10% annual percent of inflation will become ~9% for the delegating token holder.

That is more or less fair, because sequencers incur costs and wants to cover it, while other token holders has 0 cost of delegating tokens.
But open and permissionless market opf sequencers and delegators will decide what is fair. 