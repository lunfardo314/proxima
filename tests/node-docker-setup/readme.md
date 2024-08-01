# Node Docker Setup

This setup lets you run a Proxima node in the public testnet using Docker.


## Running in access mode

Make sure the peering port 4000 is open.

To start the node execute the command

```bash
./run.sh
```

This will install and run a node in access mode.

Now lets setup a sequencer.


## Setup a sequencer


We don't have a faucet yet, so try to get funds from someone, e.g. ask in proxima discord with your wallet address.

The address can be found in the wallet file `proxi.yaml` in the local directory in `./data/config` and has the following format, e.g. `addressED25519(0x0efcab0fa7441f8bbcb592d5f8350648dfa73c42eba926c56a66d5997a38e278)`. This file also contains the private key. The wallet is created during initialization of the node.

The directory `./data/config` also contains the config file `proxima.yaml` for the node. Both config files are created during the first start of the node and copied to this directory. Changes in the config files are activated after a restart of the node.


Make sure your address has received the funds. To check open a command shell on the docker node in the directory `/app` and issue this command:

```bash
./proxi node balance
```

It should show a non-zero amount on your address, e.g.:

```bash
....
TOTALS:
amount controlled on 1 non-chain outputs: 2_000_000_000_000
TOTAL controlled on 1 outputs: 2_000_000_000_000
```

This also shows that currently no funds are on a chain.

Now you can setup a sequencer with the command. The minimum amount required for a sequencer is 1000000000.

```bash
./proxi node setup_seq --finality.weak mySeq 1000000000000
```

`mySeq` is the name of the sequencer which you can choose yourself.
`1000000000000` is the amount that should be on the chain. You will have to adapt this to your available funds.

Now restart the proxima process to start the sequencer:

```bash
./restart.sh
```

The command

```bash
./proxi node balance
```

should now show also funds on the sequencer chain:

```bash
...
TOTALS:
amount controlled on 1 non-chain outputs: 999_999_999_500
amount controlled on 1 chain outputs: 1_000_000_000_000
TOTAL controlled on 2 outputs: 1_999_999_999_500
```
