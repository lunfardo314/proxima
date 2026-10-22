# Library upgrade 2: post-branch consolidation ticks

> SPEC, not built (2026-10-21, revised 2026-10-22). The second live library upgrade, no reset.
> Restores a ledger rule removed in v0.8.0: no sequencer transaction other than a branch before
> tick 9 of its slot, so that every sequencer meets the slot's branches bare and the random
> branch inflation bonus decides between them. Why: `kb/research/branch-bias.md`. Mechanism and
> the first upgrade: `ledger/upgrade.md`, `kb/library_upgrade1.md`. Prerequisites: v1.1.1 on
> every node, and the two fixes under Prerequisites released before the upgrade is listed.

## The problem

Competing branches of a slot tie on coverage in most slots (50% to 87% depending on the period),
because frozen delegations are nearly the whole supply and count in full on every branch. The
whitepaper expects the branch inflation bonus to decide such ties: each sequencer maximises the
coverage of its next transaction, and with both branches bare in front of it the amounts on both
sides cancel and the bonus is the difference.

That only holds if nobody has built on a branch yet. Today a sequencer may build on any branch at
tick 1: endorsing needs a 1-tick gap, and extending its own branch the sequencer pace, 3 ticks.
The first branch to arrive gathers milestones within a few ticks; from then on maximising
coverage follows it, and the bonus never decides. VULCAN cuts its branch mid-slot, so its branch
commits about 140 ms into the slot against about 450 ms for a branch cut at the end. It won 83%
of slots 26,000 to 28,999 and 79% of slots 31,500 to 33,600, 3.9 times its fair share of ties.
By 2026-10-22 eight outside sequencers had copied the early first milestone (tick 1 to 3); their
branches commit about 280 ms in and lose to VULCAN's, and the slots carry 3 to 8 competing
branches instead of 2 to 3. An arms race only the fastest wins, and every node attaches every
branch.

A policy change on the reference sequencer alone cannot fix it: a sequencer that waits while
another builds early only finds the early branch further ahead in coverage. The rule has to bind
everyone, so it belongs in the ledger.

## Why the rule was removed, and why that was wrong

Until v0.8.0 the ledger had `constPostBranchConsolidationTicks` (12) and
`checkPostBranchConsolidationTicks` in `sequencer.easyfl`. It was removed as redundant
(`kb/archive/shipped/seq_v080_pbc_removal.md`): the sequencer pace on a branch output was taken to
push the first extender to tick 12 anyway. That holds for a sequencer extending its own branch
only. A follower extends its own previous-slot milestone and endorses another's branch, at tick 1.

## The rule

A transaction producing a sequencer output, other than a branch, must be stamped at tick
`constPostBranchConsolidationTicks` = 9 or later. A sequencer transaction at tick 0 is a branch
by definition (`_predIsBranch` in `sequencer.easyfl`, `TransactionID.IsBranchTransaction`), so
the rule is: a branch, or tick >= 9. It covers chain origins carrying the sequencer constraint
and bootstrap transactions as well; neither may be at tick 0 already.

With the pre-branch zone (ticks 103 to 127, single-input milestones only) the slot becomes:

```
tick 0        9                                   102       127
branch | no sequencer tx |  active zone                 | pre-branch zone |
```

**Why 9.** The 128 ticks of a slot are scarce: every tick in the window is a tick in which no
sequencer cooperates. Branch commits at an access node, slots 31,500 to 33,600:

| Window | All branches | boot (cut at the end of the slot) | Early-cut branches |
|---|---|---|---|
| 6 ticks (480 ms) | 88.7% | 90.9% | 92.6% to 99.9% |
| 9 ticks (720 ms) | 93.8% | 99.8% | 99.1% to 100% |
| 12 ticks (960 ms) | 94.3% | 100% | 99.1% to 100% |

Three more ticks buy half a percentage point. Almost everything outside 9 ticks is one cluster
near 1.35 s from the founder's non-bootstrap sequencers, which issue about 20% of their branches
at tick 12 of the new slot instead of tick 127 of the old one (see Prerequisites); a larger window
would not cover it either. The constant can be raised by a later upgrade if the measurement after
activation asks for it.

Not affected: the mine settlement window (ticks 86 to 102) and the transit stamping tick (83) are
at the other end of the slot; non-sequencer transactions.

## Prerequisites (normal releases, before the upgrade is listed)

