# Proxima monitor page — spec 0

Status: **spec 0** (provisional, for approval). Process: spec 0 → approve →
prototype → spec 1 (the buildable spec). Spec 0 fixes *what* the page shows and
*where each number comes from*; it deliberately leaves open the questions the
prototype exists to answer (marked **TBD-p**).

Date: 2026-08-06

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
label each, drill-down only via links to the existing tools. Every number must
be either exact-from-state or explicitly marked as an estimate/projection.

---

## 2. Data sources available today

Established by inspection of the codebase; this is the substrate spec 1 will
build on.

**LRB branch aggregates** — `multistate.BranchData` (stem-projected), already
exposed via `/api/v1/get_latest_reliable_branch` and computed inside
`chain_explorer/list`: `Supply`, `TotalCoverage`, `CoverageDelta`,
`FrozenCoverage`, `SlotInflation`, `NumConfirmedTransactions`,
`NumSeqTransactions`, `NumSeq`, plus `BranchInflationBonusBase(slot)`. Cheap
(one branch record).

**Health threshold** — `global.IsHealthyBranchAt(slot, coverageDelta, supply)`
and `global.FractionHealthyBranchAt(slot)` (default 7/12). This is the exact
criterion the decentralization metric must be stated against.

**Chains** — `SugaredStateReader.IterateChainedOutputs(fun, budget)` plus the
classifier in `chain_explorer.makeRow` (sequencer / foundry / delegation / mine
/ generic, with balances, frozen amounts, transition counters). Bounded walk.

**Non-chained accounts** — `multistate.Readable.AccountsByLocks()` walks the
whole `TriePartitionControllers` partition and groups by `lock.String()`,
returning balance + output count per lock; `ScanState()` walks every UTXO. Both
are **full state scans**, today used only by offline `proxi db` commands. Cost
at live state size is unknown → **TBD-p**.

**Mine chain** — fixed `base.MineChainID`; its LRB output gives everything:
`MineLockFromBytesWithLib` → `R` (remaining mintable) and `B` (current
difficulty); the chain constraint's `TransitionCounter` = number of mined
transactions; `lib.Constants.MineAmount` = A. Mining *history* (pace and
difficulty over time, miner set) is **not** in the state — only the tip is.
Sources for history: walking predecessors through the txstore, or the
`/wsapi/v1/mining_tx_stream` feed. **TBD-p**.

**Mining constants** — `MineAmount` (A), `MineMinPace` (P),
`MineTargetPace`, `MineFloorDifficulty` (E), `MineBaseDifficulty` (B₀),
`MineMaxDifficulty` (C). Semantics in `claude/fairlaunch.md` §8:
`K_required = max(B − (M − P), E)`, ±1 retarget per transit.

