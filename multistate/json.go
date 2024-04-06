package multistate

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
)

func (r *RootRecord) JSONAble() *RootRecordJSONAble {
	return &RootRecordJSONAble{
		Root:           r.Root.String(),
		SequencerID:    r.SequencerID.StringHex(),
		LedgerCoverage: r.LedgerCoverage,
		SlotInflation:  r.SlotInflation,
		Supply:         r.Supply,
	}
}

func RootRecordFromJSONAble(r *RootRecordJSONAble) (*RootRecord, error) {
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
	ret.SequencerID, err = ledger.ChainIDFromHexString(r.SequencerID)
	if err != nil {
		return nil, err
	}
	ret.LedgerCoverage = r.LedgerCoverage
	return ret, nil
}
