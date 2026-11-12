# sequencer v3

The reference sequencer and the default from v1.4.0 (`sequencer.version: v3`, or the key absent):
the strategy by zone of the coverage adjustment, stage 1, ranking its candidates by the adjusted
coverage, stage 2. Stage 1 alone was version `v2`, removed on 2026-11-12; `v1` was removed on
2026-11-08. Everything not listed here is as described in [`../README.md`](../README.md).

## Strategy by zone (stage 1)

The design, its status and the live trial results are in `kb/sequencer_search.md`.

* **Post-branch zone (first move).** The factory searches every extend-endorse
  pair of the slot, and staying on the own branch, and the best goes out with
  no tag-alongs (`factory/first_move.go`; `proposer_base.go` adds no inputs
  when extending the own branch).
* **Re-anchor.** A round seeded after the first move compares the head's pair
  with a re-anchor on the leading candidate's lineage and takes the better one.
  A re-anchor spends an own output of an earlier slot again, so it is taken
  only to move to another lineage: a skeleton spending an output that an own
  milestone of the slot already spent on the same lineage is dropped
  (`forksOwnChain` in `task/proposer_factory.go`).
* **Pre-branch zone.** Two milestones close the slot: a closing one at the
  first tick the pace allows, and the final one `finalConsolidationLeadTicks`
  (7) before the boundary, retried each tick, each skipped when it has nothing
  new (`strategy_async.go`). The branch goes out at the last tick on the
  freshest own milestone.
* **Eligibility before maximisation.** `task.Run` drops proposals that do not
  beat the own latest milestone of the slot before choosing the best, so an
  ineligible best does not hide an eligible one.
* **No build past the slot boundary.** A milestone build is bounded by the
  boundary, and one finished after it is not submitted (counted as
  `late dropped` in the slot stats).

## Ranking by the adjusted coverage (stage 2)

The metric is defined in `kb/dag_semantics.md` (adjusted coverage) and designed in
`kb/coverage_adjustment.md`; the core computes it beside the classical coverage
(`attacher.FinalScore`, the score on the vertex).

* **One rank value.** Every comparison the sequencer makes to choose what to build goes
  through `factory.RankValue`: the factory's first-move search, pair choice, re-anchor,
  improvement loop and publication gate, the task's choice among proposals and of the drained
  skeleton. In a slot ranked by score it is the adjusted coverage, otherwise the classical one.
* **Eligibility stays classical.** The ledger's coverage advance and the node's rule, more
  coverage than the own latest milestone of the slot, are decided by the classical coverage, as
  are branch health and the declared values.
* **Activation.** `factory.ScoreActivationSlot`, a constant of the release: slot 138000,
  1000 slots after library upgrade 4. Before it `v3` ranks by the classical coverage (stage 1 alone). Every branch-producing sequencer switches at the
  same slot, since sequencers ranking by different measures choose different lineages.
* **Fallback.** A sequencer ranks by score only from its second full slot after start: branches
  loaded at start carry no score, so until then every candidate is ranked by the classical
  coverage alike (`RankByScore`). In a slot ranked by score, a candidate the node cannot score
  ranks lowest.

* **One improvement loop per lineage** (`kb/coverage_adjustment.md`, sequencer decisions), in a
  slot ranked by score. When the chain head is not on the leading lineage, a round is seeded both
  with the head's pair and with a re-anchor onto the leading candidate's lineage, and each is
  improved on its own: a loop goes on while a step improves on the previous one (the local
  threshold), and only steps at least as good as the best posted in the slot are posted (the
  publication threshold). A lineage starting below another can so overtake it once endorsements
  are added. Before activation the round is as in stage 1: the better seed alone, improved while its
  steps are posted.
