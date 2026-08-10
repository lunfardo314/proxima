package task

import (
	"sync"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
)

// SlotData collect values of sequencer during one slot
type SlotData struct {
	mutex            sync.RWMutex
	slot             uint32
	numTargets       int
	seqTxSubmitted   []base.TransactionID
	branchSubmitted  *base.TransactionID
	numNoProposals   int
	numNotGoodEnough int
	// coverage out of bounds already warned this slot
	coverageBoundsWarned bool
	// factory proposal discarded already warned this slot
	factoryDiscardWarned bool
}

// WarnFactoryDiscardOnce reports whether the caller should emit the factory-discard warning,
// i.e. it has not been emitted yet in this slot.
func (s *SlotData) WarnFactoryDiscardOnce() (first bool) {
	s.withWriteLock(func() {
		first = !s.factoryDiscardWarned
		s.factoryDiscardWarned = true
	})
	return
}

func NewSlotData(slot uint32) *SlotData {
	return &SlotData{
		slot:           slot,
		seqTxSubmitted: make([]base.TransactionID, 0),
	}
}

func (s *SlotData) Slot() uint32 {
	return s.slot
}

func (s *SlotData) NewTarget() {
	s.withWriteLock(func() {
		s.numTargets++
	})
}

func (s *SlotData) SequencerTxSubmitted(txid base.TransactionID) {
	s.withWriteLock(func() {
		s.seqTxSubmitted = append(s.seqTxSubmitted, txid)
	})
}

func (s *SlotData) BranchTxSubmitted(txid base.TransactionID) {
	s.withWriteLock(func() {
		txidCopy := txid
		s.branchSubmitted = &txidCopy
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

	return ret
}

func (s *SlotData) withWriteLock(fun func()) {
	s.mutex.Lock()
	fun()
	s.mutex.Unlock()
}
