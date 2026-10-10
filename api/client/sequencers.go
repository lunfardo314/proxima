package client

import (
	"fmt"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// SequencerCandidates turns the node's sequencer list, the sequencer outputs
// in the LRB state, into rating candidates (kb/sequencer_rating.md), split
// into those active within txbuildercore.ActiveSequencerSlots of the LRB, not
// by a bootstrap transaction, and the rest.
func SequencerCandidates(outs map[base.ChainID]SequencerOutput, lrbID *base.TransactionID) (active, inactive []txbuildercore.SequencerCandidate) {
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

// MinStorageDeposit is the minimum storage deposit of out, computed
// wallet-side: the effective size (output bytes + index-values tuple bytes +
// one 33-byte trie row per index value, mirroring the ledger's
// effectiveStorageSize) fed through the `storageDeposit($0)` schedule on the
// node's evaluator.
func (c *APIClient) MinStorageDeposit(out *txbuildercore.Output) (uint64, error) {
	size := uint64(len(out.Bytes()))
	if ivBin, err := out.ConstraintAt(ledger.ConstraintIndexIndexValues); err == nil && len(ivBin) > 0 {
		values, err := ledger.IndexValuesFromBytes(ivBin)
		if err != nil {
			return 0, err
		}
		size += uint64(len(ivBin)) + uint64(len(values))*33
	}
	return c.EvalU64(0, fmt.Sprintf("storageDeposit(u64/%d)", size))
}
