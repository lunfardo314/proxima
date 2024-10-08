**DRAFT**

# Network of 5 nodes/sequencers
The step-by-step tutorial how to run a small testnet on one computer. 

## Compile
Clone the repository to `<your_dir>/proxima`.

Type `go install` in working directories `<your_dir>/proxima` and `<your_dir>/proxima/proxi`. 
This will create executables: `proxima` for the node, and `proxi` for the CLI-wallet program.   

Run `proxi -h`, `proxi init -h`, `proxi db -h` to check if its is working.

## Directory structure
The directory in the repo `<your_dir>/proxima/tests/nodes` and its 5 subdirectories contain template configuration 
for a small network of 5 nodes.
* each subdirectory contains node configuration file `proxima.yaml` and wallet configuration profile `proxi.yaml`
* the configuration contains private keys of two kinds: 
  * for the peering host ID, as required by the `libp2p` package. 5 of them were pre-generated for the manual configuration of 5 nodes in 
in the `peering` section of `proxima.yaml` in each subdirectory. The `peering` configuration
connect each node with 4 others. **Auto-peering of nodes is not available yet.**.
  * private keys, which control accounts on the ledger. Each account is represented by address in the form `addressED25519(<hex>)`. 
Those private keys are used as controlling keys of sequencers and also as normal account keys.

**Do not use any of these private keys in your production environment!!!**

To start a testnet on your computer, copy `<your_dir>/proxima/tests/nodes/*` with subdirectories and config files to your preferred location, say `myHome/*`.

## Create ledger identity
To create ledger identity file make `myHome/nodes/0` the working directory and run the command: `proxi init ledger_id`.

It will create a file `proxi.genesis.id.yaml` with all constants needed for the genesis ledger state, which includes the absolute
genesis time for the slot `0`.

Copy `proxi.genesis.id` from directory `myHome/0` to the rest of node directories `myHome/1`, `myHome/2`... 
The file must be exactly the same in each of them in order nodes would be able to start from the same genesis ledger state.

## Initialize genesis for the node
To start a node, first we need to create genesis ledger state for it. Let's run the following command in the directory `myHome/0`:

`proxi init genesis_db`

The command will create `multi-state` database name `proximadb` and will display the _chain ID_ of the bootstrap chain.
It is always the same: `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963`.

Now in the same directory run command `proxi db info`. It will display something like that:
```text
Command line: 'proxi db info'
Multi-state store database: proximadb
Total 1 branches in the latest slot 0
----------------- Global branch data ----------------------
  0: boot ($/af7bed..) supply: 1_000_000_000_000_000, infl: 1_000_000_000_000_000, on chain: 1_000_000_000_000_000, coverage: 1_000_000_000_000_000, root: 35e8a06a40057f368db5c2546cf1bf1b619c10d6c6b5c147e08d33c1813887d2

------------- Supply and inflation summary -------------
   Slots from 0 to 0 inclusive. Total 1 slots
   Number of branches: 1
   Supply: 1_000_000_000_000_000 -> 1_000_000_000_000_000 (+0, 0.000000%)
```

Repeat the same procedure for other nodes in directories `myHome/1`, `myHome/2` ... etc. now or later.

## Run network with one node on directory `myHome/0`
The genesis state contains the whole initial supply of tokens `1.000.000.000.000.000` in a single boostrap sequencer output,
controlled by the private key with the address `addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)`.

We will take some part of tokens from the sequencer chain output and put it 
into the normal `addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)` account with the following command.

`proxi init bootstrap_account`

