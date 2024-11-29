package api

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
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

	PathCompileScript             = PrefixTxAPIV1 + "/compile_script"
	PathDecompileBytecode         = PrefixTxAPIV1 + "/decompile_bytecode"
	PathParseOutputData           = PrefixTxAPIV1 + "/parse_output_data"
	PathParseOutput               = PrefixTxAPIV1 + "/parse_output"
	PathGetTxBytes                = PrefixTxAPIV1 + "/get_txbytes"
	PathGetParsedTransaction      = PrefixTxAPIV1 + "/get_parsed_transaction"
	PathGetVertexWithDependencies = PrefixTxAPIV1 + "/get_vertex_dep"
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

	TxBytes struct {
		TxBytes    string                                  `json:"tx_bytes"`
		TxMetadata *txmetadata.TransactionMetadataJSONAble `json:"tx_metadata,omitempty"`
	}

	Bytecode struct {
		Bytecode string `json:"bytecode"`
	}

	ScriptSource struct {
		Source string `json:"source"`
	}

	ParsedOutput struct {
		// raw hex-encoded output data
		Data string `json:"data"`
		// parsed constraints for display
		Constraints []string `json:"constraints"`
		// amount
		Amount uint64 `json:"amount"`
		// Chain ID for chain outputs
		ChainID string `json:"chain_id,omitempty"`
	}

	Input struct {
		OutputID   string `json:"output_id"`
		UnlockData string `json:"unlock_data"`
	}

	MilestoneData struct {
		Name         string `json:"name"`
		MinimumFee   uint64 `json:"minimum_fee"`
		ChainHeight  uint32 `json:"chain_height"`
		BranchHeight uint32 `json:"branch_height"`
	}

	SequencerTxData struct {
		SequencerID          string `json:"sequencer_id"`
		SequencerOutputIndex byte   `json:"sequencer_output_index"`
		StemOutputIndex      *byte  `json:"stem_output_index,omitempty"` // nil for non-branch transaction
		*MilestoneData       `json:"milestone_data,omitempty"`
	}

	// TransactionJSONAble is more or less human-readable form of the transaction. Intended mostly for display
	// It is not a canonical form. The canonical form is binary blob. It cannot be reproduced from the TransactionJSONAble
	TransactionJSONAble struct {
		// hex-encoded transaction ID
		ID               string `json:"id"`
		TotalAmount      uint64 `json:"total_amount"`
		TotalInflation   uint64 `json:"total_inflation"`
		IsBranch         bool   `json:"is_branch"`
		*SequencerTxData `json:"sequencer_tx_data,omitempty"`
		Sender           string                                  `json:"sender"`
		Signature        string                                  `json:"signature"`
		Inputs           []Input                                 `json:"inputs"`
		Outputs          []ParsedOutput                          `json:"outputs"`
		Endorsements     []string                                `json:"endorsements,omitempty"`
		TxMetadata       *txmetadata.TransactionMetadataJSONAble `json:"tx_metadata,omitempty"`
	}

	// VertexWithDependencies primary purpose is streaming vertices for DAG visualization

	InputDependency struct {
		ID               string `json:"id"`
		IsStem           bool   `json:"is_stem,omitempty"`
		IsSeqPredecessor bool   `json:"is_seq_predecessor,omitempty"`
	}

	VertexWithDependencies struct {
		ID                  string   `json:"id"`
		TotalAmount         uint64   `json:"total_amount"`
		TotalInflation      uint64   `json:"total_inflation"`
		IsSequencerTx       bool     `json:"is_sequencer_tx"`
		IsBranch            bool     `json:"is_branch"`
		SequencerID         string   `json:"sequencer_id,omitempty"`
		SequencerInputIndex *byte    `json:"sequencer_input_index,omitempty"`
		StemInputIndex      *byte    `json:"stem_input_index,omitempty"`
		InputDependencies   []string `json:"input_dependencies"`
		Endorsements        []string `json:"endorsements,omitempty"`
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

func JSONAbleFromTransaction(tx *transaction.Transaction) *TransactionJSONAble {
	ret := &TransactionJSONAble{
		ID:             tx.IDStringHex(),
		Inputs:         make([]Input, tx.NumInputs()),
		Outputs:        make([]ParsedOutput, tx.NumProducedOutputs()),
		Endorsements:   make([]string, tx.NumEndorsements()),
		TotalAmount:    tx.TotalAmount(),
		TotalInflation: tx.InflationAmount(),
		IsBranch:       tx.IsBranchTransaction(),
	}

	if seqData := tx.SequencerTransactionData(); seqData != nil {
		ret.SequencerTxData = &SequencerTxData{
			SequencerID:          seqData.SequencerID.StringHex(),
			SequencerOutputIndex: seqData.SequencerOutputIndex,
		}
		if tx.IsBranchTransaction() {
			ret.SequencerTxData.StemOutputIndex = util.Ref(seqData.StemOutputIndex)
		}
		if md := seqData.SequencerOutputData.MilestoneData; md != nil {
			ret.SequencerTxData.MilestoneData = &MilestoneData{
				Name:         md.Name,
				MinimumFee:   md.MinimumFee,
				ChainHeight:  md.ChainHeight,
				BranchHeight: md.BranchHeight,
			}
		}
	}

	tx.ForEachEndorsement(func(i byte, txid *ledger.TransactionID) bool {
		ret.Endorsements[i] = txid.StringHex()
		return true
	})

	tx.ForEachInput(func(i byte, oid *ledger.OutputID) bool {
		ret.Inputs[i] = Input{
			OutputID:   oid.StringHex(),
			UnlockData: hex.EncodeToString(tx.MustUnlockDataAt(i)),
		}
		return true
	})

	tx.ForEachProducedOutput(func(i byte, o *ledger.Output, oid *ledger.OutputID) bool {
		ret.Outputs[i] = ParsedOutput{
			Data:        hex.EncodeToString(o.Bytes()),
			Constraints: o.LinesPlain().Slice(),
			Amount:      o.Amount(),
		}
		if cc, idx := o.ChainConstraint(); idx != 0xff {
			var chainID ledger.ChainID
			if cc.IsOrigin() {
				chainID = ledger.MakeOriginChainID(oid)
			} else {
				chainID = cc.ID
			}
			ret.Outputs[i].ChainID = chainID.StringHex()
		}
		return true
	})
	ret.Sender = tx.SenderAddress().String()
	ret.Signature = hex.EncodeToString(tx.SignatureBytes())
	return ret
}

func VertexWithDependenciesFromTransaction(tx *transaction.Transaction) *VertexWithDependencies {
	ret := &VertexWithDependencies{
		ID:                tx.IDStringHex(),
		TotalAmount:       tx.TotalAmount(),
		TotalInflation:    tx.InflationAmount(),
		IsSequencerTx:     tx.IsSequencerMilestone(),
		IsBranch:          tx.IsBranchTransaction(),
		InputDependencies: make([]string, tx.NumInputs()),
		Endorsements:      make([]string, tx.NumEndorsements()),
	}
	seqInputIdx, stemInput := tx.SequencerAndStemInputData()

	if tx.IsSequencerMilestone() {
		ret.SequencerInputIndex = seqInputIdx
	}

	tx.ForEachInput(func(i byte, oid *ledger.OutputID) bool {
		ret.InputDependencies[i] = oid.StringHex()
		if stemInput != nil && *stemInput == *oid {
			ret.StemInputIndex = util.Ref(i)
		}
		return true
	})

	tx.ForEachEndorsement(func(i byte, txid *ledger.TransactionID) bool {
		ret.Endorsements[i] = txid.StringHex()
		return true
	})
	return ret
}
