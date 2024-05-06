# Testnet of 5 nodes/sequencers
The tutorial how to run a testnet follows below.

## Compile
Type `go install` in working directories `<your_dir>/proxima` and `<your_dir>/proxima/proxi`. This will create executables 
of the node `proxima` and of the CLI-wallet program called `proxi`.   

Run `proxi -h`, `proxi init -h`, `proxi db -h` and so on to check if its is working.

## Directory structure
The directory in the repo `<your_dir>/proxima/tests/nodes` and 5 subdirectories contain template configuration for a small testnet of 5 nodes.
* each subdirectory contains node configuration file `proxima.yaml` and wallet configuration profile `proxi.yaml`
* the configuration contains private keys of two kinds: 
  * as peering host ID, as required by the `libp2p` package. We pregenerated 5 of them for the manual configuration of 5 sequencers.
They are included into `peering` section of `proxima.yaml` in each subdirectory
  * private keys, which control accounts on the ledger. Each account is represented by address in the form `addressED25519(<hex>)`. 
Those private keys are used as controlling keys of sequencers and also as normal account keys.

More of those pre-generated private keys can be found in[testkeys.yaml](../../testkeys.yaml). 

_Do not use any of these private keys to protect your production environment!!!_

To start a testnet on your computer, copy `nodes/*` with subdirectories and config files to your preferred location, say `myHome/*`.

## Create ledger identity
To create ledger identity file make `myHome/nodes/0` working directory and run command:

`proxi init ledger_id`

It will create a file `proxi.genesis.id` with all constants needed for the genesis ledger state.

Copy `proxi.genesis.id` from `0` to the rest of node directories `1`, `2`... The file must be exactly the same in each of them 
so that nodes would be able to start from the same genesis ledger state.

## Initialize genesis for the node
To start a node, first we need to create genesis ledger state for it. This is achieved by running the command in the directory 
which contains `proxi.genesis.id`. Let's do it for directory `0`:

`proxi init genesis_db`

The command will create `multi-state` database `proximadb` in the directory and will display the _chain ID_ of the bootstrap chain.
It is always the same: `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963`.

Now in the same directory run:

`proxi db info`

It will display something like that:

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

You can do this same procedure for other nodes in directories `1`, `2` ... etc now or later.

## Run network with 1 node on directory `0`
The genesis state contains the whole initial supply of tokens `1.000.000.000.000.000` in the single boostrap sequencer output,
controlled by the private key with the address `addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)`.

If we run sequencer on that chain output immediately, we won't be able to control the funds outside it. 
So, in order to make bootstrap process smooth, we take some part of tokens from the sequencer chain output and put it 
into the normal `addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)` account.

`proxi init bootstrap_account`

This command created transaction, which takes `1.000.000` tokens from the chain and puts them into the normal output. 
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
which means the bootstrap chain now contains `1.000.000` tokens less (the rest is on ED25519 address).

In command above also creates database called `proximadb.txstore`, which will contain all war transaction. Currently, `txStore` contains a single transaction,
the one which took `1.000.000` to another output.

This step of creating bootstrap account is needed only for the bootstrap node. It is not needed when starting other nodes.

Now we can start the node by running command in the working directory:

`proxima`

It will start the node and the bootstrap sequencer in it. It is possible because `sequencer ID` of the bootstrap sequencer
is always known, so the `proxima.yaml` is pre-configured with automatic start of the bootstrap sequencer.

The sequencer (this time it is controlling the whole supply, minus `1.000.000` on ordinary account), will start building 
the chain of sequencer transactions (2 transactions per slot) and generate inflation for itself. 

Not much useful work yet. With one-node network we can transfer tokens from account to account using `proxi`, however it is a centralized system yet. 

The node can be stopped with `CTRL-C`.

## Run other nodes in 'access node' mode 
Let's make node running on directory `0` as a separate stand-alone process for example using `tmux`.
Now let's initialize genesis for the node `1` as described in the section [Initialize genesis for the node](#initialize_genesis_for_the_node). 
Then start the node on the working directory `1` with command `proxima`.

The node will start and will sync its state with the node `0`. It will keep receiving sequencer transactions from the 
sequencer on the node `0` and will keep updating its ledger state with new transactions.

Now we can repeat this step with nodes on directories  `2`, `3` and `4`. 

After doing that, we will have all 5 nodes connected into the network of peers, 
and exchanging transactions via gossip. We will be able to make transfers between accounts with `proxi` and all other
functions of the distributed ledger. The network will keep running and having valid ledger state when we stop any node, except `0`.

This will be a distributed network of peers, however it will be centralized: the bootstrap sequencer controls the whole supply
of tokens and controls the network. Stopping that single sequencer will stop the network.

## Decentralizing the network
To make the network decentralized, we need to run several sequencers. We will run 5 of them, while total number is practically
unlimited.

**TODO**

