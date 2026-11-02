# sequencer v3

`v2` ranking its candidates by the adjusted coverage, the coverage adjustment, stage 2. The
default from v1.4.0 (`sequencer.version: v3`, or the key absent); `v2` is its fallback. Everything not listed here is as in [`../v2/README.md`](../v2/README.md) and
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
* **Activation.** `factory.ScoreActivationSlot`, a constant of the release: slot 138000,
  1000 slots after library upgrade 4. Before it `v3` behaves exactly as `v2`. Every branch-producing sequencer switches at the
  same slot, since sequencers ranking by different measures choose different lineages.
* **Fallback.** A sequencer ranks by score only from its second full slot after start: branches
  loaded at start carry no score, so until then every candidate is ranked by the classical
  coverage alike (`RankByScore`). In a slot ranked by score, a candidate the node cannot score
  ranks lowest.

* **One improvement loop per lineage** (`kb/coverage_adjustment_B.md`, sequencer decisions), in a
  slot ranked by score. When the chain head is not on the leading lineage, a round is seeded both
  with the head's pair and with a re-anchor onto the leading candidate's lineage, and each is
  improved on its own: a loop goes on while a step improves on the previous one (the local
  threshold), and only steps at least as good as the best posted in the slot are posted (the
  publication threshold). A lineage starting below another can so overtake it once endorsements
  are added. Before activation the round is as in `v2`: the better seed alone, improved while its
  steps are posted.
