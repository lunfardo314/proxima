package multistate

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

type (
	RootRecordJSONAble struct {
		Root          string `json:"root"`
		SequencerID   string `json:"sequencer_id"`
		CoverageDelta uint64 `json:"coverage_delta"`
		SlotInflation uint64 `json:"slot_inflation"`
		Supply        uint64 `json:"supply"`
	}

	BranchDataJSONAble struct {
		Root                 RootRecordJSONAble `json:"root"`
		StemOutputIndex      byte               `json:"stem_output_index"`
		SequencerOutputIndex byte               `json:"sequencer_output_index"`
		OnChainAmount        uint64             `json:"on_chain_amount"`
		BranchInflation      uint64             `json:"branch_inflation"`
	}
)

func (r *RootRecord) JSONAble() *RootRecordJSONAble {
	return &RootRecordJSONAble{
		Root:          r.Root.String(),
		SequencerID:   r.SequencerID.StringHex(),
		CoverageDelta: r.CoverageDelta,
		SlotInflation: r.SlotInflation,
		Supply:        r.Supply,
	}
}

func (r *RootRecordJSONAble) Parse() (*RootRecord, error) {
	ret := &RootRecord{
		SlotInflation: r.SlotInflation,
		Supply:        r.Supply,
	}
	var err error
	rootBin, err := hex.DecodeString(r.Root)
	if err != nil {
		return nil, err
	}
	ret.Root, err = common.VectorCommitmentFromBytes(ledger.CommitmentModel, rootBin)
	if err != nil {
		return nil, err
	}
	ret.SequencerID, err = base.ChainIDFromHexString(r.SequencerID)
	if err != nil {
		return nil, err
	}
	ret.CoverageDelta = r.CoverageDelta
	return ret, nil
}

func (br *BranchData) JSONAble() *BranchDataJSONAble {
	return &BranchDataJSONAble{
		Root:                 *br.RootRecord.JSONAble(),
		StemOutputIndex:      br.Stem.ID.Index(),
		SequencerOutputIndex: br.SequencerOutput.ID.Index(),
		OnChainAmount:        br.SequencerOutput.Output.TokenBalance(),
		BranchInflation:      br.SequencerOutput.Output.Inflation(),
	}
}
