package api

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
)

const (
	PathGetLedgerID       = "/get_ledger_id"
	PathGetAccountOutputs = "/get_account_outputs"
	PathGetChainOutput    = "/get_chain_output"
	PathGetOutput         = "/get_output"
	PathQueryTxStatus     = "/query_tx_status"
	PathSubmitTransaction = "/submit_tx"
	PathGetSyncInfo       = "/sync_info"
	PathGetNodeInfo       = "/node_info"
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
	OutputData string `json:"output_data,omitempty"`
	//Inclusion  []InclusionDataEncoded `json:"inclusion,omitempty"`
}

type QueryTxStatus struct {
	Error
	TxIDStatus vertex.TxIDStatusJSONAble              `json:"txid_status"`
	Inclusion  map[string]tippool.TxInclusionJSONAble `json:"inclusion"`
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
