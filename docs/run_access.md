## Running access node

The following are step-by-step instructions how start Proxima access node and sync it with the network.

The **access node** is the simplest configuration of the node. It does not run sequencer as part of it.
The main functions of the access node are:
- keep connections with peers
- keep valid multi-ledger state in sync with the network
- provide API access to the network for the Proxi wallet and other programs
- gossip new transaction coming to the node from other peers and from the API to the network
- submit new transactions to the transaction store database

Running an access node does not require to be a token holder. 

### 1. Compile
Clone the repository to `<your_dir>/proxima`.

Run `go install -v` in working directories `<your_dir>/proxima` and `<your_dir>/proxima/proxi`.
This will create executables: `proxima` for the node, and `proxi` for the CLI program with simple wallet functionality and tools.

Run `proxi -h`, `proxi init -h`, `proxi db -h` to check if it works.

Below we assume we use same working directory for all configuration profiles and databases.

### 2. Download snapshot file
One of testnet nodes is constantly producing multi-state database snapshots. 

Temporary place for download is http://83-229-84-197.cloud-xip.com/downloads/ . 
Go there and download the latest out of three snapshot files to the directory on your computer where node's database will reside. 

The snapshot file name is made out of the transaction ID of the branch which represents snapshot state. 

### 3. Check of snapshot file if suitable to start a node
In the directory with the downloaded snapshot file run a command:

`proxi snapshot check --api.endpoint <APIendpoint>`

This command check the snapshot file against the current ledger state which is seen in the latest reliable branch (LRB)
of specified node in the network. It makes sure that branch of the snapshot is in the past cone of all current branches
on the network. This prevents situations, when branch of the snapshot was orphaned (small yet positive probability).

If you see something like:
```text
latest reliable branch (LRB) is [101018|0br]01b14af9f0eae05b1e457ae140d6812a47e8151f10ac382c97d92a
the snapshot:
      - is INCLUDED in the current LRB of the network. It CAN BE USED to start a node
      - is 889 slots back from LRB and 890 slots back from now
```

Command `proxi snapshot check_all --api.endpoint <APIendpoint>` scans all snapshot files in the current directory and check each of it.

snapshot file can be used to start a node and it will be synced.
Otherwise snapshot represents a ledger state which cannot be synced with the network.

### 4. Create multi-state database
In the directory with the snapshot file run command `proxi snapshot restore -v`.
Depending on the computer, it may take several minutes to build the database. Interrupting the process makes DB inconsistent,
the `proximadb` directory must be deleted and the command run again.

Upon finish, the command will report statistics like this:
```text
Success
Total 513278 records. By type:
    UTXO: 2575
    ACCN: 2575
    CHID: 11
    TXID: 508117
```

It is, respectively, number of UTXOs, account index records, number of chains and number of all committed transaction IDs. 
Currently, the ledger state DB keeps IDs of all transactions committed since genesis. In the future state pruning will be implemented. 

The result of the command will be `proximadb` directory in the working directory. 

### 5. Prepare node configuration profile
Run the command `proxi init node`. It will ask to enter some entropy needed for generation of the private key and the
ID of the libp2p host of the node. The private key is used only to secure communications between peers, 
it is not a private key which protects tokens.

The command will create node configuration profile `proxima.yaml` in the working directory. 

You will find host ID of your node in the `peering.host.id` key of the `proxima.yaml` file. It may be needed for the statical
peering with other nodes.

If you plan to run a sequencer on the node later, use `proxi init node -s` for convenience. 
The flag `-s` will put sequencer configuration section template into `proxima.yaml`. 
The sequencer will be disabled, so the node still will be a simple access node. 

The generated file will contain 4 pre-configured static peers for the testnet.

To finish the config file for the testnet node, adjust ports to your environment.

There are additional information embedded as comments right into the generated `proxima.yaml` file.

For example, if you want to expose node's metrics to a Prometheus server , respective `metrics` sector must be adjusted.
Proxima node provides a lot of Prometheus-compatible metrics, all start with prefix `proxima_`.

### 6. Run the node
**Ensure that the clock of your computer is in sync with the global world clock**. 
Few seconds difference is tolerated, but the lesser, the better. 
Significant clock differences between peers may make the network non-operational. 

It is recommended to enable clock time auto-syncing on your computer.

The node is run by typing command `proxima` in the working directory with the node configuration profile and the database. 

The node will sync with the network by pulling all transactions with their past cones along 
the heaviest chain of branches pulled from the peers. Orphaned branches and their past cones won't be synced.

Look for something like this in the log:

```text
[sync] latest reliable branch is 1 slots behind from now, current slot: 75613, coverage: 1_702_419_177_591_708 (1.636152ms)
```

Node is synced if `latest reliable branch` is just few, normally 2-5, slots behind from now and
coverage is at least `1_300_000_000_000_000`.

You also can check current parameters of the network by running `proxi node lrb` command. 

Note that if you use old snapshot file (more than, say, 12 hours old), the syncing process may take much longer and even may fail.

**Disclaimer: testnet version of the Proxima node is unprotected from many of extreme edge-cases and there's no guarantee it will work.**

Node is safely stopped with  `ctrl-C`.

