# Node Docker Setup

This setup lets you run a Proxima node in the public testnet using Docker.

The only tools required are git and docker.
The setup was tested on Linux and Windows 11 with WSL2.

First get the Proxima repository and checkout the newest release tag with these commands:

```bash
git clone https://github.com/lunfardo314/proxima.git
cd proxima
git checkout <newest release tag>
```

Then change the directory:
```bash
cd tests/node-docker-setup
```
Now we can run an access node.


## Running an access mode

Make sure the peering port 4000 is open.

To start the node execute the command

```bash
./run.sh
```

This will build (if started for the first time) and run an access node.

After the node is started the following directories are created under `./data/`:

<div align="center">
    <img src="image.png" alt="Alt text" width="340">
</div>

`config` contains:
- `proxima.yaml` with the node settings, e.g. node id
- `proxi.yaml` with your wallet settings, e.g. account address
- `proxima.key` with your secret key. **Backup this file to another location!**

You can adapt these files to your needs. The changes will be copied to the docker image with a restart.
To restart the node, press CTRL+C and then `./run.sh`.


## Playing with the access node

The CLI wallet program `proxi` is used for the following actions.
For a comprehensible overview of this tool, look into [docs/proxi.cmd](https://lunfardo314.github.io/#/participate/proxi)

To access this tool on the node you have to attach a shell to the docker node. One way to achieve this would be with visual studio code using its docker extension.
You can also ask ChatGPT how to attach a shell with the docker tools (ask "How to attach a shell to a docker node?").


### Mining funds

The proxi tool can be found in the directory `/app` on the node.

First check the balance of the wallet:

```bash
./proxi node balance
```

To mine funds use

```bash
./proxi node mine
```
After some time (depending on your hashing power) you should have funds in your account.


### Spamming

For spamming some settings have to be made in `./data/config/proxi.yaml` under the `spammer` section:

set the `sequencer_id` under `tag_along` to a valid sequencer id, e.g. `6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc`.

set the `target` to a valid target lock, you can use the account address of your wallet for example.

Now restart the node to activate the changes in `proxi.yaml`.

Now you can start spamming with the command

```bash
./proxi node spam
```

## Setup a sequencer

To setup a sequencer you can use the steps described in [docs/run_sequencer.cmd](https://lunfardo314.github.io/#/participate/run_sequencer)

Remember that you have to edit the config files in `./data/config/`.

Alternatively there is now also the tool `SetupSequencer` available, that can setup a sequencer, e.g.:

```bash
./SetupSequencer "seq1" 4000000000000
```
This call initializes a sequencer with the name "seq1" using 499990000000 funds.
The maximal supported length for the name is 6 characters.

After the call the node has to be restarted manually to start the sequencer.
