package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
)

type (
	RootInclusion struct {
		BranchID   ledger.TransactionID
		RootRecord RootRecord
		Included   bool
	}

	RootInclusionJSONAble struct {
		BranchID   string             `json:"branch_id"`
		RootRecord RootRecordJSONAble `json:"root_record"`
		Included   bool               `json:"included"`
	}

	TxInclusion struct {
		TxID         ledger.TransactionID
		LatestSlot   ledger.Slot
		EarliestSlot ledger.Slot
		Inclusion    []RootInclusion
	}

	TxInclusionJSONAble struct {
		TxID         string                  `json:"txid"`
		LatestSlot   ledger.Slot             `json:"latest_slot"`
		EarliestSlot ledger.Slot             `json:"earliest_slot"`
		Inclusion    []RootInclusionJSONAble `json:"inclusion"`
	}
)

func (i *TxInclusion) JSONAble() *TxInclusionJSONAble {
	ret := &TxInclusionJSONAble{
		TxID:         i.TxID.StringHex(),
		LatestSlot:   i.LatestSlot,
		EarliestSlot: i.EarliestSlot,
		Inclusion:    make([]RootInclusionJSONAble, len(i.Inclusion)),
	}
	for j := range i.Inclusion {
		ret.Inclusion[j] = RootInclusionJSONAble{
			BranchID:   "",
			RootRecord: *i.Inclusion[j].RootRecord.JSONAble(),
			Included:   i.Inclusion[j].Included,
		}
	}
	return ret
}

// GetTxInclusion return information about transaction's inclusion into all branches some slots back from the latest.
func GetTxInclusion(store global.StateStoreReader, txid *ledger.TransactionID, slotsBack ...int) *TxInclusion {
	latestSlot := FetchLatestSlot(store)
	ret := &TxInclusion{
		TxID:         *txid,
		LatestSlot:   latestSlot,
		EarliestSlot: latestSlot,
		Inclusion:    nil,
	}
	back := 1
	if len(slotsBack) > 0 && slotsBack[0] > 1 {
		back = slotsBack[0]
	}
	rootRecords := FetchRootRecordsNSlotsBack(store, back)
	branches := FetchBranchDataMulti(store, rootRecords...)
	incl := make([]RootInclusion, len(rootRecords))

	for i := range rootRecords {
		incl[i].RootRecord = rootRecords[i]
		incl[i].Included = RootHasTransaction(store, incl[i].RootRecord.Root, txid)
		incl[i].BranchID = branches[i].Stem.ID.TransactionID()
		if incl[i].BranchID.Slot() < latestSlot {
			ret.EarliestSlot = incl[i].BranchID.Slot()
		}
	}
	ret.Inclusion = incl
	return ret
}

func (r *RootInclusionJSONAble) Parse() (*RootInclusion, error) {
	rr, err := r.RootRecord.Parse()
	if err != nil {
		return nil, err
	}
	return &RootInclusion{
		BranchID:   ledger.TransactionID{},
		RootRecord: *rr,
		Included:   r.Included,
	}, nil
}

func (incl *TxInclusionJSONAble) Parse() (*TxInclusion, error) {
	ret := &TxInclusion{
		LatestSlot:   incl.LatestSlot,
		EarliestSlot: incl.EarliestSlot,
		Inclusion:    make([]RootInclusion, len(incl.Inclusion)),
	}
	var err error
	if ret.TxID, err = ledger.TransactionIDFromHexString(incl.TxID); err != nil {
		return nil, err
	}
	for i := range ret.Inclusion {
		rr, err := incl.Inclusion[i].RootRecord.Parse()
		if err != nil {
			return nil, err
		}
		branchID, err := ledger.TransactionIDFromHexString(incl.Inclusion[i].BranchID)
		if err != nil {
			return nil, err
		}
		ret.Inclusion[i] = RootInclusion{
			BranchID:   branchID,
			RootRecord: *rr,
			Included:   ret.Inclusion[i].Included,
		}
	}
	return ret, nil
}

func (i *TxInclusion) String() string {
	return fmt.Sprintf("txid: %s, slot from %d to %d, num roots: %d", i.TxID.StringShort(), i.EarliestSlot, i.LatestSlot, len(i.Inclusion))
}
