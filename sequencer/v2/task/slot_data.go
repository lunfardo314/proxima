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
	// milestones built after the slot boundary and not submitted
	numLateDropped int
	// coverage out of bounds already warned this slot
	coverageUpperBoundWarned bool
	// outcome of the pre-branch zone milestones by kind ("closing", "final"): issued or skipped
	zoneMilestones []string
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

func (s *SlotData) LateDropped() {
	s.withWriteLock(func() {
		s.numLateDropped++
	})
}

// ZoneMilestone records whether a pre-branch zone milestone of the given kind was issued or
// skipped, which is when it had nothing new to raise coverage with.
func (s *SlotData) ZoneMilestone(kind string, issued bool) {
	outcome := "skipped"
	if issued {
		outcome = "issued"
	}
	s.withWriteLock(func() {
		s.zoneMilestones = append(s.zoneMilestones, kind+" "+outcome)
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
		Add("not good enough: %d", s.numNotGoodEnough).
		Add("late dropped: %d", s.numLateDropped)
	for _, z := range s.zoneMilestones {
		ret.Add("%s", z)
	}
	if s.branchSubmitted == nil {
		ret.Add("branch: NONE")
	} else {
		ret.Add("branch: %s", s.branchSubmitted.StringShort())
	}

	return ret
}

func (s *SlotData) withWriteLock(fun func()) {
	s.mutex.Lock()
	fun()
	s.mutex.Unlock()
}
