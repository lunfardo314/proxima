# Local testnet runbook (laptop, 3 nodes)

Generic procedure to (re)start the local Proxima net on the laptop. Test
scenarios (specific `proxi`/API command sequences + expected outputs) live in
separate files, not here. Peering and onboarding edge cases:
`local_testnet_edge_cases.md`.

**The net can be started from scratch (fresh genesis) or restarted (reuse DBs).
Always ask the user which one before acting.**

## Layout

Dirs under `/mnt/c/Users/evaldas/Desktop/proxima/`:

| dir   | role                | peering | api  | metrics |
|-------|---------------------|---------|------|---------|
| node0 | bootstrap sequencer | 4000    | 8000 | 14000   |
| node1 | 2nd sequencer       | 4001    | 8001 | 14001   |
| node2 | access node         | 4002    | 8002 | 14002   |

What persists vs. what is disposable:

- **Always reused:** `proxima.key` (node0 + node1) and the wallet profiles
  `proxi.yaml`. `proxima.yaml` is normally reused, but may need tuning per
  scenario.
- **Reused on restart, deleted on fresh start:** DB dirs, `*.snapshot`, `*.log`,
  `run.out`, `.snapshot_restore.json` — all recreated; DBs restore from the
  genesis snapshot.

Chain IDs:

- **node0 bootstrap chain ID is key-derived** → stable across re-genesis
  (`9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82`). node0's `proxima.yaml`
  (`sequencer.chain_id`) and `proxi.yaml` (`default_sequencer_id`,
  `wallet.sequencer_id`) already match; no edit needed after re-genesis.
- **node1's sequencer chain ID is created fresh each time** the net is genesised
  (it is the origin of the funding/init transaction). After `init genesis` on
  node1, write the printed chain ID into node1 `proxima.yaml` `sequencer.chain_id`
  and `proxi.yaml`.

## 0. Build binaries (with current code)

```bash
cd ~/go/src/github.com/lunfardo314/proxima
export PATH="/usr/local/go/bin:$PATH"
go build -o "$CLAUDE_JOB_DIR/tmp/proxima" .
go build -o "$CLAUDE_JOB_DIR/tmp/proxi"   ./proxi
"$CLAUDE_JOB_DIR/tmp/proxima" version    # confirm commit hash
```

## A. Start from scratch (fresh genesis)

1. **Wipe disposable state**, keeping keys + configs:
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima
   for d in node0 node1 node2; do
     find "$d" -mindepth 1 \
       ! -name proxima.key ! -name proxima.yaml ! -name proxi.yaml -delete
   done
   ```
2. **Genesis on node0** (offline, from its key; no entropy prompt — that is only
   for new-key creation). Output dir = node0 (its `snapshot.directory` is `""` =
   cwd); distribute the same snapshot to node1/node2 per
   `local_testnet_edge_cases.md`.
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima/node0
   "$CLAUDE_JOB_DIR/tmp/proxi" init genesis -o . -f
   ls s0-0-*.snapshot
   ```
3. Proceed to **Bootstrap** below.

## B. Restart (reuse existing DBs)

Just relaunch each node from its dir (configs + DBs intact). node0 (bootstrap,
standalone) catches up by itself; node1/node2 forward-sync from sources. If a
sequencer was down long enough that the snapshot predates its chain, see edge
case 1.

## Bootstrap (bring-up order, fresh net)

1. **Run node0** (bootstrap sequencer, `sequencer.standalone: true`):
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima/node0
   nohup "$CLAUDE_JOB_DIR/tmp/proxima" > run.out 2>&1 &
   until curl -s -m 2 http://127.0.0.1:8000/api/v1/get_ledger_id >/dev/null; do sleep 2; done
   grep -aE "SUBMIT BRANCH" run.out | tail -2     # producing branches => healthy
   ```
2. **Fund the bootstrap controller's spendable account** from the sequencer chain.
   Genesis places ~100% of supply in the bootstrap *chain* output; the only
   spendable seed is the **1-mote controller dust output**
   (`GenesisControllerDustOutput`, sigLock to the controller, index 2 of the
   genesis tx, indexed under the controller account). It is enough to pay the
   tag-along fee (`1`) of a `withdraw` request, which pulls a real amount out of
   the sequencer:
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima/node0
   "$CLAUDE_JOB_DIR/tmp/proxi" node sequencer withdraw 1000000000 -f
   # default target = the wallet's own account (no -t needed)
   ```
   The request commits, then the sequencer fulfils it in a later milestone;
   `proxi node balance` then shows the withdrawn amount as a plain sigLock output.

   > **CRITICAL — dust is single-use; `withdraw` MUST be the FIRST wallet command.**
   > Every tag-along command (`withdraw`, `set-params`, `delegate amount`, …)
   > consumes a wallet output for its fee. There is exactly ONE dust mote after a
   > fresh genesis. If any other command runs first (e.g. `set-params
   > --ignore-freeze-bound`), it eats the dust and the wallet is left empty —
   > subsequent `withdraw` fails `wallet has no outputs to create transaction`.
   > Recovery: re-genesis (wipe DB + regenerate snapshot, path A) to restore the
   > dust. Always: fund via `withdraw` first, then everything else.
3. **Start node2** (access node):
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima/node2
   nohup "$CLAUDE_JOB_DIR/tmp/proxima" > run.out 2>&1 &
   ```
4. **Fund node1's account** from the bootstrap (keep node1's eventual chain
   balance ≤ ~10% of supply — edge case 2):
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima/node0
   "$CLAUDE_JOB_DIR/tmp/proxi" node sequencer withdraw <amount> -t a/<node1 holder ID> -f
   ```
   (node1's holder ID is in its keystore / `proxi.yaml` `wallet.holder_id`.)
5. **Init node1's sequencer** and record its chain ID:
   ```bash
   cd /mnt/c/Users/evaldas/Desktop/proxima/node1
   "$CLAUDE_JOB_DIR/tmp/proxi" node sequencer init_genesis <amount> --name node1
   ```
   Put the printed chain ID into node1 `proxima.yaml` (`sequencer.chain_id`,
   `enable: true`) and `proxi.yaml`.
6. **Run node1** (2nd sequencer). If it errors `LoadSequencerStartTips … object
   not found` (snapshot older than its just-created chain), restart it once it
   has synced — edge case 1.

## Tests

Run the scenario files planned for the session (each is a separate doc with its
`proxi`/API commands + expected outputs). Monitor via logs, `sync_info`, metrics
(`:1400x`). This runbook stays scenario-independent.

## Stop

```bash
pkill -f "$CLAUDE_JOB_DIR/tmp/proxima" || pkill -f 'proxima$'
```

Do **not** erase configs/keys — they are reused. To start clean next time, run
path A (wipe disposable state).
