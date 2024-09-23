package task

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

// SlotData collect values of sequencer during one slot
// Proposers may keep theirs state there from target to target
type (
	SlotData struct {
		mutex               sync.RWMutex
		slot                ledger.Slot
		numTargets          int
		seqTxSubmitted      []ledger.TransactionID
		branchSubmitted     *ledger.TransactionID
		proposalsByProposer map[string]int
		numNoProposals      int
		numNotGoodEnough    int
		// base proposer
		lastExtendedOutputB0     vertex.WrappedOutput
		lastTimeBacklogCheckedB0 time.Time
		// e1 proposer optimization
		lastTimeBacklogCheckedE1 time.Time
		alreadyCheckedE1         set.Set[extendEndorsePair]
		// e2, r2 proposer optimization
		lastTimeBacklogCheckedE2 time.Time
		lastTimeBacklogCheckedR2 time.Time
		alreadyCheckedTriplets   set.Set[extendEndorseTriplet] //shared by e2 and r2
	}

	extendEndorsePair struct {
		extend  vertex.WrappedOutput
		endorse *vertex.WrappedTx
	}

	extendEndorseTriplet struct {
		extend   vertex.WrappedOutput
		endorse1 *vertex.WrappedTx
		endorse2 *vertex.WrappedTx
	}
)

func NewSlotData(slot ledger.Slot) *SlotData {
	return &SlotData{
		slot:                   slot,
		seqTxSubmitted:         make([]ledger.TransactionID, 0),
		proposalsByProposer:    make(map[string]int),
		alreadyCheckedE1:       set.New[extendEndorsePair](),
		alreadyCheckedTriplets: set.New[extendEndorseTriplet](),
	}
}

func (s *SlotData) NewTarget() {
	s.withWriteLock(func() {
		s.numTargets++
	})

}

func (s *SlotData) SequencerTxSubmitted(txid *ledger.TransactionID) {
	s.withWriteLock(func() {
		s.seqTxSubmitted = append(s.seqTxSubmitted, *txid)
	})
}

func (s *SlotData) BranchTxSubmitted(txid *ledger.TransactionID) {
	s.withWriteLock(func() {
		txidCopy := *txid
		s.branchSubmitted = &txidCopy
	})
}

func (s *SlotData) ProposalSubmitted(strategyName string) {
	s.withWriteLock(func() {
		s.proposalsByProposer[strategyName] = s.proposalsByProposer[strategyName] + 1
	})
}

func (s *SlotData) NoProposals() {
	s.withWriteLock(func() {
		s.numNoProposals++
	})
}

func (s *SlotData) NotGoodEnough() {
	s.withWriteLock(func() {
		s.numNotGoodEnough++
	})
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

func (s *SlotData) withWriteLock(fun func()) {
	s.mutex.Lock()
	fun()
	s.mutex.Unlock()
}
