# The first library upgrade

> SPEC, implemented on branch `develop-upgrade1` (2026-10-19), uncommitted, rehearsed on a local
> network. Activation slot **not decided**: `ledger.Upgrades` is empty.
> The first live library upgrade of the network: no reset. Two parts that ship in two
> releases: the upgrade machinery (node and wallet code, no ledger change, safe to release
> without any upgrade pending) and upgrade 1 itself (the mine settlement rule of
> `kb/mine_settlement_upgrade.md`). Mechanism reference: `ledger/upgrade.md`.

## Why the machinery first

A library upgrade has existed only on paper: `ledger/upgrade.md` describes it, tests use
placeholder libraries, and no `Build(prevJSON)` function was ever written. A survey of the code
on 2026-10-19 found that a live upgrade would break the network at the activation slot S:

1. **Every wallet and miner transaction would be rejected after S.** A transaction carries the
   upgrade index of the library it was built for (`TxVersion`), and the node rejects a mismatch
   at parse (`transaction/parse.go`). Only the sequencer derives it from the slot; `proxi`, the
   miner and the consolidator hard-code `txbuildercore.New(0)`, and neither the wallet library
   nor the API exposes the index.
2. **Wallets cannot load the library between a node's upgrade and S.** `get_ledger_definition`
   serves the newest library by default, and its commitment proof (`kb/library_proof.md`) needs
   the upgrade UTXO in a reliable branch, which exists only from S on. `proxi` refuses an
   unproven library and stops.
3. **The new rules show at restart, not at S.** An upgraded node stores the new library at
   start (`multistate/genesis.go`, `storePendingUpgrade`), and `ledger.L(base.MaxSlot)`, the
   API's default for constants, returns it at once: the miner version reads 5 days early, an
   old miner stops, a new one is refused by the stream and builds invalid transits.
4. **No guard for a node upgraded after S.** It has committed branches under the old rules and
   never notices. A fresh sync rebuilds the library only if the binary carries the definition,
   and the code holds one pending definition, so a later upgrade could not rebuild this one.
   The production path stores the library with the writer marked "testing only".
5. **No end-to-end test** builds a real upgrade and validates transactions on both sides of S.

## Part 1: machinery (release before upgrade 1 is scheduled)

**M1. Wallet transaction version.** The wallet constants (`txbuildercore.Constants`, served by
`/ledger_constants`) gain the library's `UpgradeSlot` and `UpgradeIndex`, filled in by the
library cache. Every wallet-side builder takes the index of its transaction's own slot instead
of 0: `proxi` stamps every transaction through `glb.SetTimestamp`, which sets the version from
`/ledger_constants?slot=` of the timestamp's slot, and compaction builds with the constants of
its target slot; the miner reads the constants of the very slot it builds for. The node holds an
upgrade's library from its start, so the per-slot answer is right on both sides of S, including
the slot or two after S in which the default constants are still the previous library's. The
consolidator takes the active library's index, re-read every tick (`followLibraryUpgrade`): a
transaction it builds in that gap is rejected and rebuilt on the next tick.

**M2. Serve the active library.** `get_ledger_definition` and `/ledger_constants` without a slot
serve the newest library **whose commitment the node can prove**: before S, and in the slot or two
after S until the upgrade UTXO reaches a reliable baseline, the previous one. With a slot they
serve that slot's library as today. A wallet therefore never sees an unprovable library, and a
long-running process (miner, consolidator) notices an activation as a change of the served
`UpgradeSlot` and reloads.

**M3. The miner across an upgrade.** The miner reads the constants of the slot it mines for:
the required miner version (stream handshake, the version written into the transit) and the
upgrade index (transaction version). A miner whose own version is below the slot's requirement
stops and says why (as today). One built for a later version mines the earlier rules until S:
a v5 miner builds v4 transits before upgrade 1 and v5 after, so miners can update before S and
nothing has to happen at S itself.

**M4. Upgrade history in the binary.** `ledger.PendingUpgrade` becomes the list
`ledger.Upgrades`; at start a node builds and stores every listed upgrade it does not hold, in
order, with the checked writer (`WriteUpgradeLibrary`, genesis identity enforced). A node synced
from genesis years later still rebuilds every library.

**M5. Late-node guard.** At start, before storing an upgrade at slot S: if the node's state
already holds committed branches at or after S, it committed them under the old rules, so it
refuses to start and tells the operator to restore a snapshot taken after S (snapshots carry
every library) or one taken before S. Building an upgrade whose slot is not ahead of the latest
committed branch is refused for the same reason.

