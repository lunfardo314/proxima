# Sequencer rating — choosing a delegation or tag-along target

> **LIVE** — approved and **built 2026-09-20**, verified against the testnet
> with `proxi node seq_rating`. Replaces the four wallet-side random target
> pickers (§6) with one rank-based rating computed from the sequencer list the
> wallet already fetches. Wallet-side only; a node endpoint is deferred (§8).

## 1. Problem

A wallet that picks its target by price alone sends everything to the cheapest
sequencer. Today the wallet does not even agree with itself on what "price"
is: `proxi node consolidate` draws a delegation target in proportion to the
share the sequencer leaves, `proxi node delegate` draws inversely to the
sequencer's balance plus frozen coverage, `proxi node mine` draws uniformly
among those tolerating its cut, and the random tag-along target is uniform
among the active sequencers. Four different activity windows (1, 2, 3 or 6
slots) on top.

One rating, built from several criteria, softens the price signal instead of
removing it, and gives every picker the same notion of a good target.

## 2. Data

One call, `/api/v1/get_sequencers` (`client.GetAllSequencerOutputs`), returns
the sequencer output of every sequencer chain in the LRB state, plus the LRB
transaction ID. Everything the rating needs is on that output:

| Fact | Where it is read |
|------|------------------|
| balance | amounts index 0, `Output.TokenBalance()` |
| frozen coverage | amounts index 2, `Output.FrozenCoverage(0)`: the cumulative total frozen by the delegations on this sequencer |
| share left to delegators | 1000 − `SequencerData.InflationProfitMarginPromille()`, in promille; 1000 when the output carries no sequencer data |
| minimum tag-along fee | `SequencerData.MinimumFee()` |
| last settled milestone | the slot of the output ID |

Nothing else is fetched. In particular the tippool's "last known sequencer
data" (`/last_known_milestones`) is no longer consulted by the pickers: it
reports milestones that may never settle, and a sequencer whose milestones
do not settle is not serving anyone.

## 3. Eligibility

A sequencer is a candidate when it is **active**: its sequencer output in the
LRB state is at most `activeSequencerSlots` = **5** slots older than the LRB.
Measured against the LRB slot, not the wallet clock, so the answer does not
depend on how far the node's LRB trails real time. One constant, shared by
every picker.

For a **delegation** target one more gate applies: it leaves delegators
something (share > 0). There is **no price floor**: the consolidating wallet
is a price taker (`kb/consolidate.md`), the delegation requires exactly what
the drawn target leaves, and the share is one criterion among three, so a
dear sequencer is drawn less often, not never. `delegate.minimum_cut` is not
read by any picker.

The one exception is `proxi node delegate`, whose `--cut` flag (default
`delegate.minimum_cut`, 900) fixes the cut the delegation is built with: a
target leaving less would never freeze it, so its picker drops those first.
That is a construction constraint of that command, not a policy.

A pinned target (a configured sequencer ID) bypasses the rating but not the
gates, exactly as today.

## 4. Rating

Given N candidates and M criteria, each criterion sorts the candidates and the
**rank** C(i, j) of candidate i by criterion j is its 1-based position in that
order, best first. Candidates equal under a criterion share the rank of the
first of them (competition ranking: 1, 2, 2, 4). The **rating** is the rank
sum

    R(i) = Σ_j W_j · C(i, j)

with the **price criterion at weight 2** and the others at 1. Weights are
constants in the code, not a config key. Equal weights were the first cut;
on the testnet, nine sequencers of which five hold genesis-sized balances,
the balance and the frozen-to-balance ratio agreed on every candidate and
gave the five keeping 40% about 78% of the draws against 22% for the four
leaving everything. Doubling the price brings it back to a vote the other
two cannot outnumber on their own. Smaller R is better.

Rank sums are scale free, so a balance in the billions and a share in
promille mix without units, and one absurd value moves its owner by one rank,
not by orders of magnitude.

### 4.1 Delegation criteria

| j | Criterion | Order | W | Why |
|---|-----------|-------|---|-----|
| 1 | share left to delegators | descending | 2 | the price |
| 2 | balance | descending | 1 | skin in the game; the only stake-weighted component, and the only thing that costs a sequencer splitting into several identities |
| 3 | DE ratio = frozen coverage / balance | **ascending** | 1 | spreading: a high ratio means the sequencer is already crowded, carries more delegation transitions per slot, and has less of its own capital behind each delegated token. A delegator earns the same per token anywhere at the same cut, so the crowded one is not worth more |

The ratio is compared as a fraction, `frozen_i · balance_j` against
`frozen_j · balance_i` in 128-bit arithmetic, so no float and no division. A
zero balance ranks last under criteria 2 and 3.

