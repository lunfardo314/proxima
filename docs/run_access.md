## Running access node

The following are step-by-step instructions of how to start Proxima access node and sync it with the network.

The **access node** is the simplest configuration of the node. It does not run sequencer as part of it. 
Anybody can run an access node, no tokens are needed.
The main functions of the access node are:
- keep connections with peers
- keep valid the multi-ledger state in sync with the network
- provide API access to the network for the Proxi wallet and other programs
- gossip new transactions that are coming to the node from other peers and from the API to the network
- submit new transactions to the transaction store database

### 1. Compile
Clone the repository to `<your_dir>/proxima`.

Run `go install -v` in working directories `<your_dir>/proxima` and `<your_dir>/proxima/proxi`.
This will create executables: `proxima` for the node, and `proxi` for the CLI program with simple wallet functionality and tools.

Run `proxi -h`, `proxi init -h`, `proxi db -h` etc., to check if it works.

Below we assume we use the same working directory for all configuration profiles and databases.

### 2. Download snapshot file
At least one of the testnet nodes is constantly producing multi-state database snapshots. 

The temporary place for download is http://83-229-84-197.cloud-xip.com/downloads/ . 
Go there and download the latest snapshot file to the directory on your computer where the node's database will reside. 

The snapshot file name is made out of the transaction ID of the branch which represents the snapshot state. 

### 3. Check of snapshot file if suitable to start a node
In the directory with the downloaded snapshot file run a command:

`proxi snapshot check --api.endpoint <APIendpoint>`

This command checks the snapshot file against the current ledger state that is seen in the latest reliable branch (LRB)
of the specified node in the network. It makes sure that the branch of the snapshot is in the past cone of all current branches
on the network. This prevents situations when the branch of the snapshot was orphaned (small yet positive chances).

If you see something like:
```text
latest reliable branch (LRB) is [101018|0br]01b14af9f0eae05b1e457ae140d6812a47e8151f10ac382c97d92a
the snapshot:
      - is INCLUDED in the current LRB of the network. It CAN BE USED to start a node
      - is 889 slots back from LRB and 890 slots back from now
```

Command `proxi snapshot check_all --api.endpoint <APIendpoint>` scans all snapshot files in the current directory and check each of it.

### 4. Create a multi-state database
In the directory with the snapshot file run command `proxi snapshot restore -v`.
Depending on the computer, it may take several minutes to build the database. Interrupting the process makes DB inconsistent,
the `proximadb` directory must be deleted and the command run again.

Upon finish, the command will report statistics of snapshot file.

The result of the command will be a newly created `proximadb` directory in the working directory. 

### 5. Prepare node configuration profile
Run the command `proxi init node`. It will ask to enter some entropy needed for generation of the private key and the
ID of the libp2p host of the node. The private key is used only to secure communications between peers, 
it is not a private key that protects tokens.

The command will create node configuration profile `proxima.yaml` in the working directory. 

You will find host ID of your node in the `peering.host.id` key of the `proxima.yaml` file. It may be needed for the statical
peering with other nodes.

If you plan to run a sequencer on the node later, use `proxi init node -s` for convenience. 
The flag `-s` will put sequencer configuration section template into `proxima.yaml`. 
The sequencer will be disabled, so the node still will be a simple access node. 

The generated file will contain 4 pre-configured static peers for the testnet.

To finish the config file for the testnet node, adjust ports to your environment.

There is additional information embedded as comments right into the generated `proxima.yaml` file.

For example, if you want to expose node's metrics to a Prometheus server, respective `metrics` sector must be adjusted.
Proxima node provides a lot of Prometheus-compatible metrics, all start with prefix `proxima_`.

### 6. Run the node
**Ensure that the clock of your computer is in sync with the global world clock**. 
Difference of few seconds is tolerated, but the lesser, the better. 
Significant clock differences between peers may make the network non-operational. 

It is recommended to enable clock time auto-syncing on your server system.

The node is run by typing command `proxima` in the working directory with the node configuration profile and the database. 

The node will sync with the network by pulling all transactions with their past cones along 
the heaviest chain of branches pulled from the peers. Orphaned branches and their past cones won't be synced.

Look for something like this in the log:

```text
[sync] latest reliable branch is 1 slots behind from now, current slot: 75613, coverage: 1_702_419_177_591_708 (1.636152ms)
```

Node is synced if `latest reliable branch` (LRB) is just few, normally 1 to 3, slots behind the current slot and
coverage is at least `1_300_000_000_000_000`.

You also can check current parameters of the network by running `proxi node lrb` command. 

Note that if you use an old snapshot file (more than, say, 12 hours old), the syncing process may take much longer and even may fail. 
Node preserves the consistency of the database even in case of a crash. So, if the node crashes during the sync process, 
restarting it will usually continue from the last commited branch.

Node is safely stopped with  `ctrl-C`.

One may consider setting up a system service of the Proxima node (to control it with `systemctl`). 
It is also recommended to put periodic restart of the node on the `crontab`, say every 12 hours (e.g. `sudo systemctl restart proxima-node-service`). 
That would mitigate memory leak problems which may still be present. 
