package api

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/core/core_modules/tippool"
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

	PathGetLedgerDefinition              = PrefixAPIV1 + "/get_ledger_definition"
	PathGetLedgerConstants               = PrefixAPIV1 + "/ledger_constants"
	PathGetLedgerTime                    = PrefixAPIV1 + "/get_ledger_time"
	PathEval                             = PrefixAPIV1 + "/eval"
	PathGetOutputs                       = PrefixAPIV1 + "/get_outputs"
	PathGetChainOutput                   = PrefixAPIV1 + "/get_chain_output"
	PathGetOutput                        = PrefixAPIV1 + "/get_output"
	PathSubmitTransaction                = PrefixAPIV1 + "/submit_tx"
	PathGetSyncInfo                      = PrefixAPIV1 + "/sync_info"
	PathGetNodeInfo                      = PrefixAPIV1 + "/node_info"
	PathGetPeersInfo                     = PrefixAPIV1 + "/peers_info"
	PathGetConnectivityMap               = PrefixAPIV1 + "/get_connectivity_map"
	PathGetConnectivityMatrix            = PrefixAPIV1 + "/get_connectivity_matrix"
	PathGetLatestReliableBranch          = PrefixAPIV1 + "/get_latest_reliable_branch"
	PathGetSnapshotBranchID              = PrefixAPIV1 + "/get_snapshot_branch_id"
	PathGetSnapshot                      = PrefixAPIV1 + "/get_snapshot"
	PathCheckTxIDInLRB                   = PrefixAPIV1 + "/check_txid_in_lrb"
	PathGetLastKnownSequencerMilestones  = PrefixAPIV1 + "/last_known_milestones"
	PathGetMainChain                     = PrefixAPIV1 + "/get_mainchain"
	PathGetAllChains                     = PrefixAPIV1 + "/get_all_chains"
	PathGetSequencers                    = PrefixAPIV1 + "/get_sequencers"
	PathGetSequencerTargetInfo           = PrefixAPIV1 + "/get_sequencer_target_info"
	PathGetInactive                      = PrefixAPIV1 + "/get_inactive"
	PathGetBranchList                    = PrefixAPIV1 + "/get_branch_list"
	PathGetSnapshotInfo                  = PrefixAPIV1 + "/get_snapshot_info"

	// get_outputs parameter values
	GetOutputsLockTypeAll            = "all"
	GetOutputsLockTypeSigLock        = "sigLock"
	GetOutputsLockTypeChainLock      = "chainLock"
	GetOutputsLockTypeTagAlongMaster = "tagAlongMaster"
	GetOutputsLockTypeTagAlongTarget = "tagAlongTarget"
	GetOutputsLockTypeDelegateMaster = "delegateMaster"
	GetOutputsLockTypeDelegateTarget = "delegateTarget"

	GetOutputsSortByTimestamp = "timestamp"
	GetOutputsSortByAmount    = "amount"

	GetOutputsSortOrderAsc  = "asc"
	GetOutputsSortOrderDesc = "desc"

	GetOutputsDefaultMaxOutputs = 200
	GetOutputsIterationCap      = 2000
	// PathGetDashboard returns dashboard
	PathGetDashboard = "/dashboard"
	// PathGetPeersDashboard returns the peers dashboard (auto-refreshing peer info page)
	PathGetPeersDashboard = "/peers"
	// PathDAGViz serves the live MemDAG visualizer
	PathDAGViz = "/dagviz"
	// PathNetviz serves the network connectivity visualizer (force-directed graph
	// over /api/v1/get_connectivity_matrix)
	PathNetviz = "/netviz"
	// PathDAGExplorer serves the static DAG explorer page (browses the txstore DB)
	PathDAGExplorer            = "/dag_explorer"
	PathDAGExplorerPastCone    = PrefixAPIV1 + "/dag_explorer/past_cone"
	PathDAGExplorerSlot        = PrefixAPIV1 + "/dag_explorer/slot"
	PathDAGExplorerFindTx      = PrefixAPIV1 + "/dag_explorer/find_tx"
	PathDAGExplorerTxDetail    = PrefixAPIV1 + "/dag_explorer/tx_detail"

	// PathChainExplorer serves the static chain explorer page (browses chained accounts in the LRB)
	PathChainExplorer     = "/chain_explorer"
	PathChainExplorerList = PrefixAPIV1 + "/chain_explorer/list"
	PathChainExplorerUTXO = PrefixAPIV1 + "/chain_explorer/utxo"

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

	// Transaction Logger API
	PathTxLogEnable = PrefixAPIV1 + "/txlog/enable"
	PathTxLogGet    = PrefixAPIV1 + "/txlog/get"
	PathTxLogRange  = PrefixAPIV1 + "/txlog/range"
	PathTxLogStatus = PrefixAPIV1 + "/txlog/status"
)

