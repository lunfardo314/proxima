# Proxima monitor page — spec 0

Status: **spec 0** (provisional, for approval). Process: spec 0 → approve →
prototype → spec 1 (the buildable spec). Spec 0 fixes *what* the page shows and
*where each number comes from*; it deliberately leaves open the questions the
prototype exists to answer (marked **TBD-p**).

Date: 2026-08-06 (refined 2026-08-07 after review)

---

## 1. What it is

A single read-only browser page giving a **high-level overview of where the
Proxima ledger and network stand**: not a node dashboard, not a chain browser —
a state-of-the-network page a newcomer, a token holder, or the project itself
can look at and immediately see supply, distribution, fair-launch progress and
decentralization.

**Standalone.** It is *not* part of `/chain_explorer`. The chain explorer is a
per-chain browsing tool (rows, filters, per-chain UTXO popup); the monitor is
aggregate-only and covers mining and decentralization, which the explorer does
not. They may share Go helpers (chain classification, LRB header aggregates),
but the page, the route and the JSON endpoint are separate.

Working names: route `/monitor`, backend `/api/v1/monitor` (final naming TBD).

**Non-goals**
- No per-transaction / per-chain drill-down (that is `/chain_explorer`,
  `/dag_explorer`, `/dagviz`).
- No node operator health (that is `/dashboard`, `/peers`, Prometheus).
- No write path, no wallet, no config.
- Not a Prometheus/Grafana replacement: the monitor is *ledger-wide* truth read
  off the LRB state, not per-node time series.

**Audience and tone.** One screen, three sections, big numbers with a short
label each, drill-down only via links to the existing tools.

---

## 2. Mixed freshness is the organizing principle

Some values are cheap and can be refreshed continuously; others need a full
state traversal and can only be refreshed every so many minutes. The page
combines both and **tells the user how old each value is**.

Three freshness tiers:

| Tier | Refresh | Typical source | Displayed as |
|---|---|---|---|
| **live** | ~15 s | LRB branch aggregates, mine chain tip, sequencer/peer APIs | plain value |
| **periodic** | minutes (configurable) | full-state census, snapshot-time stats | value + "as of slot N, HH:MM ago" |
| **historical** | on demand / background | txstore back-walk, Prometheus series | series with its own window label |

Rules this imposes on the design:
- every field carries its own `as_of` (slot + wall clock), never one page-wide
  timestamp;
- a periodic value that has never been collected renders as "not yet
  collected", not as zero;
- the page must be fully useful with only the live tier available — periodic
  and historical tiers degrade to "unavailable" without breaking the layout.

### 2.1 The monitor module

It all leads to **one central data structure (a module)** served instantly by
the API and updated asynchronously from several collectors. The API handler
does no state traversal: it serializes the current snapshot of that structure,
stamps included.

```
  collectors (async, own cadences)          module              API
  ─────────────────────────────────────     ──────────────      ─────────────
  live poller      (LRB, mine tip, seqs) ─┐
  census collector (full state walk)     ─┼─▶  monitor state ──▶ /api/v1/monitor ──▶ /monitor page
  snapshot hook    (stats at snapshot)   ─┤    (+ per-field
  history walker   (txstore back-walk)   ─┤     as_of stamps)
  Prometheus reader (optional)           ─┘
```

Consequences: collectors never block the handler; a slow or failed collector
leaves its fields stale (and visibly so) rather than failing the page; each
collector's cadence is configurable independently.

---

## 3. Data sources

Established by inspection of the codebase; this is the substrate spec 1 builds
on.

### 3.1 Live tier

**LRB branch aggregates** — `multistate.BranchData` (stem-projected), already
exposed via `/api/v1/get_latest_reliable_branch` and computed inside
`chain_explorer/list`: `Supply`, `TotalCoverage`, `CoverageDelta`,
`FrozenCoverage`, `SlotInflation`, `NumConfirmedTransactions`,
`NumSeqTransactions`, `NumSeq`, plus `BranchInflationBonusBase(slot)`. Cheap
(one branch record).

**Health threshold** — `global.IsHealthyBranchAt(slot, coverageDelta, supply)`
and `global.FractionHealthyBranchAt(slot)` (default 7/12). This is the exact
criterion the decentralization metric must be stated against.

**Mine chain tip** — fixed `base.MineChainID`; its LRB output gives everything
about emission state: `MineLockFromBytesWithLib` → `R` (remaining mintable) and
`B` (current difficulty); the chain constraint's `TransitionCounter` = number of
mined transactions; `lib.Constants.MineAmount` = A.

