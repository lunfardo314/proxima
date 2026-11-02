package factory

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// ScoreActivationSlot is the first slot from which a v3 sequencer ranks candidates by the adjusted
// coverage (kb/dag_semantics.md, adjusted coverage; kb/coverage_adjustment.md): 1000 slots after
// library upgrade 4, 2026-11-03 15:04:19 UTC. It belongs to the node release, not to the
// configuration: sequencers ranking by different measures in one slot choose different lineages for
// the same inputs, so every node switches at the same slot. A variable only so that tests and
// rehearsals can set it.
var ScoreActivationSlot uint32 = ledger.Upgrade4Slot + 1000

// RankByScore is implemented by the sequencer: whether the comparisons of a target slot rank by the
// adjusted coverage.
type RankByScore interface {
	RankByScore(slot uint32) bool
}

// RankValue is what a v3 sequencer ranks a candidate by: the adjusted coverage in a slot ranked by
// score, the classical coverage otherwise. Eligibility is never decided by it, always by the
// classical coverage. In a slot ranked by score, a candidate the node cannot score ranks lowest: its
// baseline was not attached against its own history.
func RankValue(env RankByScore, a *attacher.IncrementalAttacher, ts base.LedgerTime) uint64 {
	if !env.RankByScore(ts.Slot) {
		return a.FinalLedgerCoverage(ts)
	}
	score, ok := a.FinalScore(ts)
	if !ok {
		return 0
	}
	return score
}
