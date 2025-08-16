package api

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

const (
	PrefixAPIV1       = "/api/v1"
	PrefixTxAPIV1     = "/txapi/v1"
	PrefixWebSocketV1 = "/wsapi/v1"

	PathGetLedgerIDData                  = PrefixAPIV1 + "/get_ledger_id_data"
	PathGetAccountOutputs                = PrefixAPIV1 + "/get_account_outputs"
	PathGetAccountParsedOutputs          = PrefixAPIV1 + "/get_account_parsed_outputs"
	PathGetAccountSimpleSiglockedOutputs = PrefixAPIV1 + "/get_account_simple_siglocked"
	PathGetOutputsForAmount              = PrefixAPIV1 + "/get_outputs_for_amount"
	PathGetNonChainBalance               = PrefixAPIV1 + "/get_nonchain_balance"
	PathGetChainedOutputs                = PrefixAPIV1 + "/get_chain_outputs"
	PathGetChainOutput                   = PrefixAPIV1 + "/get_chain_output"
	PathGetOutput                        = PrefixAPIV1 + "/get_output"
	PathSubmitTransaction                = PrefixAPIV1 + "/submit_tx"
	PathGetSyncInfo                      = PrefixAPIV1 + "/sync_info"
	PathGetNodeInfo                      = PrefixAPIV1 + "/node_info"
	PathGetPeersInfo                     = PrefixAPIV1 + "/peers_info"
	PathGetLatestReliableBranch          = PrefixAPIV1 + "/get_latest_reliable_branch"
	PathCheckTxIDInLRB                   = PrefixAPIV1 + "/check_txid_in_lrb"
	PathGetLastKnownSequencerMilestones  = PrefixAPIV1 + "/last_known_milestones"
	PathGetMainChain                     = PrefixAPIV1 + "/get_mainchain"
	PathGetAllChains                     = PrefixAPIV1 + "/get_all_chains"
	// PathGetDelegationsBySequencer returns summarized delegation data in the form of DelegationsBySequencer
	PathGetDelegationsBySequencer = PrefixAPIV1 + "/get_delegations_by_sequencer"
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

	// WebSocket API
	PathDAGVertexStream = PrefixWebSocketV1 + "/dag_vertex_stream"
)

