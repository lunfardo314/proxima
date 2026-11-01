# Library upgrade 4: endorsements of sequencer transactions only, no origin as sequencer output

> BUILT, not listed (written 2026-11-01, branch `feat/coverage-adjustment`). Fourth live library
> upgrade, no reset, for release v1.4.0 together with the switch of sequencer `v3` to the adjusted
> coverage at a later slot (`kb/coverage_adjustment_B.md`, section 11). Its slot S is decided at
> the release and then listed in `ledger.Upgrades`; the score switches at S+N. Prerequisites:
> upgrades 2 and 3 active. Mechanism: `ledger/upgrade.md`. Rules taken from
> `kb/ledger_upgrade_backlog.md`; that document says what was left there and why.

## The ledger rules

Both close a case the node already refuses in `core/` while the ledger accepts it. Pure EasyFL, no
new embedded function (`ledger/def/upgrade4.easyfl`, `ledger/def_upgrade4.go`).

**Every endorsement is a sequencer transaction.** `_validEndorsements` is replaced, same arity: as
before, and each endorsement's ID carries the sequencer flag (`sequencerFlagON`). The flag is part
of the ID, bound by the hash, so the check needs nothing beyond the transaction's own bytes and
runs in the partial-context validator (`txIntegrityValidatorPartialContext0`, which calls
`_validEndorsements` by function code, so it is unchanged): a transaction endorsing anything else is
refused before it is stored or relayed. Unrolled over the at most `constMaxNumberOfEndorsements`
(8) elements with a helper `_endorsementIsSequencerTx`, since EasyFL has no loop; a builtin would
have needed the upgrade builder extended to embedded functions. The same-slot and strictly-earlier
checks stay where they are, in Go (`scanEndorsements`), applied at every slot. Before: the node
refused a non-sequencer endorsement only at index 0, the baseline direction; at a later index it was
valid.

**The sequencer output of a sequencer transaction is not a chain origin.** `sequencer` is replaced,
same arity: as in upgrade 2, and an output with no chain predecessor (empty predecessor index) that is
the transaction's sequencer output is refused. Such a transaction has no chain to give it a
baseline direction; the node refused it (`Transaction.BaselineDirection`). Chain origins are made by
wallet transactions (`proxi node seq init`); an origin produced by a sequencer transaction next to
its sequencer output, as a new chain, stays valid. Lives in the constraint, run in full validation,
so it also holds for transactions reloaded from the transaction store, which skip the
partial-context script.

No builder produces either case: the sequencer builders endorse sequencer transactions only, extend
a chain predecessor, and `proxi` builds no sequencer transactions.

## The node rule activated with it

`enforceSeqCoverageDelta` (`core/attacher/wrapup.go`) cross-checks the coverage delta a milestone
declares against the one the node computes, and `checkConsistencyBeforeWrapUp` (`check.go`) checks
that coverage does not decrease along same-slot inputs and endorsements. Both skipped every milestone
whose baseline is of its own slot or later, to tolerate re-attachment against the forward-sync
anchor after a snapshot restore. That is also the baseline of every live non-branch milestone (the
branch of its own slot), so those were never checked. From the slot of upgrade 4
(`Library.HasUpgrade4`), a baseline of the milestone's own slot is checked; a later one, or one
substituted by the retained-history floor, is still skipped (`recomputedAgainstForeignBaseline`).
Gated by the slot so that nodes on different releases agree. Also changed, ungated:
`checkConsistencyBeforeWrapUp` now skips a substituted baseline, as `enforceSeqCoverageDelta`
already did.

## Release

- `ledger.Upgrades` lists no upgrade on the branch: upgrades 2 and 3, active since slots 45000 and
  60000, were removed (their builders stay), and upgrade 4 is added at S when S is decided. Syncing
  from genesis needs a release that still lists them, as for upgrade 1.
- The score's activation slot `sequencer/v3/factory.ScoreActivationSlot` is set to S+N with it.

## Tests

- `ledger/tests/library_upgrade4_test.go`: upgrades 1 to 4 listed at test slots; the library carries
  upgrade 4 from its slot; a sequencer transaction with an origin as sequencer output and a milestone
  endorsing a non-sequencer transaction (at index 0 and at a later index) are valid before the slot
  and refused from it; a wallet-made origin and sequencer-only endorsements stay valid.
- `tests/upgrade4_sequencers_test.go`: five sequencers across the slot, upgrades 2 to 4 listed;
  every branch from the slot holds the upgrade UTXO and branches keep coming.

Open: an in-process test of a node restored from a snapshot re-attaching milestones of the
anchor's slot, which the node rule now checks.
