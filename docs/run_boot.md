## Running first node in the network

The following are step-by-step instructions how to initialize and start Proxima node with the genesis ledger state and bootstrap sequencer on it.

### 0. Compile
Clone the repository to `<your_dir>/proxima`.

Type `go install` in working directories `<your_dir>/proxima` and `<your_dir>/proxima/proxi`.
This will create executables: `proxima` for the node, and `proxi` for the CLI program for simple wallet and tools.

Run `proxi -h`, `proxi init -h`, `proxi db -h` to check if it works.

Below we assume we use same working directory for all configuration profiles and databases.

### 1. Create genesis owner's wallet

The following command generates private key from the provided seed and system randomness. 
It creates configuration profile file `proxi.yaml` for the `proxi` program:

`proxi init wallet`

The file `proxi.yaml` will be used as a wallet profile which will control genesis.
It contains genesis private key and corresponding controller address.

The wallet profile must be adjusted by replacing placeholder `<own sequencer ID>` with the predefined bootstrap
sequencer ID `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963`.

Please note that key `api.endpoint` must contain valid API endpoint. In the generated file it contains
default value for the single node running on the local machine. 
It may require adjustment when running several nodes on the same machine

### 2. Create ledger ID file

The following command will create ledger ID file `proxima.genesis.id.yaml` with all ledger constants:

`proxi init ledger_id`

The constant `genesis_controller_public_key` is the public key which corresponds to the private key 
taken from the wallet profile. 

The `description` constant may be adjusted to one's liking.

After genesis state DB will be created from the ledger ID file with the first node, 
**exactly the same file should be used to create genesis state databases for other nodes**.  
Ledger constants will remain immutable for the lifetime of the ledger.

### 3. Create genesis database

The database with the genesis state will be created from the ledger ID file `proxima.genesis.id.yaml` 
created in the previous step by running the following command in the same directory: `proxi init genesis_db`

The database directory `proximadb` will be created as a result.

### 4. Prepare bootstrap account
The genesis ledger state created in the previous step will contain the whole supply of tokens into one output, 
on the bootstrap chain `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963`. To be able to distribute
supply, we will need some amount of tokens in the ordinary ED25519 account, controlled by the same private key.

The following command:

`proxi init bootstrap_account`

will do the following:
* will create transaction to take `1.000.000` of tokens from the chain output into another output locked with `AddressED25519`.
* will update the ledger state directly in the database. It will produce next ledger state branch in the next slot
* will create transaction store database in `proxima.db.txstore`
* will put the transaction into the transaction store

We can check the resulting ledger state by running the command:

`proxi db info` and `proxi db accounts`

All other nodes will be able to sync right from the very genesis state therefore they will not need to 
make bootstrap account right in the database.

### 5. Prepare node configuration profile

Run the command `proxi init node -s`

It will create node config profile `proxima.yaml` in the working directory. Flag `-s` adds also sequencer config section
into the profile, otherwise optional.

To finish the config file for the first bootstrap node, it must be adjusted the following way:
* key `peering.host.bootstrap` set to `true`
* ports adjusted to the environment, if needed
* placeholder `<local_seq_name>` in sequencer configuration must be replaced with `boot`
* placeholder `<sequencer id hex encoded>` in the key `sequencers.boot.sequencer_id` must be replaced with the bootstrap 
sequencer id `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963` 
* key `sequencers.boot.enable` mst be set to `true`
* key `sequencers.boot.controller_key` must be set to the value taken from key `wallet.private_key` 
in the genesis controller wallet file `proxi.yaml`. This way sequencer will control the genesis account.

### 6. Run the node
The node with the bootstrap sequencer in it is run by typing command `proxima`. 

When node starts running, by running command `proxi node balance` in the same directory 
we will see balance of the bootstrap chain. 
Balance will be growing because sequencer will be generating inflation for itself out of the whole supply: 10 mil and more with each slot! 

The output of the command will look like this:
```text
Command line: 'proxi node balance'
using profile: ./proxi.yaml
using API endpoint: http://127.0.0.1:8000
successfully connected to the node at http://127.0.0.1:8000
wallet account will be used as target: addressED25519(0x070a54a7153e0ec5a1767c158f986e2f6e86243b67cfeba7075051723b5a5096)
TOTALS:
amount controlled on 1 non-chain outputs: 1_000_000
amount controlled on 1 chain outputs: 1_000_000_062_177_618
TOTAL controlled on 2 outputs: 1_000_000_063_177_618
```

Node can safely be stopped with `ctrl-C`. 

