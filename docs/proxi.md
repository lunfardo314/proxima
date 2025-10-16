## CLI wallet program `proxi`

`proxi` is a small CLI program with basic tools for Proxima. 

The program can be compiled by typing `go install` in the `<root>/proxi` directory of the Proxima source.

Commands `proxi -h`, `proxi db -h`, `proxi node -h`, etc. will display help text. 

Most of the commands require a wallet configuration profile in the working directory.
Some commands, for example `proxi util ed25519` are completely stand alone and may not require any config profile.

`proxi` configuration profile usually is file `proxi.yaml`. If we use a file with another name, 
say `proxi2.yaml`, we have to specify it explicitly in the command line with flag `-c` 
and profile file name without extension, for example `proxi node balance -c proxi2`.

Command `proxi wallet` displays main parameters of the profile.
Command `proxi version` displays build data.

Most of `proxi` commands have a form `proxi <cmd group> <subcommand> <args and flags>`, where `<cmd group>` is one of the following:

* `proxi util` helper subcommands
* `proxi init` admin subcommands, for initialization of the database. config profiles and similar 
* `proxi db`  admin subcommands which access multi-state and txStore database directly (bypassing the node). They will fail if the node is running. 
Note that direct access to the Badger database may change permissions of some files and the node can fail to open it when run on different user
* `proxi snapshot` subcommands related to snapshots
* `proxi node` many subcommands which accesses node via API. They all require a configuration profile and endpoint of the running node

### 1. Create a configuration profile and the wallet

The command `proxi init wallet` asks for entropy and generates private key from the provided seed and the system randomness.
Creates configuration profile `proxi.yaml`.
The file will contain something like this (with explanatory comments):

```yaml
default_sequencer_id: 8739faa34a6902e49bc16455bbd642fd3c649e8959d97089e43f214ca57ea0e5

wallet:
  private_key: 7e04abec3f41f7770345e86e85baee3be8bd65eb92f9c667f6c2aa19df25161b04eb57e55cba9cc735b0241db170e8000baa2680f43315e80b015fb918a1a0ee
  account: a(0xdcc2f3be5c019d15108d6169d3f826ac20c73a31db8ad5c5d58e9ab01d3a903a)
  sequencer_id: <own sequencer ID>
api:
  endpoint: http://63.250.56.190:8001
# alternative testnet access points:
#    endpoint: http://113.30.191.219:8001
#    endpoint: http://83.229.84.197:8001
#    endpoint: http://5.180.181.103:8001

tag_along:
  fee: 1
#    sequencer_id: <tag-along sequencer ID>

# provides parameters for 'proxi node getfunds' command
faucet:
  port:  9500
  host:  113.30.191.219

# provides parameters for 'proxi node spam' command
spammer:
  bundle_size: 5
  output_amount: 1000
  pace: 25
  tag_along:
    fee: 1
    # sequencer_id: <sequencer id hex encoded>
  # target address
  target: <target lock in EasyFL format>
```

**Usually adjustments are needed to complete the profile**. 

`wallet.private_key` contains hex encoded raw data of the ED25519 private key. The file must be kept secret 
because of this private key. 

`wallet.account` contains address of the wallet, a lock in the _EasyFL_ format which matches the private key. It usually has the form of `a(0x...)`, which is the
_EasyFL_ script of the ED25519 lock. The address can be calculated from the private key therefore the provided address is used for the consistency check. 

`default_sequencer_id` is a default value used in case when tag along, own or spammer sequencer IDs are omitted. The `proxi init wallet` command 
initializes the default sequencer ID to `8739faa34a6902e49bc16455bbd642fd3c649e8959d97089e43f214ca57ea0e5` which is the ID of the bootstrap sequencer (a constant).

`wallet.sequencer_id` is an optional field. It is irrelevant if you do not run sequencer. It contains `sequencer ID` of the sequencer controlled by this wallet. 
It is necessary so that to access sequencer controlled by this private key with the `proxi node seq withdraw ..` command. Defaults to the  

`api.endpoint` must contain URL for the node's API in the form of `http://<ip>:<port>`. **It must be set to the address of some public access point**

`tag_along.sequencer_id` specifies tag-along sequencer ID which is mandatory for any commands which create transactions, such as `proxi node transfer`.
Defaults to `default_sequencer_id` if absent. 
Each issued transaction will contain so-called _tag-along output_.
The *tag-along output* simply sends the amount of tokens specified in `tag_along.fee` to the sequencer in `tag_along.sequencer_id`. 
The sequencer will consume the *tag-along output* in its transaction. This will pull the transaction into the next ledger state. 

### How to understand transaction and other IDs
Transaction ID in Proxima is a 32-byte array. First 5 bytes are the timestamp of the transaction, the byte at index 6 contains number of outputs produced 
by the transaction minus 1, the rest 26 bytes are taken from `blake2b` hash of the raw transaction bytes.

The transaction ID or its short (trimmed) form is often displayed like this:

