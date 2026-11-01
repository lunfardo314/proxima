# sequencer v3

`v2` ranking its candidates by the adjusted coverage, the coverage adjustment, stage 2. Not
released. Everything not listed here is as in [`../v2/README.md`](../v2/README.md) and
[`../README.md`](../README.md).

The metric is defined in `kb/dag_semantics.md` (adjusted coverage) and designed in
`kb/coverage_adjustment_B.md`; the core computes it beside the classical coverage
(`attacher.FinalScore`, the score on the vertex). What differs from `v2`:

* **One rank value.** Every comparison the sequencer makes to choose what to build goes
  through `factory.RankValue`: the factory's first-move search, pair choice, re-anchor,
  improvement loop and publication gate, the task's choice among proposals and of the drained
  skeleton. In a slot ranked by score it is the adjusted coverage, otherwise the classical one.
* **Eligibility stays classical.** The ledger's coverage advance and the node's rule, more
  coverage than the own latest milestone of the slot, are decided by the classical coverage, as
  are branch health and the declared values.
* **Activation.** `factory.ScoreActivationSlot`, a constant of the release (not set in this
  build): before it `v3` behaves exactly as `v2`. Every branch-producing sequencer switches at the
  same slot, since sequencers ranking by different measures choose different lineages.
* **Fallback.** A sequencer ranks by score only from its second full slot after start: branches
  loaded at start carry no score, so until then every candidate is ranked by the classical
  coverage alike (`RankByScore`). In a slot ranked by score, a candidate the node cannot score
  ranks lowest.

Not yet in `v3` (`kb/coverage_adjustment_B.md`, sequencer decisions): one improvement loop per
baseline in the factory, instead of the single loop stopped by the slot-wide publication gate.