// Submit-stage values for SubmitTxResponse.Stage. Distinguishes which
// step failed so wallet UIs can render appropriate feedback.
const (
	SubmitStageParse  = "parse"  // ParseWithPartialValidation (tuple/txID/scan/signature/partial-context invariants)
	SubmitStageFull   = "full"   // SetFullContext / ValidateFullContext (input commitment, scripts, ledger invariant)
	SubmitStageSubmit = "submit" // enqueue into txinput_queue
)

type (
	Error struct {
		// empty string when no error
		Error string `json:"error,omitempty"`
	}

	// EvalRequest is the JSON body of POST /api/v1/eval.
	//
	// `slot` selects the library version (0 / omitted → MaxSlot, i.e.
	// "latest at request time"). `sources` is a list of CLOSED EasyFL
	// formulas (no `$0`/`$1` args); each is evaluated independently.
	// The response carries `results` in the same order; per-formula
	// failures live in EvalResult.Error rather than failing the batch.
	EvalRequest struct {
		Slot    uint32   `json:"slot,omitempty"`
		Sources []string `json:"sources"`
	}

	// EvalResult is one entry in EvalResponse.Results.
	//   Value: hex string of the evaluation result bytes, no "0x"
	//          prefix; empty when the formula failed.
	//   Error: server-side eval/compile error message; mutually
	//          exclusive with Value.
	EvalResult struct {
		Value string `json:"value,omitempty"`
		Error string `json:"error,omitempty"`
	}

	// EvalResponse is the JSON body of the eval response.
	EvalResponse struct {
		Error
		Results []EvalResult `json:"results,omitempty"`
	}

	// SubmitTxRequest is the JSON body of POST /api/v1/submit_tx.
	//   tx_bytes        required, hex-encoded raw transaction wire-bytes.
	//   consumed_utxos  optional; hex-encoded raw output bytes ordered to
	//                   match the tx's InputIDs[i]. When non-empty,
	//                   enables full-context validation.
	//   validate_only   optional; when true, the handler runs validation
	//                   stages and skips enqueueing into the workflow.
	SubmitTxRequest struct {
		TxBytes       string   `json:"tx_bytes"`
		ConsumedUTXOs []string `json:"consumed_utxos,omitempty"`
		ValidateOnly  bool     `json:"validate_only,omitempty"`
	}

	// SubmitTxResponse is the JSON response of POST /api/v1/submit_tx.
	// On success: OK=true, TxID is the hex-encoded transaction ID.
	// On failure: OK=false, Stage is one of SubmitStage* constants,
	// Error is the failure message.
	SubmitTxResponse struct {
		OK    bool   `json:"ok"`
		TxID  string `json:"tx_id,omitempty"`
		Stage string `json:"stage,omitempty"`
		Error string `json:"error,omitempty"`
	}

	OutputDataWithID struct {
		// hex-encoded outputID
		ID string `json:"id,omitempty"`
		// hex-encoded output data
		Data string `json:"data,omitempty"`
	}

	// GetOutputsResponse is returned by 'get_outputs'. Wire format is
	// raw: each Outputs entry carries hex-encoded OutputID and hex-
	// encoded output bytes. The Go API client parses Outputs into
	// []ledger.OutputWithID. See claude/get_outputs.md.
	GetOutputsResponse struct {
		Error
		Outputs []OutputDataWithID `json:"outputs,omitempty"`
		// Sum of all amounts in the (possibly capped) filtered set,
		// before truncation to max_outputs. When LimitExceeded is
		// true, this is a partial view.
		AvailableAmount uint64 `json:"available_amount,omitempty"`
		// LimitExceeded is true when the server-side iteration cap
		// (GetOutputsIterationCap) was hit before the lookup completed.
		LimitExceeded bool   `json:"limit_exceeded,omitempty"`
		LRBID         string `json:"lrbid,omitempty"`
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
		HostID string     `json:"host_id"`
		Peers  []PeerInfo `json:"peers,omitempty"`
	}

	PeerInfo struct {
		// The libp2p identifier of the peer.
		ID string `json:"id"`
		// The libp2p multi addresses of the peer.
		MultiAddresses  []string `json:"multiAddresses,omitempty"`
		IsStatic        bool     `json:"is_static"`
		IsAlive         bool     `json:"is_alive"`
		WhenAdded       int64    `json:"when_added"`
		NumIncomingPull int      `json:"num_incoming_pull"`
		NumIncomingTx   int      `json:"num_incoming_tx"`
		// RTTMs is the most recent round-trip ping time in milliseconds.
		// Omitted if no measurement has been taken yet.
		RTTMs float64 `json:"rtt_ms,omitempty"`
	}

	// ConnectivityMap is returned by /get_connectivity_map: the whole network
	// adjacency this node has collected via the lppConnectivity overlay. Masked
	// names are raw 16-hex handles (blake2b of IP:port); formatting is the UI's
	// job. See claude/network_connectivity.md.
	ConnectivityMap struct {
		Error
		Self       string               `json:"self"`        // this node's own masked name ("" if not known yet)
		CapturedAt int64                `json:"captured_at"` // unix nanos, server clock at response time
		Records    []ConnectivityRecord `json:"records"`
	}

	ConnectivityRecord struct {
		Name                  string            `json:"name"`                            // origin masked name, 16-hex
		ConsensusContribution uint64            `json:"consensusContribution,omitempty"` // sequencer mass; 0/omitted for access nodes
		ByPeer                map[string]uint64 `json:"byPeer"`                          // peer masked name (16-hex) -> RTT microseconds
		Timestamp             int64             `json:"timestamp"`                       // origin emit time, unix nanos
		Seq                   uint64            `json:"seq"`                             // origin-monotone sequence number
		AgeMs                 int64             `json:"age_ms"`                          // captured_at - whenReceived
	}

	// ConnectivityMatrix is returned by /get_connectivity_matrix: the symmetric
	// pairwise effective-distance metric d(i,j) (microseconds) derived from the
	// connectivity map. Direction disagreement (a->b vs b->a RTT) is reconciled by
	// averaging; missing/violating pairs are filled by shortest-path metric closure
	// so the triangle inequality holds. Served as the packed upper triangle.
	// Computed server-side and cached (lazily refreshed). See claude/network_rtt_mapping.md.
	ConnectivityMatrix struct {
		Error
		Self         string   `json:"self"`        // this node's own masked name ("" if not known yet)
		CapturedAt   int64    `json:"captured_at"` // unix nanos when this matrix was computed
		Nodes        []string `json:"nodes"`       // masked names, index = matrix index (sorted, stable)
		Contribution []uint64 `json:"contribution"` // parallel to Nodes: sequencer mass per node (0 for access nodes)
		// Matrix is the upper triangle: Matrix[i][k] = d(Nodes[i], Nodes[i+1+k]) in
		// microseconds. Row i has len(Nodes)-1-i entries; diagonal is 0; a 0 entry
		// off-diagonal means no path (disconnected component).
		Matrix [][]uint64 `json:"matrix"`
	}

	// LatestReliableBranch returned by get_latest_reliable_branch
	LatestReliableBranch struct {
		Error
		// BranchData carries Root + SequencerID + the stem-projected aggregates
		// (Supply, CoverageDelta, etc.). After the metadata-refactor (§5),
		// these aggregates live on the stem output rather than RootRecord.
		BranchData multistate.BranchDataJSONAble `json:"branch_data,omitempty"`
		BranchID   base.TransactionID            `json:"branch_id,omitempty"`
	}

	CheckTxIDInLRB struct {
		Error
		TxID         string `json:"txid"`
		LRBID        string `json:"lrbid"`
		FoundAtDepth int    `json:"found_at_depth"`
	}

	TxBytes struct {
		TxBytes string `json:"tx_bytes"`
	}

	Bytecode struct {
		Bytecode string `json:"bytecode"`
	}

	ScriptSource struct {
		Source string `json:"source"`
	}

	// ParsedOutput is the per-output shape returned by the txapi
	// parse endpoints (parse_output, parse_output_data) and embedded
	// in TransactionJSONAble.Outputs. Not used by the legacy state-
	// query endpoints (those were retired with the get_outputs
	// rollout).
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

	Input struct {
		OutputID   string `json:"output_id"`
		UnlockData string `json:"unlock_data"`
	}

	MilestoneData struct {
		Name             string `json:"name"`
		MinimumFee       uint64 `json:"minimum_fee"`
		TransitionCounter uint64 `json:"transition_counter"`
		BranchCounter    uint32 `json:"branch_counter"`
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
		Signature        string                                  `json:"signature"`
		Inputs           []Input                                 `json:"inputs"`
		Outputs          []ParsedOutput                          `json:"outputs"`
		Endorsements []string `json:"endorsements,omitempty"`
	}

	// VertexWithDependencies primary purpose is streaming vertices for DAG visualization
	VertexWithDependencies struct {
		ID                    string   `json:"id"`                          // transaction ID in hex form
		TotalAmount           uint64   `json:"a"`                           // total produced amount on transaction
		TotalInflation        uint64   `json:"i,omitempty"`                 // total inflation on transaction
		SequencerID           string   `json:"seqid,omitempty"`             // "" (omitted) for non-seq. Useful for coloring
		SeqName               string   `json:"seqname,omitempty"`           // sequencer name from on-chain data
		NumEndorsements       int      `json:"num_endorse,omitempty"`       // number of endorsements
		HolderID              string   `json:"holder,omitempty"`            // holder ID hex (for non-seq vertical placement)
		CoverageDelta         *uint64  `json:"cd,omitempty"`                // coverage delta (sequencer txs only)
		Supply                *uint64  `json:"supply,omitempty"`            // total supply (sequencer txs only)
		SequencerInputTxIndex *byte    `json:"seqidx,omitempty"`            // sequencer predecessor tx index
		StemInputTxIndex      *byte    `json:"stemidx,omitempty"`           // stem predecessor tx index
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

	SnapshotID struct {
		Error
		ID string `json:"id"`
	}

	// SnapshotInfo is returned by get_snapshot_info: metadata about the latest snapshot
	SnapshotInfo struct {
		Error
		Slot     uint32 `json:"slot"`
		FileSize int64  `json:"file_size"`
		FileName string `json:"file_name"`
	}
	MainChain struct {
		Error
		Branches []BranchData `json:"branches"`
	}

	// BranchList is returned by get_branch_list: branch IDs on the main chain
	// forward from a given slot, used by the sync module
	BranchList struct {
		Error
		Branches []string `json:"branches"`
		LRBSlot  uint32   `json:"lrb_slot"`
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

	SequencerData struct {
		OutputDataWithID
		NumDelegations int `json:"num_delegations"`
	}

	Sequencers struct {
		Error
		LRBID      string                   `json:"lrbid"`
		OutputData map[string]SequencerData `json:"sequencers"`
	}

	InactiveUTXOs struct {
		Error
		LRBID     string         `json:"lrbid"`
		SinceSlot uint32         `json:"since_slot"`
		UTXOs     []UTXOWithLock `json:"utxos"`
	}

	UTXOWithLock struct {
		ID           string `json:"id"`
		Lock         string `json:"lock"`
		Amount       uint64 `json:"amount"`
		OutputString string `json:"output_string"`
	}

	// LedgerDefinition is returned by 'get_ledger_definition'
	// Contains the library JSON and upgrade UTXO chain data for a specific slot
	// LedgerTimeNow is returned by 'get_ledger_time': the node's current
	// ledger time. A wallet uses (Slot, Tick) directly as the transaction
	// timestamp instead of reconstructing it from ledger_constants +
	// wall-clock. Time is the 5-byte ledger-time wire form, hex-encoded.
	LedgerTimeNow struct {
		Error
		Slot uint32 `json:"slot"`
		Tick byte   `json:"tick"`
		Time string `json:"time"`
	}

	LedgerDefinition struct {
		Error
		// UpgradeSlot is the upgrade slot this definition applies to
		UpgradeSlot uint32 `json:"upgrade_slot"`
		// LibraryJSON is the compiled library serialized as JSON (UTF-8 text)
		LibraryJSON string `json:"library_json"`
		// LibraryHash is the hex-encoded hash of the library
		LibraryHash string `json:"library_hash"`
		// PrevLibraryHash is the hex-encoded hash of the previous library
		// For slot 0, this is the EasyFL base library hash
		PrevLibraryHash string `json:"prev_library_hash"`
		// PrevUpgradeSlot is the slot of the previous upgrade
		// For slot 0, this is MaxSlot (sentinel for base library)
		PrevUpgradeSlot uint32 `json:"prev_upgrade_slot"`
	}

	// TxLogRecord is a single transaction log record for API responses
	TxLogRecord struct {
		TxID           string `json:"txid"`            // hex-encoded full TransactionID (32 bytes)
		ClockTimestamp int64  `json:"clock_timestamp"` // Unix nanoseconds
		Message        string `json:"message"`
	}

	// TxLogResponse is returned by txlog/get and txlog/range endpoints
	TxLogResponse struct {
		Error
		Records []TxLogRecord `json:"records,omitempty"`
	}

	// TxLogEnableResponse is returned by txlog/enable endpoint
	TxLogEnableResponse struct {
		Error
		Enabled bool   `json:"enabled"`
		Level   string `json:"level"`
	}

	// SequencerTargetInfo is returned by 'get_sequencer_target_info'.
	// Contains comprehensive information about a sequencer for delegators.
	// Only primary data is stored; derived values (e.g. AvailableForAdvance = TokenBalance - StorageDeposit)
	// should be computed by the consumer.
	SequencerTargetInfo struct {
		Error
		LRBID string `json:"lrbid"`

		// Identity & chain
		SequencerID       string `json:"sequencer_id"`
		Name              string `json:"name,omitempty"`
		OriginSlot        uint32 `json:"origin_slot"`
		CurrentOutputSlot uint32 `json:"current_output_slot"`
		TransitionCounter uint64 `json:"transition_counter"`
		BranchCounter     uint32 `json:"branch_counter"`

		// Balances
		TokenBalance             uint64  `json:"token_balance"`
		StorageDeposit           uint64  `json:"storage_deposit"`
		FrozenCoverage           []int64 `json:"frozen_coverage"`
		CumulativeChainInflation uint64  `json:"cumulative_chain_inflation"`
		CumulativeBranchBonus    uint64  `json:"cumulative_branch_bonus"`
		// CoverageDelta of this sequencer's latest milestone (sequencer constraint).
		CoverageDelta uint64 `json:"coverage_delta"`

		// Sequencer parameters
		MinimumFee        uint64 `json:"minimum_fee"`
		ProfitMarginPml   uint16 `json:"profit_margin_promille"`
		Greedy            bool   `json:"greedy"`
		Pace              byte   `json:"pace"`
		IgnoreFreezeBound bool   `json:"ignore_freeze_bound"`

		// Delegation info
		NowSlot               uint32 `json:"now_slot"`
		CurrentEpoch          uint32 `json:"current_epoch"`
		NextEpochBoundarySlot uint32 `json:"next_epoch_boundary_slot"`
		MaxFrozenEpochs       uint32 `json:"max_frozen_epochs"`
		EpochDurationSlots    uint32 `json:"epoch_duration_slots"`
		CoverageLowerBound    uint64 `json:"coverage_lower_bound"`
		CoverageUpperBound    uint64 `json:"coverage_upper_bound"`
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
				Name:              md.Name(),
				MinimumFee:        md.MinimumFee(),
				TransitionCounter: seqData.SequencerOutputData.ChainConstraint.TransitionCounter,
				BranchCounter:     seqData.SequencerOutputData.ChainConstraint.BranchCounter,
			}
		}
	}

	tx.ForEachEndorsement(func(i byte, txid base.TransactionID) bool {
		ret.Endorsements[i] = txid.StringHex()
		return true
	})

	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
		ret.Inputs[i] = Input{
			OutputID:   oid.StringHex(),
			UnlockData: hex.EncodeToString(tx.MustUnlockDataAt(i)),
		}
		return true
	})

	tx.ForEachProducedOutput(func(i byte, o *ledger.Output, oid base.OutputID) bool {
		ret.Outputs[i] = ParsedOutput{
			Data:        hex.EncodeToString(o.Bytes()),
			Constraints: o.LinesPlainSource().Slice(),
			Amount:      o.TokenBalance(),
			LockName:    o.Lock().Name(),
		}
		if cc := o.ChainConstraint(); cc != nil {
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
	sig, err := tx.Signature()
	if err == nil {
		ret.Signature = sig.String()
	} else {
		ret.Signature = err.Error()
	}
	return ret
}

func VertexWithDependenciesFromTransaction(tx *transaction.Transaction) *VertexWithDependencies {
	return vertexWithDepsFromTx(tx, nil, nil, "")
}

func VertexWithDependenciesExtended(tx *transaction.Transaction, coverageDelta, supply *uint64, seqName string) *VertexWithDependencies {
	return vertexWithDepsFromTx(tx, coverageDelta, supply, seqName)
}

func vertexWithDepsFromTx(tx *transaction.Transaction, coverageDelta, supply *uint64, seqName string) *VertexWithDependencies {
	ret := &VertexWithDependencies{
		ID:              tx.IDStringHex(),
		TotalAmount:     tx.TotalAmount(),
		TotalInflation:  tx.InflationAmount(),
		SeqName:         seqName,
		NumEndorsements: tx.NumEndorsements(),
		CoverageDelta:   coverageDelta,
		Supply:          supply,
		Inputs:           make([]string, 0),
		Endorsements:     make([]string, tx.NumEndorsements()),
	}
	if holderID, err := tx.HolderID(); err == nil {
		ret.HolderID = hex.EncodeToString(holderID[:])
	} else {
		ret.HolderID = err.Error()
	}
	seqInputIdx, stemInputIdx, seqID := tx.SequencerAndStemInputData()

	if seqID != nil {
		ret.SequencerID = seqID.StringHex()
	}

	var stemTxID, seqTxID base.TransactionID

	inputTxIDs := set.New[base.TransactionID]()
	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
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