This command creates transaction, which takes `1.000.000` tokens from the chain and puts them into the normal output. 
Having some tokens on the output outside the chain makes it possible to issue transactions with commands to the sequencer (won't dig it here).

Now the command `proxi db info` displays the following:
```
Command line: 'proxi db info'
Multi-state store database: proximadb
Total 1 branches in the latest slot 1
----------------- Global branch data ----------------------
  0:  ($/af7bed..) supply: 1_000_000_000_000_000, infl: 0, on chain: 999_999_999_000_000, coverage: 1_500_000_000_000_000, root: ff0a5f9f42d4443e98280f8242dc3fb554e10bfa49f8e85a51e02e091d928cdf

------------- Supply and inflation summary -------------
   Slots from 0 to 1 inclusive. Total 2 slots
   Number of branches: 2
   Supply: 1_000_000_000_000_000 -> 1_000_000_000_000_000 (+0, 0.000000%)
   Per sequencer along the heaviest chain:
            $/af7bedde1fea.. : last milestone in the heaviest:        [1|0br]9d171f..[0], branches: 1, balance: 1_000_000_000_000_000 -> 999_999_999_000_000 (-1_000_000)
```
which means the bootstrap chain now contains `1.000.000` tokens less (those `1.000.000` are on the ED25519 address).

The command above also creates database named `proximadb.txstore`, which will contain all raw transaction bytes. 
This command also puts the bytes of the transaction which transferred `1.000.000` to another output, into the `txStore`.

This step of creating bootstrap account is needed only for the bootstrap node. It is not needed when starting other nodes. 
The other nodes will start from genesis and then sync its state and transaction store with other nodes. 

Now we can start the node by running command in the working directory `myHome/0`:

`proxima`

It will start the node and the bootstrap sequencer in it. The `sequencer ID` of the bootstrap sequencer
is always known, so the `proxima.yaml` is pre-configured with automatic start of the bootstrap sequencer.

The sequencer (this time it is controlling the whole supply, minus `1.000.000` on the ordinary account), will start building 
the chain of sequencer transactions and generate inflation for itself. 

With one-node network we can transfer tokens from account to account using `proxi`, however it is a centralized system yet. 

The node can be stopped with `CTRL-C`.

## Run more nodes in 'access' mode 
We will start more nodes without running sequencers on them. Those nodes will sync and validate the ledger along the 
heaviest chain of branches. It wil also provide full unrestricted access to the
network through API. However, those *access nodes* will not contribute to the consensus. In Proxima not nodes, but sequencers
contribute to consensus.

Let's make node running on the directory `myHome/0` as a separate background process for example using `tmux`.

Now let's initialize genesis for the node `myHome/1` as described in the section [Initialize genesis for the node](#initialize-genesis-for-the-node). 
Then start the node in the working directory `myHome/1` with command `proxima`.

The node will start and will sync its state with the node on `myHome/0`. It will keep receiving sequencer transactions produced by the 
sequencer on the node `myHome/0` and will keep updating its ledger state with new transactions. Yoo will observe same transactions on logs 
of both nodes. 

Now let's repeat this step with nodes on directories  `myHome/2`, `myHome/3` and `myHome/4`. 

After that, we will have all 5 nodes connected into the network of peers and exchanging transactions via gossip. 
We will be able to make transfers between accounts with `proxi` and all other
functions of the distributed ledger. The network will keep running and syncing valid ledger state even when we stop any node, 
except the one on `myHome/0`.

This will be a distributed network of peers, however it will be a centralized system: the bootstrap sequencer on `myHome/0` 
controls the whole supply of tokens and controls the network. Stopping that single node will stop the network.

## Transfer tokens to another address
In the 5 node network describe above, the genesis controller still controls all the tokens: `1.000.000` locked on the
`addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)`, plus the rest `999.999.999.000.000`+inflation
on the sequencer chain.

We can see it by running command `proxi node balance` in the working directory `myHome/0`:
```text
Command line: 'proxi node balance'
using profile: ./proxi.yaml
using API endpoint: http://127.0.0.1:8000
successfully connected to the node at http://127.0.0.1:8000
wallet account will be used as target: addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)
TOTALS:
amount controlled on 1 non-chain outputs: 1_000_000
amount controlled on 1 chain outputs: 1_000_000_926_197_073
TOTAL controlled on 2 outputs: 1_000_000_927_197_073
```

As per convention, the command line with prefix `proxi node` means the `proxi` is accessing the ledger via the node's API.
Meanwhile, commands which starts with `proxi db` accesses the DB directly, without node. The latter option is mostly used for bootstrap and debug. 
It cannot be used while node is running.

The command `proxi node transfer 1000 -t "addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)"`
will produce transaction which will transfer `1000` tokens from the address, controlled by private key in `myHome/0/proxi.yaml` 
to the corresponding address, by tagging the transaction along the sequencer, configured in the `proxi.yaml`.

Now command `proxi node balance` in directory `myHome/0` will display:
```text
Command line: 'proxi node balance'
using profile: ./proxi.yaml
using API endpoint: http://127.0.0.1:8000
successfully connected to the node at http://127.0.0.1:8000
wallet account will be used as target: addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)
TOTALS:
amount controlled on 1 non-chain outputs: 998_500
amount controlled on 1 chain outputs: 1_000_001_895_390_589
TOTAL controlled on 2 outputs: 1_000_001_896_389_089
```
`1000` tokens have been sent to the target address and additionally `500` tokens were consumed by the sequencer as the _tag-along fee_.

The command `proxi node balance` in the directory `myHome/1` will output:
```text
Command line: 'proxi node balance'
using profile: ./proxi.yaml
using API endpoint: http://127.0.0.1:8001
successfully connected to the node at http://127.0.0.1:8001
wallet account will be used as target: addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)
TOTALS:
amount controlled on 1 non-chain outputs: 1_000
TOTAL controlled on 1 outputs: 1_000
```

Similarly, transfer of tokens from account to account can be performed from any access node in the network, provided `proxi.yaml` contains the right 
private key and the _tag-along sequencer_ is configured as the bootstrap sequencer `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963`, 
which currently is the only running.

## Decentralizing the network
To make the network decentralized, we need to run several sequencers. We will run 5 of them, one on each node, while total number is practically
unlimited (it is even possible to run several sequencers on one node).

### Distributing supply into 4 more addresses

We need to split the whole supply, controlled by the bootstrap sequencer on its chain, into 5 pieces. 

The following command withdraws `800000000000000` tokens from sequencer chain directly to its controller's account:
`proxi node sequencer withdraw --finality.weak 800000000000000 `. 

This command creates a transaction with the `withdraw` command data on the tag-along output. Sequencer, by consuming the 
tag-along output, will recognize it as a command sent from its own controller. As a result, sequencer will produce additional output 
in the next sequencer transaction which sends `800000000000000` tokens to own ordinary ED25519 address.

_(for those who knows how IOTA SC chains works, the command above is the same principle how on-ledger requests to the chain works. 
The ISC committee may be sequencer on Proxima)_

The balance now looks like this:
```text
Command line: 'proxi node balance'
using profile: ./proxi.yaml
using API endpoint: http://127.0.0.1:8000
successfully connected to the node at http://127.0.0.1:8000
wallet account will be used as target: addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)
TOTALS:
amount controlled on 2 non-chain outputs: 800_000_000_998_000
amount controlled on 1 chain outputs: 200_003_741_966_766
TOTAL controlled on 3 outputs: 1_000_003_742_964_766
```
The following commands distribute tokens from bootstrap accounts to other 4 addresses, `200000000000000` tokens each:

`proxi node transfer 200000000000000 --finality.weak -t "addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)"` to the wallet `1`

`proxi node transfer 200000000000000 --finality.weak -t "addressED25519(0x62c733803a83a26d4db1ce9f22206281f64af69401da6eb26390d34e6a88c5fa)"` to the wallet `2`

`proxi node transfer 200000000000000 --finality.weak -t "addressED25519(0x24db3c3d477f29d558fbe6f215b0c9d198dcc878866fb60cba023ba3c3d74a03)"` to the wallet `3`

`proxi node transfer 200000000000000 --finality.weak -t "addressED25519(0xaad6a0102e6f51834bf26b6d8367cc424cf78713f59dd3bc6d54eab23ccdee52)"` to the wallet `4`

After these command we have 4 additional addresses with `200000000000000` tokens each.

Note that after this command only a bit more than `200000000000000` tokens have left on the only sequencer chain. 
The rest `800000000000000` stops contributing to the consensus. In general this means liveness problem: 
the user cannot be sure if other `800000000000000` are just passive or hiding with the aim to revert the chain some time later in the _long-range attack_.

This is expected to be rare situation when network is run by many sequencers, however it can happen in the bootstrap phase
when only 1 or few sequencers are running. 

To override this situation in the command above, we add flag `--finality.weak` which makes use of another, 
_weak_ finality criterion, instead of default _strong_.
For more details see section _Security considerations_ in the whitepaper.

### Starting new sequencer
To create chain origin for the new sequencer, controlled by the private key of the wallet `myHome/1`, we make directory `myHome/1`
the current working directory and run the following command in it: `proxi node mkchain --finality.weak 199999999000000`.

The command creates new chain origin with specified amount of tokens on the chain. We leave `1.000.000` tokens in the current ED25519 address.

The command `proxi node balance` will display something like that:
```text
using API endpoint: http://127.0.0.1:8001
successfully connected to the node at http://127.0.0.1:8001
wallet account will be used as target: addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)
TOTALS:
amount controlled on 2 non-chain outputs: 1_000_500
amount controlled on 1 chain outputs: 199_999_999_000_000
TOTAL controlled on 3 outputs: 200_000_000_000_500
```
It says, that the private key controls `199.999.999.000.000` on a chain-constrained output.
The command `proxi node chains` will display the _chain ID_ of the new chain:
```text
Command line: 'proxi node chains'
using profile: ./proxi.yaml
using API endpoint: http://127.0.0.1:8001
successfully connected to the node at http://127.0.0.1:8001
list of chains controlled by addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)
   $/991b27b0a369ce03fad15be411e730740dae563055ca357531f6d62588f414b6 with balance 199_999_999_000_000 on [2243|93]e3e70b..[0]
```
The `991b27b0a369ce03fad15be411e730740dae563055ca357531f6d62588f414b6` is the _chain ID_ of the new chain. 
Let's copy it and put into the `sequencers.seq1.sequencer_id` key in the `myHome/1/proxima.yaml`. 
Enable the sequencer by putting `true` into `sequencers.seq1.enable`.

Then restart node with `CTRL-C` and command `proxima` again.
After some 10 sec you will see sequencer `seq1` starting on the node `myHome/1`. The two nodes will be exchanging sequencer transactions. 
You will see both of them on logs of both nodes. (if not, restart it again).

Now we can repeat the procedure above for nodes `myHome/2`, `myHome/3` and `myHome/4`. 
The result will be 5 nodes with sequencers on them running the consensus.

All 5 sequencers `boot`, `seq1`, `seq2`, `seq3`, `seq4` will be exchanging transactions. 

If some of them does not appear, stopping and starting node again usually helps. Remember: this is a prototype (a very, very early alpha) 
version of the node!!!

## Starting spammer
To start transaction spammer on node `myHome/0` run command `proxi node spam` in the directory `myHome/0`.
It will start sending bundles of transactions to the address which is configured in the `spammer` section of the `myHome/0/proxi.yaml`.

The spammer is configured to wait until certain finality of the transactions. It may take 3 slots (30 seconds) for each round. 
So, with one spammer you will not achieve high TPS. In order to achieve say 100 TPS, one will need some 100 users spamming.  

Good luck!  