type (
	Error struct {
		// empty string when no error
		Error string `json:"error,omitempty"`
	}

	// OutputList is returned by 'get_account_outputs'
	OutputList struct {
		Error
		// key is hex-encoded outputID bytes
		// value is hex-encoded raw output data
		Outputs map[string]string `json:"outputs,omitempty"`
		// latest reliable branch used to extract outputs
		LRBID string `json:"lrbid"`
	}

	OutputDataWithID struct {
		// hex-encoded outputID
		ID string `json:"id"`
		// hex-encoded output data
		Data string `json:"data"`
	}
	// ChainOutput is returned by 'get_chain_output'
	ChainOutput struct {
		Error
		OutputDataWithID
		// latest reliable branch used to extract chain id
		LRBID string `json:"lrbid"`
	}

	Chains struct {
		Error
		Chains map[string]OutputDataWithID `json:"chains"`
		LRBID  string                      `json:"lrbid"`
	}

	// OutputData is returned by 'get_output'
	OutputData struct {
		Error
		// hex-encoded output data
		OutputData string `json:"output_data,omitempty"`
		// latest reliable branch used to extract output
		LRBID string `json:"lrbid"`
	}

	ChainedOutputs struct {
		Error
		Outputs map[string]string `json:"outputs,omitempty"`
		LRBID   string            `json:"lrbid"`
	}

	SyncInfo struct {
		Error
		Synced         bool                         `json:"synced"`
		CurrentSlot    uint32                       `json:"current_slot"`
		LrbSlot        uint32                       `json:"lrb_slot"`
		LedgerCoverage uint64                       `json:"ledger_coverage"`
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
		BranchID base.TransactionID            `json:"branch_id,omitempty"`
	}

	CheckTxIDInLRB struct {
		Error
		TxID         string `json:"txid"`
		LRBID        string `json:"lrbid"`
		FoundAtDepth int    `json:"found_at_depth"`
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
		// name of the lock constraint
		LockName string `json:"lock_name"`
		// Chain id for chain outputs
		ChainID string `json:"chain_id,omitempty"`
	}
	// ParsedOutputList is returned by 'get_account_parsed_outputs'
	ParsedOutputList struct {
		Error
		// key is hex-encoded outputID bytes
		// value is hex-encoded raw output data
		Outputs map[string]ParsedOutput `json:"outputs,omitempty"`
		// latest reliable branch used to extract outputs
		LRBID string `json:"lrbid"`
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
	VertexWithDependencies struct {
		ID                    string   `json:"id"`                          // transaction ID in hex form
		TotalAmount           uint64   `json:"a"`                           // total produced amount on transaction
		TotalInflation        uint64   `json:"i,omitempty"`                 // total inflation on transaction
		SequencerID           string   `json:"seqid,omitempty"`             // "" (omitted) for non-seq. Useful for coloring
		SequencerInputTxIndex *byte    `json:"seqidx,omitempty"`            // sequencer predecessor tx index for sequencer predecessor tx in the Inputs list, otherwise nil
		StemInputTxIndex      *byte    `json:"stemidx,omitempty"`           // stem predecessor (branch) tx index for stem predecessor tx in the Inputs list, otherwise nil
		Inputs                []string `json:"in"`                          // list of input IDs (not empty)
		Endorsements          []string `json:"endorse,omitempty"`           // list of endorsements (can be nil)
		ExplicitBaseline      string   `json:"explicit_baseline,omitempty"` // explicit baseline ID, if available
	}

	VertexDelete struct {
		ID string `json:"id"` // transaction ID in hex form
	}

	KnownLatestMilestones struct {
		Error
		Sequencers map[string]tippool.LatestSequencerTipDataJSONAble `json:"sequencers"`
	}

	BranchData struct {
		ID   string                        `json:"id"`
		Data multistate.BranchDataJSONAble `json:"data"`
	}

	MainChain struct {
		Error
		Branches []BranchData `json:"branches"`
	}

	Balance struct {
		Error
		Amount uint64 `json:"amount"`
		LRBID  string `json:"lrbid"`
	}

	DelegationData struct {
		Amount      uint64 `json:"amount"`
		SinceSlot   uint32 `json:"since_slot"`
		StartAmount uint64 `json:"start_amount"`
	}
	DelegationsOnSequencer struct {
		SequencerOutputID string                    `json:"seq_output_id"`
		SequencerName     string                    `json:"seq_name"`
		Balance           uint64                    `json:"balance"`
		Delegations       map[string]DelegationData `json:"delegations"`
	}

	DelegationsBySequencer struct {
		Error
		LRBID      string                            `json:"lrbid"`
		Sequencers map[string]DelegationsOnSequencer `json:"sequencers"`
	}
)

const ErrGetOutputNotFound = "output not found"

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
		if md := seqData.SequencerOutputData.SequencerData; md != nil {
			ret.SequencerTxData.MilestoneData = &MilestoneData{
				Name:         md.Name(),
				MinimumFee:   md.MinimumFee(),
				ChainHeight:  md.ChainHeight(),
				BranchHeight: md.BranchHeight(),
			}
		}
	}

	tx.ForEachEndorsement(func(i byte, txid base.TransactionID) bool {
		ret.Endorsements[i] = txid.StringHex()
		return true
	})

	tx.ForEachInput(func(i byte, oid base.OutputID) bool {
		ret.Inputs[i] = Input{
			OutputID:   oid.StringHex(),
			UnlockData: hex.EncodeToString(tx.MustUnlockDataAt(i)),
		}
		return true
	})

	tx.ForEachProducedOutput(func(i byte, o *ledger.Output, oid base.OutputID) bool {
		ret.Outputs[i] = ParsedOutput{
			Data:        hex.EncodeToString(o.Bytes()),
			Constraints: o.LinesPlain().Slice(),
			Amount:      o.TokenBalance(),
			LockName:    o.Lock().Name(),
		}
		if cc, idx := o.ChainConstraint(); idx != 0xff {
			var chainID base.ChainID
			if cc.IsOrigin() {
				chainID = base.MakeOriginChainID(oid)
			} else {
				chainID = cc.ChainID
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
		ID:             tx.IDStringHex(),
		TotalAmount:    tx.TotalAmount(),
		TotalInflation: tx.InflationAmount(),
		Inputs:         make([]string, 0),
		Endorsements:   make([]string, tx.NumEndorsements()),
	}
	seqInputIdx, stemInputIdx, seqID := tx.SequencerAndStemInputData()

	if seqID != nil {
		ret.SequencerID = seqID.StringHex()
	}

	var stemTxID, seqTxID base.TransactionID

	inputTxIDs := set.New[base.TransactionID]()
	tx.ForEachInput(func(i byte, oid base.OutputID) bool {
		inputTxIDs.Insert(oid.TransactionID())
		if tx.IsSequencerTransaction() {
			if *seqInputIdx == i {
				seqTxID = oid.TransactionID()
			}
			if tx.IsBranchTransaction() {
				if *stemInputIdx == i {
					stemTxID = oid.TransactionID()
				}
			}
		}
		return true
	})
	sorted := util.KeysSorted(inputTxIDs, func(txid1, txid2 base.TransactionID) bool {
		return base.LessTxID(txid1, txid2)
	})

	if tx.IsSequencerTransaction() {
		for i, txid := range sorted {
			if txid == seqTxID {
				ret.SequencerInputTxIndex = util.Ref(byte(i))
			}
			if tx.IsBranchTransaction() && txid == stemTxID {
				ret.StemInputTxIndex = util.Ref(byte(i))
			}
		}
	}

	for _, txid := range sorted {
		ret.Inputs = append(ret.Inputs, txid.StringHex())
	}

	tx.ForEachEndorsement(func(i byte, txid base.TransactionID) bool {
		ret.Endorsements[i] = txid.StringHex()
		return true
	})

	if etxid, ok := tx.ExplicitBaseline(); ok {
		ret.ExplicitBaseline = etxid.StringHex()
	}

	util.Assertf(!tx.IsSequencerTransaction() || ret.SequencerInputTxIndex != nil, "!tx.IsSequencerTransaction() || ret.SequencerInputTxIndex != nil")
	util.Assertf(!tx.IsBranchTransaction() || ret.StemInputTxIndex != nil, "!tx.IsBranchTransaction() || ret.StemInputTxIndex != nil")
	return ret
}