Descending balance with ascending DE also keeps the two from being one
criterion counted twice.

### 4.2 Tag-along criteria

| j | Criterion | Order | W | Why |
|---|-----------|-------|---|-----|
| 1 | minimum fee | ascending | 2 | the price the wallet pays on every transaction |
| 2 | balance | descending | 1 | as above |

No share, no DE: neither means anything for a fee output.

## 5. The draw

Candidates are sorted by R ascending, ties broken by chain ID so the order is
the same on every wallet. The candidate in position p (1-based) is drawn with
weight

    w(p) = N − p + 1

and probability w(p) / Σ w. Linear in position, positive everywhere, smallest
at the bottom.

Worked out: with 5 candidates the shares are 5/15, 4/15, 3/15, 2/15, 1/15.
For large N the quarters of the list take about 44%, 31%, 19% and 6%. That
is the shape the quartile idea (1/2, 1/4, 1/8, 1/8) was after, without its
two defects: with few sequencers a quartile is one sequencer taking half the
flow, and a flat bottom share pays every cheap extra identity the same
regardless of where it lands.

The draw is re-made on every action, as today: nothing is cached across
ticks, and a top-up re-rolls its target (`kb/consolidate.md`, tidying).

## 6. Call sites

| Picker today | Rule today | After |
|--------------|-----------|-------|
| `proxi/node_cmd/consolidate/delegate.go`, `selectDelegationTarget` | tippool activity within 3 slots, weight = share left | delegation rating |
| `proxi/node_cmd/consolidate/consolidate.go`, tag-along `random` | tippool activity within 3 slots, uniform | tag-along rating |
| `proxi/node_cmd/delegate/amount.go`, `chooseRandomSequencerForDelegation` (also used by `delegate/chain.go`) | LRB output within 6 slots, weight = max coverage − coverage | delegation rating |
| `proxi/glb/profile.go`, `randomActiveSequencerID` (tag-along `random` of every other command) | tippool activity within 1 slot, uniform | tag-along rating |

`proxi node mine` (`chooseRandomAliveSequencer`, 2 slots, uniform among those
tolerating the miner's cut) is **left alone**: its treasury loop is retired
when `proxi node consolidate` takes over (`kb/consolidate.md` §5), and
rewriting a picker that is about to be deleted is waste.

`consolidate`'s `activeSequencers()` and the `activeSequencerSlots`
constants in `glb` and `consolidate` collapse into the one in §3, kept
in the shared code (§7). The consolidator's stale-delegation check ("its
target is not active") uses the same eligibility, so a delegation is
re-targeted under the same rule that chose the target.

## 7. Where the code lives

The rating and the draw are pure functions in `ledger/txbuildercore`, beside
the other wallet helpers: the input is a slice of candidates (chain ID,
output slot, balance, frozen coverage, share left, minimum fee), the outputs
are the rated list in draw order with each candidate's ranks and R, and the
drawn one given a `func(n int) int`. No client, no singleton, no I/O, so the
ranking, the tie rules, the zero-balance case and the draw shape are unit
tested directly.

`proxi/glb` gets the one fetch that turns `/get_sequencers` into that
candidate slice, filtered by §3, so the four call sites are each a fetch plus
a call. `consolidate` keeps its `retry` around the fetch.

A read-only `proxi node seq_rating` prints the delegation table, or with
`--tag_along` the tag-along one: the candidates in draw order, best on top,
with their ranks, R and draw probability, followed by the active sequencers
leaving nothing and the inactive ones. It is the display half of the same
function and lets the rating be checked on the testnet without delegating
anything.

## 8. Not in this step

- **A node endpoint.** Useful for the chain explorer (a sequencer table
  sorted by rating), and cheap once §7 exists. When it comes it serves the
  facts and the ranks, and the wallet keeps computing its own, so nobody
  has to trust the node's policy. Not needed by any wallet path now.
- **Uptime.** An "average uptime" criterion needs history the LRB state does
  not carry (the sequencer output only says when the last milestone
  settled). A node that keeps a per-sequencer activity series can add it as
  a fourth criterion later, and the rank-sum form absorbs it without
  touching the others.
- **Configurable weights.** See §4: constants in the code.

## 9. Documentation

- `kb/consolidate.md` §2.4b: the "price taker" paragraph becomes a pointer
  here (the draw is no longer proportional to the share left, and
  `delegate.minimum_cut` is read again).
- Docs site `participate/delegate` and the consolidate page: one paragraph
  each on how a `random` target is chosen, in plain words (the site is a
  separate repo and a separate pass).
- `CLAUDE.md` kb index: one row.
