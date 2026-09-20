package glb

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// SequencerCandidates turns the node's sequencer list, the sequencer outputs
// in the LRB state, into rating candidates (kb/sequencer_rating.md), split
// into those active within txbuildercore.ActiveSequencerSlots of the LRB and
// the rest. An output without sequencer data leaves delegators everything
// and asks no fee.
func SequencerCandidates(outs map[base.ChainID]ledger.OutputWithSequencerData, lrbID *base.TransactionID) (active, inactive []txbuildercore.SequencerCandidate) {
	lrbSlot := lrbID.Slot()
	for id, o := range outs {
		c := txbuildercore.SequencerCandidate{
			ID:        id,
			Slot:      o.ID.Slot(),
			Balance:   o.Output.TokenBalance(),
			ShareLeft: 1000,
		}
		if frozen := o.Output.FrozenCoverage(0); frozen > 0 {
			c.FrozenCoverage = uint64(frozen)
		}
		if sd := o.SequencerData; sd != nil {
			c.Name = sd.Name()
			c.ShareLeft -= sd.InflationProfitMarginPromille()
			c.MinimumFee = sd.MinimumFee()
		}
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
