# Running access node

The following a step-by-step instruction how start a Proxima access node and sync it with the network.

The **access node** is the simplest configuration of the node. It does not run sequencer as a part of it.
Its main functions are:
- keep valid multi-ledger state in sync with the network
- provide API access to the network for the Proxi wallet and other programs

Running the node do not require to be a token holder. 

### 0. Compile
Clone the repository to `<your_dir>/proxima`.

Type `go install` in working directories `<your_dir>/proxima` and `<your_dir>/proxima/proxi`.
This will create executables: `proxima` for the node, and `proxi` for the CLI program for simple wallet and tools.

Run `proxi -h`, `proxi init -h`, `proxi db -h` to check if its is working.

Below we assume we use same working directory for all configuration profiles and databases.

### 2. Copy ledger ID file
To start the node, the ledger ID file `proxima.genesis.id.yaml` used to create genesis is a prerequisite.
Copy it from the bootstrap directory [Running first node in the network](bootstrap.md) to you node's location
or obtain it any other way. The YAML data in the file must be **exactly** the same as the one used for genesis.

### 3. Create genesis database

The database with the genesis state will be created from the ledger ID file `proxima.genesis.id.yaml`
by running the following command in the same directory: `proxi init genesis_db`

The directory named `proximadb` will appear in the working directory. 

Run commands `proxi db info` and `proxi db accounts` to check if everything is ok. 

### 4. Prepare node configuration profile

Run the command `proxi init node`

It will create node config profile `proxima.yaml` in the working directory. The node libp2p private key and ID 
will be randomly generated from the provided seed. 

If you plan to run a sequencer on the node later, use `proxi init node -s` for convenience. 
The flag `-s` will put sequencer configuration section template into `proxima.yaml`. 
The sequencer will be disabled, so the node still will be a simple access node. 

To finish the config file for the node, adjust it the following way:
* ports must be adjusted to the environment, if needed
* at least one static peer must be added into the `peering.peers` section. 
It must be added as a pair `<local peer name>: <libp2p multi-address>`. 
* make sure the value of the `peering.peers.max_dynamic_peers` is at least 1, normally 3.

Example of the `peering` section:
```yaml
peering:
  peers:
    boot: /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWT1MQM1kXKRaj6j9xjzVzUCWcrzihnVicepn82dTDkNYM
    friendlyPeer: /ip4/127.0.0.1/tcp/4002/p2p/12D3KooWGpPKLTg4srCokDmdRZefCUQtVnzuaRzcJxZtBxUAkmy2
  max_dynamic_peers: 3
```

### 5. Run the node
The node with the bootstrap sequencer in it is run by typing command `proxima`. 

Node will sync with the network by pulling all transaction along the heaviest chain of branches from the peers. 
Orphaned branches and their past cones won't be synced.

If node freezes at startup, `ctrl-C` and restarting the node usually helps :)

If you have `proxi` wallet profile configured with correct API endpoint, you can access the network via the node using 
commands `proxi node ...`

