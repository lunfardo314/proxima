# Local testnet — peering & onboarding edge cases

Reference companion to `local_testnet_runbook.md`. Extracted from session notes
(2026-06-18). All nodes on one laptop, `127.0.0.1`, sharing one genesis.

## Peering config (required for localhost)

Per node `proxima.yaml`:

- distinct ports: peering `400x` / api `800x` / metrics `1400x`.
- `peering.allow_local_ips: true` — **required**; the address filter drops
  `127.0.0.0/8` otherwise, so localhost peers never connect.
- `peering.peers:` = the other nodes as
  `/ip4/127.0.0.1/udp/<port>/quic-v1/p2p/<hostID>`.
- top-level `sources:` = the other nodes' APIs. Configuring `sources` is what enables
  forward sync (no separate on/off flag); with none, forward sync is off.
- node0 keeps `sequencer.standalone: true` (bypasses the libp2p connectivity
  check before submitting — single-node-safe).

Remote testnet boxes are NOT usable as peers from the laptop: WSL2 has no inbound
port-forward / CGNAT, so local-only is the reliable option.

## Shared genesis

Distribute node0's genesis snapshot (`s0-0-…snapshot`) into node1/node2 (and wipe
their DBs) so all three share the ledger identity and restore from it on first
start. The bootstrap chain ID is key-derived (stable across re-genesis); node1's
sequencer chain ID is created fresh each genesis (see runbook).

node0 (bootstrap seq) jumps genesis→current in a single branch on start, even if
the snapshot is hundreds of slots stale.

## Edge case 1 — sequencer won't start from a snapshot older than its own chain

Booting node1 from the genesis snapshot (slot 0) when its sequencer chain was
created at slot ~332 fails: `can't start sequencer: LoadSequencerStartTips …
object not found`, and it does NOT retry after forward-sync catches the state up.

**Fix:** restart the node once it has synced (its chain is then in state → the
sequencer starts). Operator gotcha worth fixing in code (retry after sync rather
than erroring at boot).

## Edge case 2 — a sequencer can't branch if its chain balance exceeds the per-sequencer coverage-contribution UPPER bound (~10% of supply)

A ~40%-supply chain produces only non-branch milestones; every branch proposal is
skipped: `coverage contribution 4e14 out of bounds [~1e12, ~1e14]`. The bound
grows ~60k/slot (effectively fixed at ~10%). The bootstrap (~60-90%) branches fine
because it contributes incrementally from genesis; a freshly-onboarded big-balance
chain injects its whole contribution at once and trips the cap.

**Fix:** drain the chain under ~10% via `proxi node sequencer withdraw` (no
rebuild, no config change — it starts branching automatically once balance <
bound). For a real co-sequencer keep its chain ≤ ~10% of supply.

(Separate from the per-sequencer freeze **upper** bound, which a sequencer can
disable on itself with `set-params --ignore-freeze-bound` — relevant only to
delegation-freeze scenarios.)
