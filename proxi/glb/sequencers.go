package glb

import (
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// FetchSequencerCandidates is client.SequencerCandidates over the node's
// current sequencer list.
func FetchSequencerCandidates() (active, inactive []txbuildercore.SequencerCandidate, err error) {
	outs, lrbID, err := GetClient().GetAllSequencerOutputs()
	if err != nil {
		return nil, nil, err
	}
	active, inactive = client.SequencerCandidates(outs, lrbID)
	return active, inactive, nil
}
