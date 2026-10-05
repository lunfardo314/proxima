package glb

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// SequencerCandidates turns the node's sequencer list, the sequencer outputs
// in the LRB state, into rating candidates (kb/sequencer_rating.md), split
// into those active within txbuildercore.ActiveSequencerSlots of the LRB, not
// by a bootstrap transaction, and the rest.
func SequencerCandidates(outs map[base.ChainID]client.SequencerOutput, lrbID *base.TransactionID) (active, inactive []txbuildercore.SequencerCandidate) {
	lrbSlot := lrbID.Slot()
	for id, o := range outs {
		c := api.SequencerCandidate(id, &o.OutputWithID)
		c.Bootstrap = o.Bootstrap
		if c.Active(lrbSlot) {
			active = append(active, c)
		} else {
			inactive = append(inactive, c)
		}
	}
	return active, inactive
}

// FetchSequencerCandidates is SequencerCandidates over the node's current
// sequencer list.
func FetchSequencerCandidates() (active, inactive []txbuildercore.SequencerCandidate, err error) {
	outs, lrbID, err := GetClient().GetAllSequencerOutputs()
	if err != nil {
		return nil, nil, err
	}
	active, inactive = SequencerCandidates(outs, lrbID)
	return active, inactive, nil
}