**Network** — `/api/v1/get_sequencers` (sequencer chains in LRB +
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

---

## 3. Ledger section

Headline: **what exists and who holds it.**

**3.1 Supply and inflation** (exact, cheap — LRB branch aggregates)
- Total supply; genesis supply I; supply growth since genesis.
- Slot inflation at the LRB; nominal branch inflation base at this slot.
- Total coverage, coverage delta; frozen coverage (delegated capital).
- LRB slot, dashed LRB id, slots behind current slot, wall-clock age.

**3.2 Account census** (aggregate counts + balance totals per account class)

Classes, with `count`, `total balance`, `share of supply` each:

| Class | Definition |
|---|---|
| ordinary accounts | outputs under a plain `sigLock`, no chain constraint |
| chained accounts | outputs with a chain constraint, kind `generic` |
| sequencers | chain + sequencer constraint |
| delegations | `delegateLock` |
| foundries | chain + foundry constraint |
| mine chain | the single fair-launch chain |
| other locks | `chainLock`, `tagAlongLock`, `sendWithDeadline`, dex order locks, stem |

Open: whether "account" means *distinct controller* or *output*. Distinct
controllers is the meaningful number for distribution and requires grouping by
index-value, which is what the controllers partition is keyed on — a full walk.
`AccountsByLocks()` today groups by *lock source string*, which conflates
neither correctly nor cheaply. **TBD-p.**

**3.3 Capital participation**
- coverage delta / supply, against the healthy fraction (7/12) — the single
  "how much of the capital is actually consensus-active" number.
- frozen (delegated) / supply; delegated capital per sequencer in §5.
- on-chain (chained) balance / supply vs. plain-lock balance / supply.

**3.4 Biggest N accounts** — top N chained and top N ordinary, by balance, with
share of supply and a Gini/top-k-share concentration figure. N ≈ 10–20.
For chained accounts the bounded chain walk suffices; for ordinary accounts a
top-N needs the full account census (§3.2) → same **TBD-p**.

---

## 4. Mining section

Headline: **how far the fair launch has got, and when control is lost.**

**4.1 Emission state** (exact, cheap — mine chain tip)
- Mined transactions (transition counter), minted total = counter × A.
- Remaining mintable R; mintable ceiling T = I + `MineRemainingInit`; % emitted.
- A (per transit), current difficulty B, floor E, ceiling C, min pace P,
  target pace.

**4.2 Mining process** (needs history — **TBD-p**)
- Observed pace M̄ over the last k transits, against the target pace.
- Difficulty B trajectory (is the retarget stable, or sawtoothing as in §7?).
- Effective network hashrate estimate: from `B ≈ log₂(H·slot) + (target−P+1)`.
- Distinct miners seen recently (distinct recipients of mined outputs) and
  their share of recent transits — the mining decentralization figure.
- Time since last transit / stall indicator.

**4.3 Distribution and loss of control**

Model: the premine (genesis I plus the inflation accruing on it) is
"founder-controlled"; mined tokens are distributed. The ledger does not
attribute inflation to origin, so the working approximation is

```
mined   = transitionCounter × A
premine ≈ supply − mined
```

and the two thresholds are the crossings of `premine/supply` through 1/2 and
1/3. Reported:
- current premine share and mined share of supply;
- **time to 1/2** and **time to 1/3**, projected from the current mining flow
  (A / observed M̄ per slot) against the inflation flow (slot inflation), with
  the flows shown so the projection is auditable;
- the same two dates under the *nominal* target pace, as the reference schedule
  (`claude/fairlaunch.md` §1: ~47 d to 50%, ~1.17 yr to full emission).

Open: whether the approximation is good enough or whether "control" should be
measured against real holdings (the account census: what fraction of supply
sits under the genesis-derived controllers) and/or against *coverage* rather
than supply — coverage is what actually decides consensus. **TBD-p.**

---

## 5. Network section

Headline: **who runs the network, and how few of them could stop it.**

**5.1 Participants**
- Sequencers: total in the LRB state, and how many are *active* (produced a
  milestone / branch recently) vs. stalled — from `last_known_milestones` +
  recent `get_mainchain` branches.
- Nodes: count from the connectivity matrix, split sequencer / access, with the
  "evidence, not census" caveat and the capture age.
- Delegations per sequencer and delegated capital per sequencer.

**5.2 Consensus weight**
- Coverage delta at the LRB and per-sequencer branch coverage delta over the
  last settled slots (the chain explorer already resolves this per sequencer
  from `BranchDataForSlot`).
- Branch share: fraction of the last k branches produced by each sequencer.
- Biggest sequencers by on-chain balance + delegated capital.

**5.3 Decentralization metrics**
- **Sequencers-to-stop**: the smallest number of sequencers whose removal drops
  the remaining coverage delta below the healthy threshold
  (`IsHealthyBranchAt`, 7/12 of supply) — i.e. sort sequencers by consensus
  weight descending and count how many must be removed. Exact definition of
  "consensus weight" (branch coverage delta share vs. balance+delegated share)
  is **TBD-p**; the two can disagree and the page should probably show both.
- Top-1 / top-3 share of consensus weight; a concentration index.
- Geographic / latency spread from the connectivity matrix (optional,
  low priority — netviz already visualizes it).

---

## 6. What the prototype has to settle (TBD-p)

1. **Cost of the account census.** Time and allocation of a full
   controllers-partition walk on live testnet state, and how it scales. Decides
   whether §3.2/§3.4/§4.3-exact are per-request, periodically recomputed, or
   bounded/sampled. A per-request full scan on a serving node is very likely
   unacceptable — the prototype measures it rather than guessing.
2. **What "account" is.** Distinct controller vs. output vs. lock-string, and
   whether the controllers partition can be grouped by index-value in one pass.
3. **Mining history source.** Txstore predecessor walk vs. `mining_tx_stream`
   vs. a small in-node ring of recent transits. Decides whether pace/difficulty
   trajectory and the miner set are available at all, and at what depth.
4. **Loss-of-control model.** Whether the `supply − mined` approximation is
   defensible, or whether it must be grounded in the account census and/or
   restated against coverage.
5. **Consensus weight definition** for §5.3, and whether the
   sequencers-to-stop number is stable enough slot-to-slot to display.
6. **One endpoint or several.** Cheap aggregates refresh every few seconds;
   an expensive census cannot. Likely split: a cheap `summary` and a slow
   `census` with its own cadence and an explicit "as of slot N" stamp.
7. **Serving safety.** Whether the page is enabled by default or gated by
   config, given the scan cost, and whether it should be restricted to access
   nodes.

---

## 7. Deliberately out of spec 0

Layout, styling, chart choices, refresh cadence, exact field names and JSON
shapes, historical time series (the page is a *now* view; any trend line needs
a data source that does not exist yet). These land in spec 1, informed by the
prototype.
