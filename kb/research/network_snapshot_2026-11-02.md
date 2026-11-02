# Network snapshot, 2026-11-02

> RESEARCH, open. A one-day measurement of the take 1 network: slots 118407 to 126844
> (the 24 hours before about 07:15 UTC on 2026-11-02). Nothing here was built from it. Read for
> the baseline numbers of activity, branch competition, stake and network shape before arguing
> about any of them; repeat the measurement rather than quoting it once it is old.

## Sources

| Source | What it gave |
|--------|--------------|
| `get_mainchain?max=25000` on a public node | The winning branch of every slot: sequencer, `num_seq`, confirmed and sequencer transaction counts, supply, coverage |
| `BRANCH COMMIT` lines in one access node's logs (oloc2-acc) | Every candidate branch of a slot with its sequencer and coverage. One node's view, so a few candidates may be missing |
| `proxi node seq_rating` | Active and inactive sequencers, balances, frozen coverage, transit counts |
| `get_connectivity_matrix` (the data of `/netviz`), read on the box: nginx answers 403 on the public port | Round-trip times between 194 nodes and the consensus mass of each sequencer node. Node names are masked |
| `chain_explorer/list?max=2000` | All 755 chains: delegations, sequencers, the mine chain |
| `get_holders` | Balance per account over the whole UTXO set |
| Prometheus on the 10 own nodes | Lag, throughput and peering trends |

## Activity

- A winning branch in every one of the 8,438 slots; no empty slot.
- A winning branch confirms about 130 transactions (median 135, 5th percentile 64, maximum 265):
  about 128 sequencer transactions and about 1.3 others. Non-sequencer traffic is about 0.13
  transactions per second; nearly all activity is sequencers consolidating each other.
- Flat over the day, rising slightly in the last 8 hours: 126 to 135 transactions per slot, median
  sequencers per branch 31 to 34.
- Supply 70.35M to 71.09M PROX, plus about 744,000 PROX in 24 hours (chain and branch inflation plus
  mining). Mining about 29,000 PROX an hour at difficulty 37 bits.
- Own nodes: about 24 transactions per second validated and 10.5 confirmed, the LRB at most 4 slots
  behind (average 1.2).

## Branch competition

- Coverage delta of the winning branch is 97.4% of supply at the median, 94% at the 5th percentile.
- All but 4 slots had several candidate branches, typically 5 to 8, at most 12.
- In 92% of slots the top candidates tie on coverage and the random branch bonus decides, as intended.
- 31 sequencers won slots. The most frequent winner, Maus1, won 6.8%.
- 81 winning branches folded in fewer than 15 sequencers (the lowest 6), most cut early by SUPRNV (36),
  Maus1 (12) and fear_a (12): the early-cut pattern of slot 120519, which the stage 2 score is meant
  to remove (`../coverage_adjustment.md`).

Branches produced against branches won:

| Sequencer | Branches produced | Won |
|-----------|-------------------|-----|
| Ryzen1 | 8,318 (nearly every slot) | 1.7% |
| RAMEN | 6,813 | 3.9% |
| Kronos | 4,381 | 4.1% |
| srv1 | 4,329 | 3.1% |
| Picard | 2,331 | 2.5% |
| VULCAN, SATURN, MERCUR, SIRIA, MAYA, JANUS | 550 to 720 each | 57 to 61% |
| founder sequencers (boot, hloc0, oseq1, oloc1, oloc2) | 470 to 640 each | 40 to 49% |

Flooding branches buys almost no wins, and every node pays an attach for each extra branch.

## Sequencers

- 41 active. Inactive: 01pool (last milestone at slot 59999) and NPROX (slot 46700). zzz is active
  but leaves delegators nothing.
- Probable single-operator groups: `p`, `p2` to `p5` (balances of 94,000 to 524,000 PROX, low transit
  rates); ORTLER, DENALI, ELBRUS, TAUNUS (about 250 PROX each, nothing frozen yet); MIRA and NAOS.
- All five founder sequencers run sequencer `v2` (v1.3.9).

## Network shape

- 194 nodes known to one access node, 38 of them sequencer nodes, every pair connected.
- Round-trip time: median 40 ms, 90th percentile about 250 ms, longest pair 3.9 s. Between sequencers:
  median 31 ms, worst 544 ms.
- The 5 founder sequencer nodes hold 17.6 to 18.2% of the consensus mass each, about 89% together;
  every other node under 2%.
- Most of the mass sits in three groups of nodes within 15 ms of each other. About five sequencers are
  250 ms or more from it, stake-weighted; they see the capital last.

## Delegations and holders

- 755 chains: 711 delegations, 43 sequencers, the mine chain.
- 96% of supply is in delegations (68.55M PROX); 680 of 711 are frozen; 396 moved in the 24 hours.
- Each founder sequencer carries 12.2M to 12.6M PROX across 28 to 44 delegations, mostly the
  founders' own. Largest outside targets: srv1 1.18M, Kronos 0.88M, VULCAN 0.66M, SATURN 0.64M,
  MORA 0.48M.
- 719 accounts over 3,680 outputs. Top 5 hold 84.6% of supply (about 12.02M each, the genesis
  stake), top 10 88%, top 50 97.4%. 264 accounts hold at least 1,000 PROX, 358 under 100. Idle
  outside delegations and chains: 0.7% of supply.

## Own nodes

- All 10 up and synced, no crash files, no error lines in recent logs. Access nodes on v1.3.8p2
  (hboot-acc on v1.3.9).
- Gossip send path: oseq1-acc dropped about 39 outgoing messages a second over 6 hours (8 to 15 on
  previous days), its send queue at the 64 limit; hloc0-acc and oloc2-acc about 2. Outside peers
  churn about every 10 minutes, some dropped for an outdated transaction version (nodes that never
  upgraded past an earlier library upgrade). Public access nodes refuse 85 to 145 incoming streams a
  second. Consensus was not affected.

## Patterns worth noting

1. Winning is spread over 31 sequencers by the bonus; the stake is not: 89% with the founders.
2. Branch flooding by a few sequencers buys almost nothing and costs every node attach work.
3. Early-cut branches with few sequencers still win a few times an hour.
4. User traffic is tiny against consensus traffic.