1. **Stored libraries checked against the binary.** `storeUpgrade` (`ledger/multistate/genesis.go`)
   leaves a slot alone as soon as a library is stored for it, and snapshot restore writes the
   snapshot's libraries as they are. A node therefore runs whatever library its database holds:
   one built by a test or release-candidate binary, restored from such a node's snapshot, or kept
   for a slot that was later moved. The library hash goes into the upgrade UTXO and so into the
   branch root, and a transaction's version is the library's position in the stored list: either
   difference splits the node from the network at the slot, as at slot 29000. Fix: at start the
   stored slots above 0 must be exactly those of `ledger.Upgrades`, and each stored library must
   equal what the binary builds on the one before it; otherwise the node refuses to start and
   says to restore a snapshot from an up-to-date node.
2. **No late branches from the reference sequencer.** hloc0, oseq1, oloc1 and oloc2 submitted 118
   of their last 600 branches at tick 12 of the new slot instead of tick 127 of the old one. With
   the rule those branches always lose. Find why and fix it.

Also, not blocking upgrade 2: builder code reading `ledger.L(base.MaxSlot)` where the slot being
built for is meant (`DelegationOutput.InflationOneSlot` in `ledger/lock_delegate_util.go`, the
sequencer pace in `sequencer/config.go`, `ledger/sdeposit.go`). A stored, not yet active library
is the newest from node start, so these read the next upgrade's constants early. Validation is not
affected, only a node's own transactions, and upgrade 2 changes none of those constants.

## EasyFL (`ledger/def/upgrade2.easyfl`)

Applied on top of the library of upgrade 1, in the same way:

- **New** `constPostBranchConsolidationTicks : u64/9`.
- **New** `checkPostBranchConsolidationTicks`, the mirror of `checkPreBranchConsolidationTicks`:
  `or(isBranchTransaction, require(lessOrEqualThan(uint8Bytes(constPostBranchConsolidationTicks),
  uint8Bytes(txTick)), !!!sequencer_transaction_violates_post-branch_consolidation_ticks_constraint))`,
  with `isBranchTransaction` as `sequencer.easyfl` already uses it. Both sides as `uint8Bytes`,
  as in the pre-branch check: comparing values of different widths does not do what it reads.
- **Replaced** `sequencer` (same arity, same function code, so existing outputs parse unchanged):
  the genesis body with `checkPostBranchConsolidationTicks` next to
  `checkPreBranchConsolidationTicks` in the produced-output arm. Alternative with a smaller
  diff: replace `checkPreBranchConsolidationTicks` with a body checking both zones; rejected,
  the name would no longer say what it checks.

The rule runs only where `checkPreBranchConsolidationTicks` runs: for the produced sequencer
output, origins included, not for consumed ones, so sequencer outputs produced before the slot
and consumed after it are unaffected.

## Go

- **Upgrade definition.** `ledger/def_upgrade2.go`: `BuildUpgrade2Library`, the same steps as
  `BuildUpgrade1Library` on its own source; factor the shared body into one function taking the
  source and a name, now that there are two. Listed in `ledger.Upgrades` with its slot and the
  description "sequencer transactions only from tick 9".
- **Constants.** `ledger/constants.go` loads `constPostBranchConsolidationTicks` as optional: 0
  when the library lacks it (every library before upgrade 2), and only then. Every other constant
  is loaded with an assertion; any error other than "not found" keeps failing, so the floor never
  depends on why a lookup failed. New field `PostBranchConsolidationTicks` in
  `txbuildercore.Constants`, JSON `post_branch_consolidation_ticks`, so wallets read it per slot
  like `upgrade_index`.
- **Attacher.** `IncrementalAttacher.TimestampLowerBound` for a non-branch target: also at least
  `T(targetSlot, PostBranchConsolidationTicks)` of `ledger.L(targetSlot)`. Per-slot library, so
  the floor is 0 before the upgrade slot and 9 from it, with no condition in the code.
- **Reference sequencer.** Gets the floor through the attacher's lower bound: the proposer factory
  takes `max(target, lowerBound)`. Check that the first pulse of a slot, the bootstrap path and the
  mine settlement pulse all go through it; restore a helper like the removed
  `IsPostBranchConsolidationTimestamp` only if one of them does not. Timing: the first milestone
  of a slot at tick 9 exactly, not later, so that every sequencer meets the branches bare
  (see Open points).
- **proxi.** `seq_cmd/init_seq.go` stamps a sequencer chain origin at "now", moved past tick 0.
  From the upgrade it must also be at least the floor of its slot, read with
  `glb.GetLedgerConstantsAt(slot)`. No other `proxi` command builds a sequencer output (checked:
  `balance.go` and `chain.go` only display). The wasm wallet takes timestamps from its caller; its
  README says the floor applies.
