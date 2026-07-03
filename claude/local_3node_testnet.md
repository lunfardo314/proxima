# Local 3-node test network (bootstrap + 2nd sequencer + access)

A localhost multi-sequencer network for exercising sync / restart / snapshot /
consensus edge cases without touching the remote testnet. Everything runs on
`127.0.0.1`, sharing one genesis. (The remote testnet boxes can't peer with a
WSL/laptop node — no inbound reachability behind NAT/CGNAT — so local-only is the
reliable option.) First stood up & exercised 2026-06-18.

## Layout

| node  | role                 | dir (`/mnt/c/Users/evaldas/Desktop/proxima/`) | peering | api  | metrics | supply |
|-------|----------------------|-----------------------------------------------|---------|------|---------|--------|
| node0 | bootstrap seq `boot` | `node0`                                       | 4000    | 8000 | 14000   | ~90% (chain `9d2c6fedeb0f…`) |
| node1 | 2nd sequencer `node1`| `node1`                                       | 4001    | 8001 | 14001   | ~8% (chain `e56fdfe3…`, after drain) |
| node2 | access node          | `node2`                                       | 4002    | 8002 | 14002   | — |

Each node dir holds: `proxima.yaml`; `proxima.key` (node0/node1 — the controller
key; access node needs none, its libp2p host key is inline in `proxima.yaml`); a
genesis/recent `*.snapshot`; and `proxi.yaml` (node0/node1 wallet profiles).

## Binaries

```
go build -o <stable>/proxima .          # node
go build -o <stable>/proxi   ./proxi    # CLI
```
Don't leave them in an ephemeral job tmp dir if the nodes must survive across
sessions — keep them next to the node dirs.

## Config essentials (what must differ from a stock `proxi config node`)

- **Distinct ports** per node (peering / api / metrics) as in the table.
- **`peering.allow_local_ips: true`** — REQUIRED; the address filter drops
  `127.0.0.0/8` by default, so localhost peering won't form without it.
- **`peering.peers:`** — localhost multiaddrs to the other nodes:
  `<name>: /ip4/127.0.0.1/udp/<port>/quic-v1/p2p/<that node's peering.host.id>`.
- **`sources:`** (top-level) = the other nodes' API URLs. Configuring `sources` is
  what enables forward sync (there is no separate on/off flag); with none it is off.
- **node0**: keep `sequencer.standalone: true`. **node1**: `sequencer.enable: true`
  + `sequencer.chain_id: <its chain>`. node2: no sequencer section.
- Snapshot serving (only needed to test peer download): `snapshot.enable: true`,
  `period_in_slots: <small for testing>`, `enable_download_api: true`,
  `always_serve: true`. `snapshot.directory: ""` (= cwd) is fine.

## Genesis / ledger identity

All nodes share one genesis snapshot and restore from it on first start (empty
DB). To create a fresh ledger: `cd node0 && proxi init genesis -o .` (uses node0's
wallet key as genesis controller; the genesis time becomes the ledger identity, so
a regen is a NEW ledger), then copy that `s0-…snapshot` into node1/node2 and wipe
their DBs.

## Bring-up

1. Start node0 (bootstrap). It jumps genesis→current in one branch even if genesis
   is stale.
2. Create node1's sequencer chain (node0 must be running):
   - `cd node0 && proxi -f node seq withdraw <amt> -t a/<node1_holderID>`
     (holder_id is in `node1/proxima.key`).
   - `cd node1 && proxi -f node seq init_genesis <amt> --name node1` → prints the
     new chain id and writes it to `node1/proxi.yaml` `wallet.sequencer_id`. Put it
     in `node1/proxima.yaml` `sequencer.chain_id` and set `enable: true`.
   - **Keep the chain ≤ ~10% of supply** (see finding 2). If you funded more, drain
     it: `cd node1 && proxi -f node seq withdraw <excess> -t a/<addr>` — once the
     balance drops under the bound the sequencer starts branching, no restart.
3. Start node1, then node2.

## Run / stop

- Start: `cd <nodeDir> && <stable>/proxima` (writes `proxima.yaml`'s `logger.output`,
  default `proxima.log`).
- Stop: `kill -INT <pid>` (graceful; `SUBMIT BRANCH`/DB flush then "Hasta la
  próxima"). Find pid by port: `ss -ltnp | grep :<apiPort>`.
- Inspect: `proxi node balance` / `seq info` (wallet dir), `curl :<api>/api/v1/get_latest_reliable_branch`, or the per-node `proxima.log`.

## Tested scenarios (2026-06-18) — all PASS unless noted

- **Genesis bootstrap** (stale-genesis jump), **immediate restart**, **downtime
  restart** — clean catch-up, no false sync trigger, no leak.
- **Recursive-sync join** (gap < depth cap): genesis→tip in seconds; forward sync
  correctly stays idle (counter never trips).
- **Forward-sync trigger** (node >50 branches behind, access node AND returning
  sequencer): `starting forward-sync (N attacher(s) at depth cap, gap=…)` → pull &
  commit the gap → `no attacher at the depth cap — going idle` → caught up. The
  counter-based, no-hysteresis trigger works end-to-end.
- **Liveness**: dropping the ~8% sequencer (node1) → net stays healthy (node0 holds
  > healthy-coverage fraction). Dropping the bootstrap (node0, ~90%) → net stalls
  (node1's 8% branches are unhealthy: `coverageDelta below health threshold`) →
  **recovers on bootstrap restart**. Boot immediate-restart ≈ zero disruption.
- **Snapshot**: production ✓; restore-from-non-genesis-snapshot + sync ✓.
- **Snapshot peer-download** ✗ → root-caused and FIXED (finding 3).

## Findings (operator gotchas)

1. **Sequencer won't start from a snapshot older than its own chain.**
   `LoadSequencerStartTips … object not found`, and it does not retry after sync
   catches up. Workaround: restart once synced (chain now in state). (Code TODO:
   wait/retry instead of erroring at boot.)
2. **A sequencer can't branch if its chain balance exceeds the per-sequencer
   coverage-contribution upper bound (~10% of supply).** It only emits non-branch
   milestones (`coverage contribution … out of bounds [~1e12, ~1e14]`; the upper
   bound grows ~60k/slot, effectively fixed). The bootstrap (~60-90%) is fine
   because it contributes incrementally from genesis. Keep co-sequencers ≤ ~10%;
   drain an over-funded chain to fix.
3. **Snapshot peer-download was broken with `snapshot.directory: ""`** — FIXED.
   Production and local-restore resolve the dir via `snapshot.SnapshotDirectory()`
   (empty → `"."`), but the serve API's `node/apiserver.go GetSnapshotFilePath()`
   defaulted empty to `"snapshot"`, so a node saving to its cwd served the download
   API from a non-existent `./snapshot/` → `cannot read snapshot directory
   'snapshot'` → peers can't fetch → a wiped node FATALs instead of restoring. Fix:
   serve side now uses `snapshot.SnapshotDirectory()`. Also fixed the config
   template key (`enable_api` → `enable_download_api`) earlier.

## Caveat learned the hard way

`tryDownloadRemoteSnapshot` runs on a missing DB, but **if it can't download, the
node FATALs** — there is no genesis-from-thin-air. Wiping DB **and** all local
snapshots on *every* node at once destroys the ledger (no surviving source).
Always keep at least one node with a valid DB or a snapshot.