**M6. Tests.** An end-to-end test in `ledger/tests` builds upgrade 1 from the genesis library,
stores it at a slot S of a test ledger and validates real transactions on both sides of S
(transaction version, the mine rule, the miner version). Node-level tests for M2, M4 and M5.

## Part 2: upgrade 1 (release that sets S)

Upgrade 1 is pure EasyFL, applied by `BuildUpgrade1Library` (`ledger/def_upgrade1.go`) from the
commented source `ledger/def/upgrade1.easyfl`, converted to JSON definitions at build (EasyFL can
replace a function only through JSON):

- new constant `constMineSettlementWindowTicks` = 16 (equal to the Go
  `txbuildercore.MineSettlementWindowTicks`);
- `_mineShape` replaced (same arity) with one more requirement: the transit's own tick is at
  least `ticksPerSlot64 - constPreBranchConsolidationTicks - constMineSettlementWindowTicks -
  constTransactionPaceSequencer` (84 today), so no output of a transit can be consumed before
  the settlement window, by any sequencer or build;
- `constMinerVersion` replaced: 4 -> 5.

The miner stamps a transit for slot s at tick 84 when the slot's miner version is 5.
Nothing else changes: payout layouts, the tag-along lock and the sequencer are as they are.

## Rollout

1. Release the machinery (Part 1). Every node, `proxi` and miner must run it before S: an older
   wallet builds transactions with version 0 and is rejected from S on.
2. Release upgrade 1 with S chosen at least several days ahead (the activation slot is written
   into `ledger.Upgrades`). Nodes and miners update before S. At S the first branch carries the
   upgrade UTXO, a slot or two later wallets are served the new library.
3. Monitor: rejected transactions with "TxVersion mismatch", the mining stream's refusals, the
   miner version in the monitor, the weekly watch's early takes (should drop to zero).

## Rehearsal (before choosing S)

On the local machine: unit and end-to-end tests (M6); a local multi-node network with a test
build activating upgrade 1 a few minutes ahead, a miner running across S, and a node started
after S (M5) and from a snapshot taken after S.

### Rehearsal results (2026-10-19)

A local network from a fresh genesis, built from the branch with a scratch-only patch listing
upgrade 1 at slot 60 (never committed): the bootstrap sequencer with `proxi node mine` (4
workers), an access node, and later nodes joining.

- Before S the node listed the slot 60 library as PENDING and served the genesis library by
  default (miner version 4, index 0) and the upgrade one only for `?slot=60`. The miner, built
  as version 5, mined version 4 transits stamped at tick 1.
- At S, `LEDGER UPGRADE ACTIVATED AT SLOT 60`; the miner's first transit for slot 60 was stamped
  at tick 84 under version 5 and every later one too; no transit was refused. A few slots later
  the default constants were version 5, index 1. A wallet withdraw built after S (version 1)
  committed.
- A node started from genesis after S synced across it, the same LRB as the bootstrap node.
- The `develop` binary (no upgrade) refused every transaction from S on, `TxVersion mismatch:
  transaction has 1, library expects 0`, and stayed at genesis; restarted on the new binary it
  stored the upgrade and caught up.
- A node restored from a snapshot taken at slot 65 by a binary that does **not** list the
  upgrade ran the upgraded rules and synced: the snapshot carries the library.
- Found and fixed: at S the stream server closes subscribers on the old miner version, and the
  miner took the close as a ban, reconnecting only after 30 s. It now reconnects at once when
  the close is for the version it has just left; confirmed by a second run from a fresh genesis
  with S at slot 15.
- Not reachable on a live network, so tested only in `multistate`: the M5 guard (a state holding
  branches at or after S without the upgrade). An old binary cannot commit such a branch, since
  every transaction at S or later carries the new version.

### Open points

- **Wallet transactions in the slot or two after S** (resolved 2026-10-19): until the upgrade UTXO
  reaches a reliable baseline the default constants are the previous library's, so a `proxi`
  transaction stamped after S carried the old index and was rejected. `proxi` now takes the index
  of the timestamp's slot (`glb.SetTimestamp`); the consolidator still retries on its next tick. A third
  run (S at slot 12) confirmed it: at slot 12 tick 2, with the default constants still at index 0,
  a `proxi node seq withdraw` stamped in slot 12 committed, and the node logged no version
  mismatch.
- When a local snapshot and a configured `sources` node both exist, snapshot restore preferred
  the remote genesis snapshot over the local slot 65 one (seen in the rehearsal). Not an upgrade
  issue, but an operator restoring past S should know it.