- **Logs for the measurement.** Add the branch inflation bonus to the `BRANCH COMMIT` line: with
  it, whether the winner of a tie is the branch with the largest bonus can be read straight from
  one node's log.
- **Version.** v1.2.0: a ledger rule change, minor version.

## Tests

- `ledger/tests/library_upgrade2_test.go`, modelled on `library_upgrade1_test.go` (upgrade listed
  at a low slot of the test ledger): before the slot a sequencer milestone at tick 1 and an origin
  at tick 5 are valid; from the slot both are refused with the new error, at tick 8 refused, at
  tick 9 valid; a branch at tick 0 is valid on both sides.
- `ledger/multistate`: the upgrade builds on upgrade 1's library and stores with the checked
  writer, as `TestStoreUpgrade_*`; the stored-library check refuses a stray slot, a library whose
  hash differs from the binary's, and accepts a store that matches.
- Attacher: the lower bound of a non-branch target is 9 from the slot, the pace before it.
- **Five sequencers across the upgrade slot** (`tests/`, in-process, under `-race` since
  `core/attacher` and `sequencer` change; one test at a time): all five produce branches, the
  upgrade is listed a few slots ahead. Assert that every branch committed at or after the slot
  holds the upgrade UTXO in its state, that no non-branch sequencer transaction after the slot is
  before tick 9, and that milestones and branches keep coming. The in-process sequencers share one
  node, so this cannot show two nodes disagreeing, but it checks the invariant the slot-29000 bug
  broke: run on the code before `7c5b9e09` it must fail.
- `TestUpgradeUTXOInjectedIntoEveryBranchOfTheSlot` stays.

## Rehearsal

The rehearsal of upgrade 1 ran one sequencer and never produced competing branches at the upgrade
slot, which is where the injection bug was. This one must:

- a local network of several nodes with at least three sequencers producing branches, one of them
  cutting its branch early as VULCAN does, an access node and a miner; upgrade 2 listed a few
  minutes ahead (scratch patch, never committed);
- at the upgrade slot every node logs the activation and the same root for every branch of the
  slot and the next one; no `root/supply mismatch`;
- from the upgrade slot no milestone before tick 9, branches still produced, mine transits still
  settled;
- the early-cutting sequencer wins about its fair share of the ties after the slot, and the
  largest bonus wins them (`BRANCH COMMIT` with the bonus);
- a node started after the slot syncs across it; one restored from a snapshot taken before it
  too; a node holding a library that differs from the binary's refuses to start;
- the old binary refuses every transaction from the slot (`TxVersion mismatch`), as with upgrade 1.

## Rollout

1. v1.1.1 on every node, then the release with the two prerequisites on every node. A node without
   the injection fix can split at any upgrade slot with competing branches; one without the
   stored-library check can split at a slot whose library it holds wrongly.
2. Release v1.2.0 with the slot chosen a few days ahead; nodes, `proxi` and miners update before
   it. Same notice as upgrade 1, and the operators of stuck nodes after slot 29000 reminded of the
   recovery. Do not move the slot once a build listing it is out: a node that ran it would refuse
   to start until its operator restores a snapshot.
3. Watch the activation as for upgrade 1 (all five machines, root mismatches, LRB coverage).
4. Measure over the following day with `.internal/minerwatch/lineage` and the `BRANCH COMMIT`
   lines: each issuer's wins against its fair share of ties should approach 1, and the winner of
   a tie should be the branch with the largest bonus.

## Open points

- **Is 9 enough?** A sequencer whose first milestone lands a few ticks after 9 may already see
  others' tick-9 milestones, each carrying its issuer's capital, and capital can then tip the
  slot (mechanism 2 of `kb/research/branch-bias.md`). Hence the reference sequencer building
  exactly at 9; the measurement after activation tells whether that is enough.
- **Branches arriving after tick 9.** About 6% of branches, nearly all from the late-branch issue
  of the prerequisites. Once that is fixed, the rest is the tail of slow outside nodes; they lose,
  as they would under any window.
- **Inclusion latency.** Nothing settles in the first 0.72 s of a slot; a tag-along or a
  delegation output waits for tick 9. Small next to the slot's 10.24 s.
- **Early cut still possible, and no longer pays.** A sequencer can still cut its branch mid-slot;
  nobody may build on it before tick 9, and when the branches do not tie, the one cut later
  carries more of its slot and more coverage.
