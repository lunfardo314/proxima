package task

import (
	"sync"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// SlotData collect values of sequencer during one slot
// Proposers may keep theirs state there from target to target
type SlotData struct {
	mutex               sync.RWMutex
	slot                ledger.Slot
	numTargets          int
	seqTxSubmitted      []ledger.TransactionID
	branchSubmitted     *ledger.TransactionID
	proposalsByProposer map[string]int
	numNoProposals      int
	numNotGoodEnough    int
}

func NewSlotData(slot ledger.Slot) *SlotData {
	return &SlotData{
		slot:                slot,
		seqTxSubmitted:      make([]ledger.TransactionID, 0),
		proposalsByProposer: make(map[string]int),
	}
}

func (s *SlotData) NewTarget() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.numTargets++
}

func (s *SlotData) SequencerTxSubmitted(txid *ledger.TransactionID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.seqTxSubmitted = append(s.seqTxSubmitted, *txid)
}

func (s *SlotData) BranchTxSubmitted(txid *ledger.TransactionID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	txidCopy := *txid
	s.branchSubmitted = &txidCopy
}

func (s *SlotData) ProposalSubmitted(strategyName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.proposalsByProposer[strategyName] = s.proposalsByProposer[strategyName] + 1
}

func (s *SlotData) NoProposals() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.numNoProposals++
}

func (s *SlotData) NotGoodEnough() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.numNotGoodEnough++
}

func (s *SlotData) Lines(prefix ...string) *lines.Lines {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	ret := lines.New(prefix...)
	ret.Add("slot: %d", s.slot).
		Add("targets: %d", s.numTargets).
		Add("seq tx submitted: %d", len(s.seqTxSubmitted)).
		Add("no proposals: %d", s.numNoProposals).
		Add("not good enough: %d", s.numNotGoodEnough)
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
