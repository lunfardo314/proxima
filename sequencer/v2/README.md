# sequencer v2

`v1` with the strategy by zone of the coverage adjustment, stage 1. The default
from v1.3.9 (`sequencer.version: v2`, or the key absent). Everything not listed here is as
described in [`../README.md`](../README.md).

The design, its status and the live trial results are in
`kb/sequencer_search.md`. What differs from `v1`:

* **Post-branch zone (first move).** The factory searches every extend-endorse
  pair of the slot, and staying on the own branch, and the best by coverage
  goes out with no tag-alongs (`factory/first_move.go`; `proposer_base.go`
  adds no inputs when extending the own branch).
* **Re-anchor.** A round seeded after the first move compares the head's pair
  with a re-anchor on the leading candidate's lineage and takes the one with
  more coverage. A re-anchor spends an own output of an earlier slot again, so
  it is taken only to move to another lineage: a skeleton spending an output
  that an own milestone of the slot already spent on the same lineage is
  dropped (`forksOwnChain` in `task/proposer_factory.go`, in `v1` too).
* **Pre-branch zone.** Two milestones close the slot: a closing one at the
  first tick the pace allows, and the final one at the last tick, each skipped
  when it has nothing new (`strategy_async.go`).
* **Eligibility before maximisation.** `task.Run` drops proposals that do not
  beat the own latest milestone of the slot before choosing the best, so an
  ineligible best no longer hides an eligible one.
* **No build past the slot boundary.** A milestone build is bounded by the
  boundary, and one finished after it is not submitted (counted as
  `late dropped` in the slot stats).
