package api

import (
	"github.com/lunfardo314/proxima/ledger"
)

const (
	PathGetLedgerID        = "/get_ledger_id"
	PathGetAccountOutputs  = "/get_account_outputs"
	PathGetChainOutput     = "/get_chain_output"
	PathGetOutput          = "/get_output"
	PathQueryTxIDStatus    = "/query_txid_status"
	PathSubmitTransaction  = "/submit_tx" // wait appending to the utangle_old
	PathGetOutputInclusion = "/inclusion"
	PathGetSyncInfo        = "/sync_info"
	PathGetNodeInfo        = "/node_info"
)

type Error struct {
	// empty string when no error
	Error string `json:"error,omitempty"`
}

type LedgerID struct {
	Error
	// hex-encoded ledger id bytes
	LedgerIDBytes string `json:"ledger_id_bytes,omitempty"`
}

// OutputList is returned by 'get_account_outputs'
type OutputList struct {
	Error
	// key is hex-encoded outputID bytes
	// value is hex-encoded raw output data
	Outputs map[string]string `json:"outputs,omitempty"`
}

// ChainOutput is returned by 'get_chain_output'
type ChainOutput struct {
	Error
	// hex-encoded outputID
	OutputID string `json:"output_id,omitempty"`
	// hex-encoded output data
	OutputData string `json:"output_data,omitempty"`
}

// OutputData is returned by 'get_output'
type OutputData struct {
	Error
	// hex-encoded output data
	OutputData string                 `json:"output_data,omitempty"`
	Inclusion  []InclusionDataEncoded `json:"inclusion,omitempty"`
}

type InclusionDataEncoded struct {
	BranchID string `json:"branch_id"`
	Coverage uint64 `json:"coverage"`
	Included bool   `json:"included"`
}

type InclusionData struct {
	BranchID ledger.TransactionID
	Coverage uint64
	Included bool
}

type QueryTxIDStatus struct {
	Error
	TxID   string `json:"txid"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	Err    error  `json:"err,omitempty"`
}

type (
	SyncInfo struct {
		Error
		Synced       bool                         `json:"synced"`
		InSyncWindow bool                         `json:"in_sync_window,omitempty"`
		PerSequencer map[string]SequencerSyncInfo `json:"per_sequencer,omitempty"`
	}
	SequencerSyncInfo struct {
		Synced           bool   `json:"synced"`
		LatestBookedSlot uint32 `json:"latest_booked_slot"`
		LatestSeenSlot   uint32 `json:"latest_seen_slot"`
	}
)

const ErrGetOutputNotFound = "output not found"

func (i *InclusionDataEncoded) Decode() (InclusionData, error) {
	txid, err := ledger.TransactionIDFromHexString(i.BranchID)
	if err != nil {
		return InclusionData{}, err
	}
	return InclusionData{
		BranchID: txid,
		Coverage: i.Coverage,
		Included: i.Included,
	}, nil
}
