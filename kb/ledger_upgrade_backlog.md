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