`[58514|30sq]029d612d07f0d235c627b720f70fe5c84ac6b0f7f296097197fed5`
or
`[58565|0br]018b9b..`

Here `58514` is slot number. The `sq` means it is a non-branch sequencer transaction and `30` is number of ticks in the slot.
The rest is hex-encoded 26 bytes of transaction hash, prefixed with the one byte with the number of produced outputs minus 1.

If `br` is used instead of `sq`, it means it is a branch transaction. Branch transactions always have `0` ticks, i.e. they are
_on the slot boundary_.
If `sq` and `br` are skipped, it is an ordinary, non-sequencer transaction, produced by user wallet. 

The output (aka UTXO) on the ledger belongs to a transaction which produced it and index in the transaction from 0 to 255. 
Index is displayed as a postfix of the transaction ID.

For example `[58579|25]010b55301a97884f3fd8b4f44ef2c682b81ecda38d2b35a2116bfb[3]` is output of the non-sequencer transaction with index 3. 
Short for of the same output (only for display) would be `[58579|25]010b55..[3]`

Usual ED25519 address takes form `a(0x370563b1f08fcc06fa250c59034acfd4ab5a29b60640f751d644e9c3b84004d0)`, which contains 
hash of the public key. We will skip details here.

Chain ID is a 32-byte array. It is displayed in the hex-encoded form, often with prefix `$/`. 

For example `$/6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc` is the pre-defined constant chain ID of the bootstrap 
(genesis) chain.

### Some useful `proxi node` commands

* `proxi node lrb` displays **latest reliable branch (LRB)** info. _LRB_ represents the ledger state which is contained
by all the current healthy ledger states, i.e., it is the **consensus ledger state** with high probability.
   Usually we require our transaction to be included in the LRB or earlier branches. 

   If LRB is more than a few slots back from now, it may indicate that the node is not synced with the network. 

   LRB information also contains ID of the sequencer which produced the branch, total supply of tokens on the ledger state and the _ledger coverage_ of the branch.
   Initial token balance of the ledger at genesis in testnet is 1.000.000.000.000.000.000 tokens
   The total supply on the LRB ledger state is constantly changing, according to the inflation rules.

   The _ledger coverage_ must be > of the supply for the branch to be _healthy_. The maximum possible value of the ledger coverage is _2 x supply_.

  * `proxi node balance` and  `proxi node balance -v` displays token balance on the usual (ED25519) address and on chains, controlled by the wallet's account in the LRB branch.  
     Token balance is the sum of tokens contained in non-chain outputs plus the sum of balances contained in chain outputs. 
  The command also displays _delegations_ to sequencers.

* `proxi node transfer <amount> -t "<target address>"` sends tokens from the wallet's account to the target address.
  For example, command `proxi node transfer 1000 -t "a(0x370563b1f08fcc06fa250c59034acfd4ab5a29b60640f751d644e9c3b84004d0)"`
  sends 1000 tokens to the specified address. The transfer transaction will contain so-called **tag-along** output with **tag-along fee**
  paid to the **tag-along sequencer** configured in the `proxi.yaml`.
  Flag  `-v` (or `--verbose`) will make command to display the whole transfer transaction. It is a good chance to get acquainted with the Proxima's UTXO transaction model.

* `proxi node compact [<max inputs>]` transfers tokens to itself by compacting up to `<max inputs>` UTXOs in the account into one. 
It is useful when account contains too much outputs, and you want to save on storage deposits. 
   This often happens as a result of the spamming. Note that than `compact` command still requires tag-along fee. 

* `proxi node utxo` displays outputs (UTXOs) in the account. `proxi node utxo -v` displays parsed UTXOs

* `proxi node info` displays info of the node

* `proxi node getfunds` requests funds from a faucet server  
  The following settings can be used to specify a faucet server (addr:port) in `proxi.yaml`:

  ```yaml
  faucet:
      port:  9500
      addr:  113.30.191.219
  ```

### 2. Run spammer from the wallet

Spammer is used as a testing tool and to study the behavior of the system. 
Spammer periodically sends tokens from the wallet's account to the target address in bundles of transactions. 
It waits until each bundle of transactions reaches finality before sending the next one. 
The optional flag `-e` sets depth below LRB (_latest reliable branch_) transaction must be waited to reach. 
Default is `2`

Spammer is run with the command `proxi node spam -e 1`.

The bundle of transactions is a chain of transactions, which consumes output of the previous. 
Only the last one (tip of the batch) contains tag-along output. 

Configured tag-along sequencer consumes the output (the tip of the batch). 
This way it pulls the whole bundle of transactions into the ledger state with one tag-along fee amount.

As per current ledger constraints, one spammer can achieve maximum 1 TPS of the transfer transactions. 
In Proxima the rate is limited per address (per user). It is 1 TPS for non-sequencers (assuming no conflicting transactions are issued).
Higher total TPS can be reached only by multiple users. 

### Delegation

See [delegation](delegate.md).