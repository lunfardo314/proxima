# Library upgrade 2: post-branch consolidation ticks

> SPEC, not built (2026-10-21). The second live library upgrade, no reset. Restores a ledger rule
> removed in v0.8.0: no sequencer transaction other than a branch before tick 12 of its slot, so
> that every sequencer meets the slot's branches bare and the random branch inflation bonus
> decides between them. Why: `kb/research/branch-bias.md`. Mechanism and the first upgrade:
> `ledger/upgrade.md`, `kb/library_upgrade1.md`. Prerequisite: v1.1.1 on every node.

## The problem

Competing branches of a slot tie on coverage in 73% to 85% of slots, because frozen delegations
are nearly the whole supply and count in full on every branch. The whitepaper expects the branch
inflation bonus to decide such ties: each sequencer maximises the coverage of its next
transaction, and with both branches bare in front of it the amounts on both sides cancel and the
bonus is the difference.

That only holds if nobody has built on a branch yet. Today a sequencer may build on any branch at
tick 1: endorsing needs a 1-tick gap, and extending its own branch the sequencer pace, 3 ticks.
The first branch to arrive gathers milestones within a few ticks; from then on maximising
coverage follows it, and the bonus never decides. Measured on slots 26,000 to 28,999: VULCAN,
whose branches are cut mid-slot and commit about 190 ms into the slot against about 450 ms for
the founder's, won 83% of slots, 2.35 times its fair share of the ties; before it MORA took 76%.

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
`constPostBranchConsolidationTicks` = 12 or later. A sequencer transaction at tick 0 is a branch
by definition (`_predIsBranch` in `sequencer.easyfl`, `TransactionID.IsBranchTransaction`), so
the rule is: a branch, or tick >= 12. It covers chain
origins carrying the sequencer constraint and bootstrap transactions as well; neither may be at
tick 0 already.

With the pre-branch zone (ticks 103 to 127, single-input milestones only) the slot becomes:

```
tick 0        12                                  102       127
branch | no sequencer tx |  active zone                 | pre-branch zone |
```

Ticks 1 to 11, 0.88 s, are for the branches to propagate. Arrivals measured on slots 26,000 to
29,342 at an access node: 95.6% of all branches within 12 ticks, 99.4% of boot's; the founder's
other nodes have a tail near 1.3 s. Not covered: the mine settlement window (ticks 86 to 102) and
the transit stamping tick (83) are at the other end of the slot; non-sequencer transactions are
not affected.

## EasyFL (`ledger/def/upgrade2.easyfl`)

Applied on top of the library of upgrade 1, in the same way:

- **New** `constPostBranchConsolidationTicks : u64/12`.
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
output, origins included, not for consumed ones.

## Go

- **Upgrade definition.** `ledger/def_upgrade2.go`: `BuildUpgrade2Library`, the same steps as
  `BuildUpgrade1Library` on its own source; factor the shared body into one function taking the
  source and a name, now that there are two. Listed in `ledger.Upgrades` with its slot and the
  description "sequencer transactions only from tick 12".
- **Constants.** `ledger/constants.go` loads `constPostBranchConsolidationTicks` as optional: 0
  when the library lacks it (every library before upgrade 2). Every other constant is loaded with
  an assertion today, which would stop a node on the genesis library. New field
  `PostBranchConsolidationTicks` in `txbuildercore.Constants`, JSON
  `post_branch_consolidation_ticks`, so wallets read it per slot like `upgrade_index`.
- **Attacher.** `IncrementalAttacher.TimestampLowerBound` for a non-branch target: also at least
  `T(targetSlot, PostBranchConsolidationTicks)` of `ledger.L(targetSlot)`. Per-slot library, so
  the floor is 0 before the upgrade slot and 12 from it, with no condition in the code.
- **Reference sequencer.** Gets the floor through the attacher's lower bound: the proposer factory
  takes `max(target, lowerBound)`. Check that the first pulse of a slot, the bootstrap path and the
  mine settlement pulse all go through it; restore a helper like the removed
  `IsPostBranchConsolidationTimestamp` only if one of them does not. Timing: the first milestone
  of a slot at tick 12 exactly, not later, so that every sequencer meets the branches bare
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
  at tick 5 are valid; from the slot both are refused with the new error, at tick 12 both are
  valid, a branch at tick 0 is valid on both sides.
- `ledger/multistate`: the upgrade builds on upgrade 1's library and stores with the checked
  writer, as `TestStoreUpgrade_*`.
- Attacher: the lower bound of a non-branch target is 12 from the slot, the pace before it.
- Sequencer tests (`tests/`, one at a time, under `-race` since `core/attacher` and `sequencer`
  change): a multi-sequencer run across the upgrade slot keeps producing milestones and branches.
- `TestUpgradeUTXOInjectedIntoEveryBranchOfTheSlot` stays: it guards the failure of slot 29000.

## Rehearsal

The rehearsal of upgrade 1 ran one sequencer and never produced competing branches at the upgrade
slot, which is where the injection bug was. This one must:

- a local network with at least three sequencers producing branches, an access node, a miner;
  upgrade 2 listed a few minutes ahead (scratch patch, never committed);
- at the upgrade slot every node logs the activation and the same root for every branch of the
  slot and the next one; no `root/supply mismatch`;
- from the upgrade slot no milestone before tick 12, branches still produced, mine transits
  still settled;
- a node started after the slot syncs across it; one restored from a snapshot taken before it
  too;
- the old binary refuses every transaction from the slot (`TxVersion mismatch`), as with upgrade 1.

## Rollout

1. v1.1.1 on every node first. A node without the injection fix can split from the network at
   any upgrade slot with competing branches.
2. Release v1.2.0 with the slot chosen a few days ahead; nodes, `proxi` and miners update before
   it. Same notice as upgrade 1, and the operators of stuck nodes after slot 29000 reminded of
   the recovery.
3. Watch the activation as for upgrade 1 (all five machines, root mismatches, LRB coverage).
4. Measure over the following day with `.internal/minerwatch/lineage` and the `BRANCH COMMIT`
   lines: each issuer's wins against its fair share of ties should approach 1, and the winner of
   a tie should be the branch with the largest bonus.

## Open points

- **Is 12 enough?** A sequencer whose first milestone lands a few ticks after 12 may already see
  others' tick-12 milestones, each carrying its issuer's capital, and capital can then tip the
  slot (mechanism 2 of `kb/research/branch-bias.md`). How often decides between keeping 12 with
  the reference sequencer building exactly at 12, and a larger value.
- **Branches arriving after tick 12.** About 4% of branches, mostly the tail of the founder's
  non-bootstrap nodes. They miss the first round and lose. Why those nodes have a 1.3 s tail is
  worth knowing on its own.
- **Inclusion latency.** Nothing settles in the first 0.96 s of a slot; a tag-along or a
  delegation output waits for tick 12. Small next to the slot's 10.24 s.
- **Early cut still possible.** A sequencer can still cut its branch mid-slot and release it
  early; it no longer gains anything, since nobody may build on it before tick 12.