**Mining constants** — `MineAmount` (A), `MineMinPace` (P), `MineTargetPace`,
`MineFloorDifficulty` (E), `MineBaseDifficulty` (B₀), `MineMaxDifficulty` (C).
Semantics in `claude/fairlaunch.md` §8: `K_required = max(B − (M − P), E)`,
±1 retarget per transit.

**Chains** — `SugaredStateReader.IterateChainedOutputs(fun, budget)` plus the
classifier in `chain_explorer.makeRow` (sequencer / foundry / delegation / mine
/ generic, with balances, frozen amounts, transition counters). Bounded walk —
cheap while the chain count stays modest, and the sequencer/delegation subsets
are what the network section needs.

**Network APIs** — `/api/v1/get_sequencers` (sequencer chains in LRB +
`num_delegations`), `/api/v1/last_known_milestones` (per-sequencer latest
milestone + last activity), `/api/v1/sync_info` (per-sequencer synced flag +
coverage), `/api/v1/get_mainchain` (recent branches with per-branch sequencer
and coverage delta), `/api/v1/get_connectivity_matrix` (network-wide *node*
list by masked name, with a per-node `Contribution` = sequencer mass, incl.
access nodes), `/api/v1/peers_info` (this node's peers only).

**Node-count caveat.** Only the connectivity matrix sees beyond the local
node's peer list, and only as far as the connectivity map gossip reaches. Any
node count on this page is "nodes this node has evidence of", never "nodes that
exist" — the page must say so.

### 3.2 Periodic tier — the state census

The census (§4.2, §4.4, and the exact form of §5.3) needs one pass over the
whole state. Two triggers, **one implementation**:

**a. Snapshot hook — cheap, because the traversal happens anyway.** Snapshot
generation already streams the entire trie and already classifies every key by
partition: `multistate.SnapshotStats` (`NumUTXO`, `NumTx`, `NumOtherState`,
`NumChainID`, `NumAccounts`, `DurationTraverse`) is filled in `writeState`'s
single pass. The census is an **extension of that existing struct and pass**,
written out as a sidecar JSON next to the snapshot file.

Two things to fix while extending it:
- `NumAccounts` today counts *controllers-partition entries* — one per non-empty
  index-value per output — which is neither distinct controllers nor outputs.
  It needs renaming to what it is, with the real account count added alongside.
- The census does not need the controllers partition at all. Partitions stream
  in order (`LedgerState` = 0, then `Controllers`, then `ChainID`), and every
  UTXO in the ledger-state partition already carries amount, lock bytecode,
  index values and chain constraint. So the whole census — per-class counts and
  balances, per-controller totals, top-N — comes out of the UTXO pass, with
  memory bounded by the number of *distinct controllers*, not UTXOs. Cost is
  the per-output parse added to a traversal that already reads every byte.

Staleness: the snapshot module runs every `snapshot.period_in_slots` (default
176 ≈ 30 min), only when synced, and only if `snapshot.enable` — so this source
is ~30 min stale and absent on non-snapshotting nodes.

**b. Periodic census collector — the general path.** A standalone collector on
its own configurable period (~5 min as a starting point) running the same census
over the LRB state, for nodes that do not snapshot and for a fresher figure than
30 minutes. Same code as (a), different trigger.

**Account = distinct controller.** That is the definition the page uses. Note a
single output can appear under several index values (a delegation is indexed
under both master and target), so the census must attribute by *role* — the
controller is `index_values[0]` — or the same tokens get counted twice.

In the first stage the census may be limited to what one pass can produce
cheaply; anything costlier waits.

### 3.3 Historical tier

**txstore / branch back-walk** — historical series (supply history, coverage
history, branch share over time) come from walking branches back along the
canonical chain. Bounded depth, run in the background, not per request.

**Prometheus (optional)** — part of the data can come from Prometheus, which
already holds simple per-node series (e.g. TPS over the last 24 h). Caveats:
it is a per-*node* view, not ledger truth; it is an external dependency needing
a configured URL; the page must render fully without it.

**Data warehousing (future alternative)** — incrementally collect the data into
a SQL DB (e.g. SQLite) as it is produced, instead of recomputing by traversal.
Not in the first cut; noted so the module boundary does not preclude it.

---

## 4. Ledger section

Headline: **what exists and who holds it.**

**4.1 Supply and inflation** (live, exact — LRB branch aggregates)
- Total supply; genesis supply I; supply growth since genesis.
- Slot inflation at the LRB; nominal branch inflation base at this slot.
- Total coverage, coverage delta; frozen coverage (delegated capital).
- LRB slot, dashed LRB id, slots behind current slot, wall-clock age.

**4.2 Account census** (periodic) — counts and balance totals per account
class, each with `count`, `total balance`, `share of supply`:

| Class | Definition |
|---|---|
| ordinary accounts | outputs under a plain `sigLock`, no chain constraint |
| chained accounts | outputs with a chain constraint, kind `generic` |
| sequencers | chain + sequencer constraint |
| delegations | `delegateLock` |
| foundries | chain + foundry constraint |
| mine chain | the single fair-launch chain |
| other locks | `chainLock`, `tagAlongLock`, `sendWithDeadline`, dex order locks, stem |

**4.3 Capital participation** (live)
- coverage delta / supply, against the healthy fraction (7/12) — the single
  "how much of the capital is actually consensus-active" number.
- frozen (delegated) / supply; delegated capital per sequencer in §6.
- on-chain (chained) balance / supply vs. plain-lock balance / supply.

**4.4 Biggest N accounts** (chained: live; ordinary: periodic) — top N chained
and top N ordinary by balance, with share of supply and a top-k-share
concentration figure. N ≈ 10–20. Chained comes from the bounded chain walk;
ordinary needs the census, and top-N is a bounded heap inside its single pass.

---

## 5. Mining section

Headline: **how far the fair launch has got, and when control is lost.**

**5.1 Emission state** (live, exact — mine chain tip)
- Mined transactions (transition counter), minted total = counter × A.
- Remaining mintable R; mintable ceiling T = I + `MineRemainingInit`; % emitted.
- A (per transit), current difficulty B, floor E, ceiling C, min pace P,
  target pace.

**5.2 Mining process** (needs history — **TBD-p**)
- Observed pace M̄ over the last k transits, against the target pace.
- Difficulty B trajectory (is the retarget stable, or sawtoothing as in §7 of
  `claude/fairlaunch.md`?).
- Effective network hashrate estimate, from `B ≈ log₂(H·slot) + (target−P+1)`.
- Distinct miners seen recently (distinct recipients of mined outputs) and
  their share of recent transits — the mining decentralization figure.
- Time since last transit / stall indicator.

**5.3 Distribution and loss of control**

The premined amount is fixed at genesis, so the first cut simply compares it
against the totals:

```
mined   = transitionCounter × A
premine = I  (genesis supply, constant)
```

Reported:
- premine share and mined share of supply;
- **time to 1/2** and **time to 1/3** — when the premine share crosses those
  thresholds — projected from the current mining flow (A / observed M̄ per
  slot) against the inflation flow (slot inflation), with both flows shown so
  the projection is auditable;
- the same two dates under the *nominal* target pace, as the reference schedule
  (`claude/fairlaunch.md` §1: ~47 d to 50%, ~1.17 yr to full emission).

That is enough to start. Later the premine can be declared as an explicit list
of chained accounts and addresses, which makes the figure track the premine's
*inflation* as well, instead of holding I constant — at which point the census
supplies the actual balances of those accounts.

---

## 6. Network section

Headline: **who runs the network, and how few of them could stop it.**

**6.1 Participants** (live)
- Sequencers: total in the LRB state, and how many are *active* (produced a
  milestone / branch recently) vs. stalled — from `last_known_milestones` +
  recent `get_mainchain` branches.
- Total capital on sequencers, split active / inactive, absolute and as % of
  supply.
- Total delegated capital and number of delegations, absolute and as % of
  supply; per sequencer as well.
- Nodes: count from the connectivity matrix, split sequencer / access, with the
  "evidence, not census" caveat and the capture age.

**6.2 Consensus weight** (live)
- Coverage delta at the LRB and per-sequencer branch coverage delta over the
  last settled slots (the chain explorer already resolves this per sequencer
  from `BranchDataForSlot`).
- Branch share: fraction of the last k branches produced by each sequencer.
- Biggest sequencers by on-chain balance + delegated capital (up to 20).

**6.3 Decentralization metrics**
- **Sequencers-to-stop**: the smallest number of sequencers whose removal drops
  the remaining coverage delta below the healthy threshold
  (`IsHealthyBranchAt`, 7/12 of supply) — sort sequencers by consensus weight
  descending and count how many must be removed. Exact definition of "consensus
  weight" (branch coverage delta share vs. balance + delegated share) is
  **TBD-p**; the two can disagree and the page should probably show both.
- Top-1 / top-3 share of consensus weight; a concentration index.
- Latency spread from the connectivity matrix (optional, low priority — netviz
  already visualizes it).

---

## 7. What the prototype has to settle (TBD-p)

1. **Cost of the census pass.** Time and allocation of a full state traversal
   with per-output parsing, on live testnet state, and how it scales. Decides
   the periodic collector's default cadence and whether the first cut ships the
   snapshot hook, the standalone collector, or both.
2. **Census memory.** Whether per-controller aggregation (bounded by distinct
   controllers, not UTXOs) is comfortably bounded at live and projected state
   size, or needs a bounded top-N + bucketed tail instead of exact per-account
   totals.
3. **Mining history source.** Txstore predecessor walk vs.
   `/wsapi/v1/mining_tx_stream` vs. a small in-node ring of recent transits.
   Decides whether the pace/difficulty trajectory and the miner set are
   available at all, and at what depth.
4. **Consensus weight definition** for §6.3, and whether the
   sequencers-to-stop number is stable enough slot-to-slot to display.
5. **Module shape and placement.** Where the monitor module lives (a
   `core_modules` collector vs. an API-side module), how the snapshot hook
   hands its stats over, and how the API snapshot is taken without locking the
   collectors.
6. **Serving safety.** Whether the page is enabled by default or gated by
   config, given the census cost, and whether it should be restricted to access
   nodes.

---

## 8. Prototype — what was built and what it settled

Status: running. `api/monitor/` (Go module + collectors + JSON backend +
embedded page), routes `/monitor` and `/api/v1/monitor`, registered from the
API server next to the chain explorer. Validated on a standalone node: live
tier, census and mine chain history all served with real data, and four mined
transits walked back.

**A state-iteration bug, found by the census and fixed.**
`multistate.Readable.IterateUTXOIDs` walked the *controllers* partition, so
every output carrying no index values was invisible to it — including the
fair-launch mine chain, whose open `mineLock` has none. The census's
conservation check caught it: the scanned total came out short by exactly the
mine chain dust, and the mine chain was missing from the UTXO set. Since
`IterateUTXOs` builds on it, `ScanState()` (supply and chain totals) and
`ScanInactive()` inherited the same blind spot. It now walks the ledger-state
partition, where every UTXO has exactly one key, skipping transaction records
(by key length) and synthetic upgrade UTXOs. It also drops the
`Set[OutputID]` dedup that the controllers walk needed.

**Settled**
- *Census shape.* One pass over the UTXO partition yields every class total,
  distinct-controller accounts and the top-N, with no access to the controllers
  partition and memory bounded by distinct controllers. Conservation
  (scanned total == supply) is asserted in the tests and holds on a live node.
- *Account attribution.* Controller = index-values entry 0; framework locks
  (stem and the like) and the mine chain produce no account row, so they no
  longer appear as phantom zero-balance accounts. A controller lands in exactly
  one of the chained / ordinary lists.
- *Mining history source.* The txstore back-walk is sufficient — no streaming
  needed. It recovers per-transit slot, pace, difficulty B and the miner from
  the mine chain's predecessor links, and reports honestly where it stops
  (at the genesis mine output the txstore has no transaction to walk into).
  Verified against a live miner's own log: slots and the ±1 B retarget match.

**Measured, but not conclusively**
- Census cost is ~13.6 µs per UTXO on an in-memory trie (305 UTXOs),
  extrapolating to ~14 s per 1M UTXOs. That number comes from `utxodb`, not
  from BadgerDB at real state size, so it bounds nothing yet: the cadence
  decision still needs a measurement on a real node with real state.

**Still open**
- The snapshot hook is not implemented — only the standalone periodic
  collector. Extending `SnapshotStats` remains the cheap path for nodes that
  snapshot.
- Consensus weight is branch coverage delta share, and the sequencers-to-stop
  figure subtracts it from the branch's coverage delta. Competing branches each
  cover the whole slot, so this is a ranking proxy, not an identity.
- No config gate, no historical tier (Prometheus / branch back-walk), and the
  census does not survive a restart.

---

## 9. Deliberately out of spec 0

Layout, styling, chart choices, exact refresh cadences, field names and JSON
shapes, the Prometheus wiring, and the SQL-warehouse variant. These land in
spec 1, informed by the prototype.
