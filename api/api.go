package api

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
)

const (
	PrefixAPIV1   = "/api/v1"
	PrefixTxAPIV1 = "/txapi/v1"

	PathGetLedgerID             = PrefixAPIV1 + "/get_ledger_id"
	PathGetAccountOutputs       = PrefixAPIV1 + "/get_account_outputs"
	PathGetChainOutput          = PrefixAPIV1 + "/get_chain_output"
	PathGetOutput               = PrefixAPIV1 + "/get_output"
	PathQueryTxStatus           = PrefixAPIV1 + "/query_tx_status"
	PathQueryInclusionScore     = PrefixAPIV1 + "/query_inclusion_score"
	PathSubmitTransaction       = PrefixAPIV1 + "/submit_tx"
	PathGetSyncInfo             = PrefixAPIV1 + "/sync_info"
	PathGetNodeInfo             = PrefixAPIV1 + "/node_info"
	PathGetPeersInfo            = PrefixAPIV1 + "/peers_info"
	PathGetLatestReliableBranch = PrefixAPIV1 + "/get_latest_reliable_branch"
	PathCheckTxIDInLRB          = PrefixAPIV1 + "/check_txid_in_lrb"
	// PathGetDashboard returns dashboard
	PathGetDashboard = "/dashboard"

	// Transaction API calls

	PathCompileScript        = PrefixTxAPIV1 + "/compile_script"
	PathDecompileBytecode    = PrefixTxAPIV1 + "/decompile_bytecode"
	PathParseOutput          = PrefixTxAPIV1 + "/parse_output"
	PathGetTxBytes           = PrefixTxAPIV1 + "/get_txbytes"
	PathGetParsedTransaction = PrefixTxAPIV1 + "/get_parsed_transaction"
)

type (
	Error struct {
		// empty string when no error
		Error string `json:"error,omitempty"`
	}

	LedgerID struct {
		Error
		// hex-encoded ledger id bytes
		LedgerIDBytes string `json:"ledger_id_bytes,omitempty"`
	}

	// OutputList is returned by 'get_account_outputs'
	OutputList struct {
		Error
		// key is hex-encoded outputID bytes
		// value is hex-encoded raw output data
		Outputs map[string]string `json:"outputs,omitempty"`
		// latest reliable branch used to extract outputs
		LRBID string `json:"lrb_id"`
	}

	// ChainOutput is returned by 'get_chain_output'
	ChainOutput struct {
		Error
		// hex-encoded outputID
		OutputID string `json:"output_id,omitempty"`
		// hex-encoded output data
		OutputData string `json:"output_data,omitempty"`
		// latest reliable branch used to extract chain ID
		LRBID string `json:"lrb_id"`
	}

	// OutputData is returned by 'get_output'
	OutputData struct {
		Error
		// hex-encoded output data
		OutputData string `json:"output_data,omitempty"`
		// latest reliable branch used to extract output
		LRBID string `json:"lrb_id"`
	}

	QueryTxStatus struct {
		Error
		TxIDStatus vertex.TxIDStatusJSONAble       `json:"txid_status"`
		Inclusion  *multistate.TxInclusionJSONAble `json:"inclusion,omitempty"`
	}

	TxInclusionScore struct {
		ThresholdNumerator   int    `json:"threshold_numerator"`
		ThresholdDenominator int    `json:"threshold_denominator"`
		LatestSlot           int    `json:"latest_slot"`
		EarliestSlot         int    `json:"earliest_slot"`
		StrongScore          int    `json:"strong_score"`
		WeakScore            int    `json:"weak_score"`
		LRBID                string `json:"lrb_id"`
		IncludedInLRB        bool   `json:"included_in_lrb"`
	}

	QueryTxInclusionScore struct {
		Error
		TxInclusionScore
	}

	SyncInfo struct {
		Error
		Synced         bool                         `json:"synced"`
		CurrentSlot    uint32                       `json:"current_slot"`
		LrbSlot        uint32                       `json:"lrb_slot"`
		LedgerCoverage string                       `json:"ledger_coverage"`
		PerSequencer   map[string]SequencerSyncInfo `json:"per_sequencer,omitempty"`
	}

	SequencerSyncInfo struct {
		Synced              bool   `json:"synced"`
		LatestHealthySlot   uint32 `json:"latest_healthy_slot"`
		LatestCommittedSlot uint32 `json:"latest_committed_slot"`
		LedgerCoverage      uint64 `json:"ledger_coverage"`
	}

	PeersInfo struct {
		Error
		HostID    string            `json:"host_id"`
		Peers     []PeerInfo        `json:"peers,omitempty"`
		Blacklist map[string]string `json:"blacklist,omitempty"` // map: peerID -> reason why it is in the blacklist
	}

	PeerInfo struct {
		// The libp2p identifier of the peer.
		ID string `json:"id"`
		// The libp2p multi addresses of the peer.
		MultiAddresses            []string `json:"multiAddresses,omitempty"`
		IsStatic                  bool     `json:"is_static"`
		RespondsToPull            bool     `json:"responds_to_pull"`
		IsAlive                   bool     `json:"is_alive"`
		WhenAdded                 int64    `json:"when_added"`
		LastHeartbeatReceived     int64    `json:"last_heartbeat_received"`
		ClockDifferencesQuartiles [3]int64 `json:"clock_differences_quartiles"`
		HBMsgDifferencesQuartiles [3]int64 `json:"hb_differences_quartiles"`
		NumIncomingHB             int      `json:"num_incoming_hb"`
		NumIncomingPull           int      `json:"num_incoming_pull"`
		NumIncomingTx             int      `json:"num_incoming_tx"`
	}

	// LatestReliableBranch returned by get_latest_reliable_branch
	LatestReliableBranch struct {
		Error
		RootData multistate.RootRecordJSONAble `json:"root_record,omitempty"`
		BranchID ledger.TransactionID          `json:"branch_id,omitempty"`
	}

	CheckRxIDInLRB struct {
		Error
		LRBID    string `json:"lrb_id"`
		TxID     string `json:"txid"`
		Included bool   `json:"included"`
	}
)

const ErrGetOutputNotFound = "output not found"

func CalcTxInclusionScore(inclusion *multistate.TxInclusion, thresholdNumerator, thresholdDenominator int) TxInclusionScore {
	ret := TxInclusionScore{
		ThresholdNumerator:   thresholdNumerator,
		ThresholdDenominator: thresholdDenominator,
		LatestSlot:           int(inclusion.LatestSlot),
		EarliestSlot:         int(inclusion.EarliestSlot),
		StrongScore:          0,
		WeakScore:            0,
	}
	if len(inclusion.Inclusion) == 0 {
		return ret
	}
	var includedInBranches, numDominatingBranches, numIncludedInDominating int
	for i := range inclusion.Inclusion {
		if inclusion.Inclusion[i].Included {
			includedInBranches++
		}
		if inclusion.Inclusion[i].RootRecord.IsCoverageAboveThreshold(thresholdNumerator, thresholdDenominator) {
			numDominatingBranches++
			if inclusion.Inclusion[i].Included {
				numIncludedInDominating++
			}
		}
	}
	ret.WeakScore = (includedInBranches * 100) / len(inclusion.Inclusion)
	if numDominatingBranches > 0 {
		ret.StrongScore = (numIncludedInDominating * 100) / numDominatingBranches
	}
	return ret
}
