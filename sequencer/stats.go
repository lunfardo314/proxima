package sequencer

import (
	"sync"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// slotStats collect values of sequencer performance. Usually per slot, the resets
type slotStats struct {
	mutex               sync.RWMutex
	slot                ledger.Slot
	numTargets          int
	seqTxSubmitted      []ledger.TransactionID
	branchSubmitted     *ledger.TransactionID
	proposalsByProposer map[string]int
}

func _newSlotStats(slot ledger.Slot) slotStats {
	return slotStats{
		slot:                slot,
		seqTxSubmitted:      make([]ledger.TransactionID, 0),
		proposalsByProposer: make(map[string]int),
	}
}
func (s *slotStats) StatsReset(slot ledger.Slot) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.slot = slot
	s.numTargets = 0
	s.branchSubmitted = nil
	s.proposalsByProposer = make(map[string]int)
	s.seqTxSubmitted = s.seqTxSubmitted[:0]
}

func (s *slotStats) StatsNewTarget() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.numTargets++
}

func (s *slotStats) StatsSequencerTxSubmitted(txid *ledger.TransactionID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.seqTxSubmitted = append(s.seqTxSubmitted, *txid)
}

func (s *slotStats) StatsBranchTxSubmitted(txid *ledger.TransactionID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	txidCopy := *txid
	s.branchSubmitted = &txidCopy
}

func (s *slotStats) StatsProposalSubmitted(strategyName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.proposalsByProposer[strategyName] = s.proposalsByProposer[strategyName] + 1
}

func (s *slotStats) Lines(prefix ...string) *lines.Lines {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	ret := lines.New(prefix...)
	ret.Add("slot: %d", s.slot).
		Add("targets: %d", s.numTargets).
		Add("seq tx submitted: %d", len(s.seqTxSubmitted))
	if s.branchSubmitted == nil {
		ret.Add("branch: NONE")
	} else {
		ret.Add("branch: 1")
	}
	for _, strategy := range util.KeysSorted(s.proposalsByProposer, util.StringsLess) {
		ret.Add("'%s': %d", strategy, s.proposalsByProposer[strategy])
	}

	return ret
}
