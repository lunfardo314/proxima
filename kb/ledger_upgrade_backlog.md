# Ledger upgrade backlog

> **LIVE** — validity rules waiting for the next library upgrade. Nothing here is
> built. Started 2026-10-26, when the current release was kept Go-only. An item
> leaves this list when it is specified in its own `kb/library_upgradeN.md`.

A rule belongs here when the node already protects itself in `core/`, but the
ledger still accepts the transaction. Such a rule cannot be added in Go under
`ledger/`, not even in the stage-1/2 scans of `ledger/transaction`: nodes on
different releases would then disagree on validity. It has to be a library rule
that activates at a slot (`ledger/upgrade.md`).

## Endorsement validity in the partial context

**Rule.** Every endorsement is a sequencer transaction.

**Today.** The ledger checks only the number of endorsements, that no endorsement
repeats, and that only a sequencer transaction endorses (`_validEndorsements` in
`ledger/def/tx_integrity_validator.easyfl`). `scanEndorsements` in
`ledger/transaction/parse.go` adds, in Go: same slot as the endorser, strictly
earlier. Nothing requires an endorsed transaction to be a sequencer transaction,
although the cross-slot error message of `sequencer.easyfl` assumes it. The
attacher refuses a milestone whose first endorsement, its baseline direction, is
not a sequencer transaction (`6ce8e080`). A non-sequencer endorsement at a later
index is valid on the ledger.

**Shape.** A new embedded builtin, a pure predicate, that scans the endorsement
tuple once and validates every element: the sequencer flag, and, so the rules sit
in one place, the same-slot and strictly-earlier checks now done by
`scanEndorsements`. Called from `txIntegrityValidatorPartialContext` in place of
the per-element part of `_validEndorsements`, so a bad endorsement is refused
before the transaction is stored or relayed. A builtin, because EasyFL has no
loop: the check would have to be unrolled over `constMaxNumberOfEndorsements`.
Adding one in an upgrade: `ledger/upgrade.md`, step 3.

## Candidates found on the same path

Both need only the transaction's own bytes, so they fit the partial context too.
The attacher rejects both since `2b2bf94f`.

- **Cross-slot chain predecessor needs an endorsement or an explicit baseline.**
  `_crossSlotPredecessorCase` in `sequencer.easyfl` enforces it, but only in full
  validation, after the attacher has needed it.
- **A sequencer transaction's sequencer output is not a chain origin.** The ledger
  accepts a wallet transaction that names a chain origin as its sequencer output;
  chain origins are meant to be wallet transactions only.

## No own-branch extension right after the branch

Unlike the items above, not a rule the node already enforces in `core/`: a fairness rule, from the
2026-10-30 live measurement (`.internal/capital-bias.md`, gitignored, with the scripts in
`.internal/seq_analysis/`).

**Problem.** A slot's branches tie on coverage apart from the branch inflation bonus, which should
decide. The released sequencer (`sequencer/v1`) extends its own branch with its first milestone at
the post-branch consolidation tick; that milestone carries the issuer's whole coverage (balance and
frozen delegations), and a sequencer moving after it joins the heaviest lineage. Live, the bonus
decided 59% of tied slots; the founder with the largest capital won 57 slots against the bonus, all
other issuers 8, in capital order, with a capital lead of 1-3%. `sequencer/v2` does not back its own
branch, and moves at that tick: if every sequencer does, the bonus decides and a self-backing issuer
wins only with more capital than all those who followed the bonus.

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

**Shape.** An extension of the sequencer constraint in `sequencer.easyfl`, next to the post-branch
consolidation rule of upgrade 2 (`kb/library_upgrade2.md`): the chain predecessor's transaction ID
is in the input, so whether it is a branch (a flag in the ID) and its slot need no builtin. `k` a library constant, a few ticks; the attacher's timestamp floor and the
sequencer's first-move timing follow it.
