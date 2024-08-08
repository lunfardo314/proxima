package api

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/multistate"
)

const (
	PathGetLedgerID         = "/get_ledger_id"
	PathGetAccountOutputs   = "/get_account_outputs"
	PathGetChainOutput      = "/get_chain_output"
	PathGetOutput           = "/get_output"
	PathQueryTxStatus       = "/query_tx_status"
	PathQueryInclusionScore = "/query_inclusion_score"
	PathSubmitTransaction   = "/submit_tx"
	PathGetSyncInfo         = "/sync_info"
	PathGetNodeInfo         = "/node_info"
	PathGetPeersInfo        = "/peers_info"
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
	TxIDStatus vertex.TxIDStatusJSONAble       `json:"txid_status"`
	Inclusion  *multistate.TxInclusionJSONAble `json:"inclusion,omitempty"`
}

type TxInclusionScore struct {
	ThresholdNumerator   int `json:"threshold_numerator"`
	ThresholdDenominator int `json:"threshold_denominator"`
	LatestSlot           int `json:"latest_slot"`
	EarliestSlot         int `json:"earliest_slot"`
	StrongScore          int `json:"strong_score"`
	WeakScore            int `json:"weak_score"`
}

type QueryTxInclusionScore struct {
	Error
	TxInclusionScore
}

type (
	SyncInfo struct {
		Error
		Synced       bool                         `json:"synced"`
		InSyncWindow bool                         `json:"in_sync_window,omitempty"`
		PerSequencer map[string]SequencerSyncInfo `json:"per_sequencer,omitempty"`
	}
	SequencerSyncInfo struct {
		Synced              bool   `json:"synced"`
		LatestHealthySlot   uint32 `json:"latest_healthy_slot"`
		LatestCommittedSlot uint32 `json:"latest_committed_slot"`
		LedgerCoverage      uint64 `json:"ledger_coverage"`
	}
)

type (
	PeersInfo struct {
		Error
		Peers []PeerInfo `json:"peers,omitempty"`
	}
	PeerInfo struct {
		// The libp2p identifier of the peer.
		ID string `json:"id"`
		// The libp2p multi addresses of the peer.
		MultiAddresses []string `json:"multiAddresses,omitempty"`
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
