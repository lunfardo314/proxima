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
Go there and download the latest snapshot file to the node's working directory (or the directory configured as `snapshot.directory` in `proxima.yaml`).

The snapshot file name is made out of the transaction ID of the branch which represents the snapshot state.

On startup, if the database is missing or corrupted, the node will automatically find and restore from the latest snapshot file in `snapshot.directory` (default: current working directory). If no snapshot is found, the node refuses to start.


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
The database is created automatically on first startup if a snapshot file is present in `snapshot.directory` (default: current working directory). Simply place the snapshot file there and start the node.

Alternatively, you can manually restore with `proxi snapshot restore -v`.
Depending on the computer, it may take several minutes to build the database. Interrupting the process is safe — on next startup the node detects the incomplete restore and re-creates the database from the snapshot automatically.

The result will be a newly created `proximadb` directory in the working directory.


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
Node preserves the consistency of the database even in case of a crash. If the node crashes during sync,
restarting it will continue from the last committed branch. If the database is corrupted, the node
automatically restores from the latest snapshot in `snapshot.directory`.

Node is safely stopped with `ctrl-C`.

One may consider setting up a system service of the Proxima node (to control it with `systemctl`).

For automatic state cleanup, enable `snapshot_restore` in `proxima.yaml` — this periodically
restarts the node and restores from the latest snapshot, keeping the database compact.
Alternatively, periodic restarts via `crontab` (e.g. every 12 hours) can mitigate memory issues.
