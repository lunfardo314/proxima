# Ledger upgrade backlog

> **LIVE** — validity rules waiting for a library upgrade. Started 2026-10-26, when the
> release was kept Go-only. An item leaves this list when it is specified in its own
> `kb/library_upgradeN.md`. Revised 2026-11-01: two items went into upgrade 4, one was
> dropped, the fairness rule stays.

A rule belongs here when the node already protects itself in `core/`, but the
ledger still accepts the transaction. Such a rule cannot be added in Go under
`ledger/`, not even in the stage-1/2 scans of `ledger/transaction`: nodes on
different releases would then disagree on validity. It has to be a library rule
that activates at a slot (`ledger/upgrade.md`).

## Taken by upgrade 4

Endorsement validity (every endorsement a sequencer transaction) and the chain-origin ban (a sequencer
output is not a chain origin) are specified and built in `kb/library_upgrade4.md`, in EasyFL, without
a builtin: an unroll over the eight endorsements needs none.

## Dropped

- **Cross-slot chain predecessor needs an endorsement or an explicit baseline.** Already a ledger rule
  (`_crossSlotPredecessorCase` in `sequencer.easyfl`, full validation), and the node refuses such a
  transaction early (`Transaction.BaselineDirection`). Moving the check to the partial context changes
  no validity, and transactions reloaded from the transaction store skip the partial-context script.

## No own-branch extension right after the branch

Unlike the items above, not a rule the node already enforces in `core/`: a fairness rule, from the
2026-10-30 live measurement (`.internal/capital-bias.md`, gitignored, with the scripts in
`.internal/seq_analysis/`).

**Problem.** A slot's branches tie on coverage apart from the branch inflation bonus, which should
decide. The released sequencer (`sequencer/v1`) extends its own branch with its first milestone at
the post-branch consolidation tick; that milestone carries the issuer's whole coverage (balance and
frozen delegations), and a sequencer moving after it joins the heaviest lineage. Live, the bonus
decided 59% of tied slots; the founder with the largest capital won 57 slots against the bonus, all
other issuers 8, in capital order, with a capital lead of 1-3%. `sequencer/v2` moves at that tick
and chooses its first move by coverage among every extend-endorse pair, staying on its own branch
included (`factory/first_move.go`): it backs its own branch only when that has the most coverage. If
every sequencer does, the bonus decides and a self-backing issuer wins only with more capital than all
those who followed the bonus.

**Rule.** A sequencer transaction other than a branch, timestamped before tick
`constPostBranchConsolidationTicks + k` of its slot, may not have as chain predecessor a branch of
the same slot (its own branch: a chain predecessor is always the own chain). Other sequencers'
milestones may extend or endorse any branch from the post-branch consolidation tick as now, so the
choice stays pure coverage maximisation; the coverage definition is not touched. In the window the
issuer can still build on another lineage from its older chain output (the re-anchor `v2` does).

**Value, and its limit.** It stops a single chain from backing its own branch before the others
choose, whatever software it runs. It does not stop a holder running a second sequencer chain that
extends the branch in the window: the ledger cannot tell owners apart, so the split capital backs the
branch just the same. It raises the cost of self-backing, it does not remove it. The main remedy is
every sequencer moving at the post-branch consolidation tick and choosing by coverage (`v2` as the
default); this rule is a guard behind it. Decide after measuring a network of mostly `v2`.

**Measured, not taken into upgrade 4 (2026-11-01).** With `v2` on every founder the bonus decided
95-97% of tied slots and the largest issuer had 9% of wins: the capital bias is gone without the rule.
Its cost: every sequencer version (`v1`, `v2`, `v3`) extends its own branch at the post-branch
consolidation tick, so all three, the attacher's timestamp floor and the slot timing would change, and
a lone sequencer could not move before tick 9+k on its own lineage.

**Shape.** An extension of the sequencer constraint in `sequencer.easyfl`, next to the post-branch
consolidation rule of upgrade 2 (`kb/library_upgrade2.md`): the chain predecessor's transaction ID
is in the input, so whether it is a branch (a flag in the ID) and its slot need no builtin. `k` a library constant, a few ticks; the attacher's timestamp floor and the
sequencer's first-move timing follow it.
