package sequencer

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

// SlotStats collect values of sequencer performance. Usually per slot, the resets
type SlotStats struct {
	slot                ledger.Slot
	seqTxSubmitted      []ledger.TransactionID
	branchSubmitted     *ledger.TransactionID
	proposalsByProposer map[string]int
	winningProposers    set.Set[string]
}

func _new(slot ledger.Slot) SlotStats {
	return SlotStats{
		slot:                slot,
		seqTxSubmitted:      make([]ledger.TransactionID, 0),
		proposalsByProposer: make(map[string]int),
		winningProposers:    set.New[string](),
	}
}
func (s *SlotStats) Reset(slot ledger.Slot) {
	*s = _new(slot)
}

func (s *SlotStats) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	return ret
}
