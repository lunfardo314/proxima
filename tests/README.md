# tests

Two different things live here: Go tests that run the node in-process, and
material for standing up real node processes on one machine.

```bash
go test ./tests/...                 # in-process; several suites are slow
go test -race ./tests/...           # required after any change to the core
```

Sequencer suites in this package are best run one at a time
(`go test -run '^TestName$' ./tests/...`); the package as a whole tends to
exceed the default timeout.

## Running real nodes locally

Four setups, in increasing order of effort. Pick by what you are trying to
exercise.

| Setup | Nodes | Use it for |
|-------|-------|------------|
| **Standalone** (below) | 1 | Wallet and API work, `proxi` behaviour, anything that needs a live node but not consensus. |
| **Three-node network** (below) | 3 | Sync, restart, snapshot and consensus edge cases; two sequencers competing. |
| `nodes/README.md` | 5 | Pre-generated configuration for a larger manual network. |
| `docker/` | 5 | A docker-based network, maintained separately. |

Everything below runs on `127.0.0.1`, sharing one genesis.

### Build

```bash
go build -o proxima .
go build -o proxi ./proxi
```

Keep the names `proxima` and `proxi`.

## Standalone node

The quickest way to get a live node. One process, its own bootstrap sequencer,
no peering.

```bash
cd <workdir>
proxi config wallet                  # creates proxima.key + proxi.yaml
proxi config node --standalone       # creates proxima.yaml AND the genesis snapshot
proxima                              # run it
```

`config wallet` generates a key if `proxima.key` is not already there, and
reuses it if it is. It prompts for entropy, so it needs a terminal — from a
script, run it under a pty.

`config node --standalone` writes both the node configuration and the genesis
snapshot; there is no separate `init genesis` step in this path.

The node takes twenty to forty seconds to produce its first branches. Poll
`proxi node info` until it prints an LRB branch ID and a `sequencer:` line.

At that point everything is on the bootstrap sequencer's chain and your ordinary
balance is empty. Move some out before doing anything else:

```bash
proxi node balance
proxi node sequencer withdraw 500000000
```

**Re-deploy from an empty directory after any breaking ledger change.** The
library hash changes, and an old snapshot or database will not load against new
definitions.

## Three-node network

A bootstrap sequencer, a second sequencer, and an access node. This is the
smallest setup where consensus is real: two sequencers compete, and the access
node has to sync.

Give each node its own directory and its own ports:

| dir     | role                | peering | API  | metrics |
|---------|---------------------|---------|------|---------|
| `node0` | bootstrap sequencer | 4000    | 8000 | 14000   |
| `node1` | second sequencer    | 4001    | 8001 | 14001   |
| `node2` | access node         | 4002    | 8002 | 14002   |

What persists across restarts and what does not:

* **Keep**: `proxima.key` for each sequencer node, and the wallet profiles
  `proxi.yaml`. `proxima.yaml` is normally reused but often needs tuning per
  scenario.
* **Disposable**: database directories, `*.snapshot`, logs, and
  `.snapshot_restore.json`. Delete these for a fresh start; databases are
  rebuilt from the genesis snapshot.

### Bringing it up from a fresh genesis

1. Wipe the disposable state in all three directories, keeping keys and configs.
2. Create the genesis snapshot from the bootstrap node's key (`proxi init
   genesis`) and give the **same** snapshot to all three nodes. They must share
   one genesis or they will not peer.
3. Start `node0` and wait for it to produce branches.
4. Create the second sequencer's chain (`proxi node sequencer init_genesis` from
   `node1`'s wallet, funded from `node0`), then write the printed chain ID into
   `node1`'s `proxima.yaml` under `sequencer.chain_id` and into its `proxi.yaml`.
5. Start `node1`, then `node2`.

The bootstrap sequencer's chain ID is derived from its key, so it survives
re-genesis and needs no edit. The second sequencer's chain ID is created fresh
every time the network is re-genesised — step 4 is not optional.

### Peering on one machine

Localhost peering needs explicit configuration: nodes will not discover each
other on `127.0.0.1` the way they would on a real network. Each node needs the
others listed as static peers, with their peering ports and host IDs from the
`peering` section of their `proxima.yaml`.

## Edge cases worth knowing

These have all cost time before.

**A sequencer will not start from a snapshot older than its own chain.** If you
restore an old snapshot under a sequencer whose chain has moved past it, the
node refuses to start rather than forking. Re-genesis, or use a newer snapshot.

**A sequencer cannot produce branches if its chain holds too much of the
supply.** There is an upper bound on how much one sequencer may contribute to
coverage — roughly a tenth of supply. On a small local network it is easy to
exceed it by accident, especially right after genesis when the bootstrap chain
holds nearly everything. Withdraw from the sequencer chain, or spread the tokens
across sequencers. The bound is per-sequencer and can be relaxed on a test
network with `proxi node sequencer set-params enforce_freeze_bounds`.

**Fund a fresh wallet before setting sequencer parameters.** Commands that submit
a transaction need a tag-along fee, and a wallet holding nothing but its chain
cannot pay one. Withdraw to an ordinary output first.

**A breaking ledger change invalidates everything on disk.** Snapshots and
databases are tied to the library hash. After such a change, start from an empty
directory.
