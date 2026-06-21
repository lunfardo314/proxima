package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/chain_explorer"
	"github.com/lunfardo314/proxima/api/dag_explorer"
	"github.com/lunfardo314/proxima/api/dagviz"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/exp/slices"
)

type (
	environment interface {
		global.NodeGlobal
		GetNodeInfo() *global.NodeInfo
		GetSyncInfo() *api.SyncInfo
		GetPeersInfo() *api.PeersInfo
		GetConnectivityMap() *api.ConnectivityMap
		GetConnectivityMatrix() *api.ConnectivityMatrix
		LatestReliableState() (multistate.SugaredStateReader, error)
		CheckTransactionInLRB(txid base.TransactionID, maxDepth int) (lrbid base.TransactionID, foundAtDepth int)
		SubmitTxBytesFromAPI(txBytes []byte)
		GetLatestReliableBranch() *multistate.BranchData
		GetSnapshotBranchID() base.TransactionID
		GetSnapshotFilePath() (string, error)
		StateStore() global.Store
		TxBytesStore() global.TxBytesStore
		GetKnownLatestMilestonesJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble
		// TxLogger methods
		TxLogOnOffAPIEnabled() bool
		TxLogEnable(level global.TxLogLevel)
		TxLogGet(txShortIDPrefix []byte, max ...int) ([]global.TxLogRecord, error)
		TxLogIterate(begin time.Time, fun func(rec global.TxLogRecord)) error
		TxLogIsEnabled() bool
		TxLogLevel() global.TxLogLevel
	}

	server struct {
		*http.Server
		environment
		metrics
	}

	metrics struct {
		totalRequests prometheus.Counter
	}
)

const TraceTag = "apiServer"

func (srv *server) registerHandlers() {
	// GET request format: '/api/v1/get_ledger_definition?slot=<slot>' (slot optional, defaults to MaxSlot for latest)
	srv.addHandler(api.PathGetLedgerDefinition, srv.getLedgerDefinition)
	// GET request format: '/api/v1/ledger_constants?slot=<slot>' (slot optional, defaults to MaxSlot for latest)
	srv.addHandler(api.PathGetLedgerConstants, srv.getLedgerConstants)
	// GET '/api/v1/get_ledger_time' returns the node's current ledger time {slot, tick, time}
	srv.addHandler(api.PathGetLedgerTime, srv.getLedgerTime)
	// POST request format: '/api/v1/eval'. JSON body {slot, sources: [closed EasyFL formulas]}.
	// See claude/wallet_eval_api.md.
	srv.addHandler(api.PathEval, srv.eval)
	// Unified state-query endpoint. See claude/get_outputs.md.
	// GET '/api/v1/get_outputs?index_value=<hex>[&max_outputs=N][&sort_by=timestamp|amount][&sort_order=asc|desc][&for_amount=N][&lock_type=all|sigLock|chainLock|tagAlongMaster|tagAlongTarget|delegateMaster|delegateTarget][&chained=true|false]'
	srv.addHandler(api.PathGetOutputs, srv.getOutputs)
	// GET request format: '/api/v1/get_chain_output?chainid=<hex-encoded chain id>'
	srv.addHandler(api.PathGetChainOutput, srv.getChainOutput)
	// GET request format: '/api/v1/get_output?id=<hex-encoded output id>'
	srv.addHandler(api.PathGetOutput, srv.getOutput)
	// POST request format '/api/v1/submit_tx'. Feedback only on parsing error, otherwise async posting
	srv.addHandler(api.PathSubmitTransaction, srv.submitTx)
	// GET sync info from the node '/api/v1/sync_info'
	srv.addHandler(api.PathGetSyncInfo, srv.getSyncInfo)
	// GET node info from the node '/api/v1/node_info'
	srv.addHandler(api.PathGetNodeInfo, srv.getNodeInfo)
	// GET peers info from the node '/api/v1/peers_info'
	srv.addHandler(api.PathGetPeersInfo, srv.getPeersInfo)
	// GET the network connectivity map '/api/v1/get_connectivity_map'
	srv.addHandler(api.PathGetConnectivityMap, srv.getConnectivityMap)
	// GET the derived distance matrix '/api/v1/get_connectivity_matrix'
	srv.addHandler(api.PathGetConnectivityMatrix, srv.getConnectivityMatrix)
	// GET latest reliable branch '/api/v1/get_latest_reliable_branch'
	srv.addHandler(api.PathGetLatestReliableBranch, srv.getLatestReliableBranch)
	// GET latest reliable branch '/api/v1/get_snapshot_branch'
	srv.addHandler(api.PathGetSnapshotBranchID, srv.getSnapshotBranchID)
	// GET latest reliable branch and check if transaction id is in it '/check_txid_in_lrb?txid=<hex-encoded transaction id>[&max_depth=<max depth in LRB>]'
	srv.addHandler(api.PathCheckTxIDInLRB, srv.checkTxIDIncludedInLRB)
	// GET last milestone list
	srv.addHandler(api.PathGetLastKnownSequencerMilestones, srv.getMilestoneList)
	// GET main chain of branches /get_mainchain?[max=]
	srv.addHandler(api.PathGetMainChain, srv.getMainChain)
	// GET all chains in the LRB /get_all_chains
	srv.addHandler(api.PathGetAllChains, srv.getAllChains)
	// GET all sequencer chains in the LRB /get_sequencers
	srv.addHandler(api.PathGetSequencers, srv.getSequencers)
	// GET sequencer target info /get_sequencer_target_info?chainid=<hex-encoded chain id>
	srv.addHandler(api.PathGetSequencerTargetInfo, srv.getSequencerTargetInfo)
	// GET dashboard for node
	srv.addHandler(api.PathGetDashboard, srv.getDashboard)
	// GET peers dashboard (auto-refreshing peer info page)
	srv.addHandler(api.PathGetPeersDashboard, srv.getPeersDashboard)
	// GET live MemDAG visualizer page
	srv.addHandler(api.PathDAGViz, dagviz.Handler)
	// GET network connectivity visualizer (force-directed graph over the distance matrix)
	srv.addHandler(api.PathNetviz, srv.getNetviz)
	// DAG explorer (browses the txstore DB): HTML page + JSON APIs
	if explorerStore, ok := srv.TxBytesStore().(dag_explorer.TxStore); ok {
		dag_explorer.Register(srv.addHandler, explorerStore)
	} else {
		srv.Log().Warnf("DAG explorer not registered: TxBytesStore does not support prefix iteration")
	}
	// Chain explorer (browses chained accounts in the LRB): HTML page + JSON list API
	chain_explorer.Register(srv.addHandler, srv)
	// GET inactive UTXOs in LRB /get_inactive?[slots_back=<slot>]
	srv.addHandler(api.PathGetInactive, srv.getInactive)
	// GET branch's back-chain for forward sync /get_branch_list?to_branch=<hex>&from_slot=<slot>&max=<max>
	srv.addHandler(api.PathGetBranchList, srv.getBranchList)
	// GET snapshot info /get_snapshot_info (slot, size, name)
	srv.addHandler(api.PathGetSnapshotInfo, srv.getSnapshotInfo)
	// GET snapshot file download /get_snapshot (binary, enable with snapshot.enable_download_api)
	srv.addHandler(api.PathGetSnapshot, srv.getSnapshot)

	// Transaction logger API
	// POST /api/v1/txlog/enable?level=<level>
	srv.addHandler(api.PathTxLogEnable, srv.txLogEnable)
	// GET /api/v1/txlog/get?prefix=<hex_prefix>&max=<max>
	srv.addHandler(api.PathTxLogGet, srv.txLogGet)
	// GET /api/v1/txlog/range?from=<unix_ns>&to=<unix_ns>&max=<max>
	srv.addHandler(api.PathTxLogRange, srv.txLogRange)
	// GET /api/v1/txlog/status
	srv.addHandler(api.PathTxLogStatus, srv.txLogStatus)

	// register handlers of tx API
	srv.registerTxAPIHandlers()
}

func (srv *server) getLedgerDefinition(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)
	srv.Tracef(TraceTag, "getLedgerDefinition invoked")

	// Parse optional slot parameter (default to MaxSlot for latest)
	var slot uint32 = base.MaxSlot
	if slotParam := r.URL.Query().Get("slot"); slotParam != "" {
		slotVal, err := strconv.ParseUint(slotParam, 10, 32)
		if err != nil {
			api.WriteErr(w, "invalid slot parameter: must be non-negative 32-bit integer")
			return
		}
		slot = uint32(slotVal)
	}

	// Get the library for the requested slot - always succeeds
	lib := ledger.L(slot)
	chainData := lib.UpgradeChainData()

	resp := api.LedgerDefinition{
		UpgradeSlot:     chainData.UpgradeSlot,
		LibraryJSON:     string(lib.DefinitionsJSON()),
		LibraryHash:     hex.EncodeToString(chainData.LibraryHash[:]),
		PrevLibraryHash: hex.EncodeToString(chainData.PrevLibraryHash[:]),
		PrevUpgradeSlot: chainData.PrevUpgradeSlot,
	}

	respBytes, err := json.Marshal(&resp)
	if err != nil {
		api.WriteErr(w, fmt.Sprintf("failed to marshal response: %v", err))
		return
	}
	if _, err = w.Write(respBytes); err != nil {
		srv.Log().Warnf("getLedgerDefinition: failed to write response: %v", err)
	}
}

// getLedgerConstants returns the runtime ledger constants extracted
// from the library active at the given slot (default MaxSlot). The
// response body is the JSON-marshalled *txbuildercore.Constants;
// see claude/wallet_eval_api.md.
func (srv *server) getLedgerConstants(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	var slot uint32 = base.MaxSlot
	if slotParam := r.URL.Query().Get("slot"); slotParam != "" {
		slotVal, err := strconv.ParseUint(slotParam, 10, 32)
		if err != nil {
			api.WriteErr(w, "invalid slot parameter: must be non-negative 32-bit integer")
			return
		}
		slot = uint32(slotVal)
	}

	walletConsts := ledger.L(slot).Constants
	respBytes, err := json.Marshal(walletConsts)
	if err != nil {
		api.WriteErr(w, fmt.Sprintf("failed to marshal response: %v", err))
		return
	}
	_, _ = w.Write(respBytes)
}

// getLedgerTime returns the node's current ledger time. A wallet uses
// (slot, tick) directly as the transaction timestamp, avoiding a
// client-side wall-clock-to-ledger-time conversion.
func (srv *server) getLedgerTime(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	t := ledger.TimeNow()
	respBytes, err := json.Marshal(&api.LedgerTimeNow{
		Slot: t.Slot,
		Tick: t.Tick,
		Time: hex.EncodeToString(t.Bytes()),
	})
	if err != nil {
		api.WriteErr(w, fmt.Sprintf("failed to marshal response: %v", err))
		return
	}
	_, _ = w.Write(respBytes)
}

// eval evaluates a batch of CLOSED EasyFL formulas against the library
// active at the requested slot (default MaxSlot). Per-formula failures
// (compile error, eval panic, type error) land in EvalResult.Error;
// the batch as a whole is HTTP 2xx as long as the request itself
// parses. See claude/wallet_eval_api.md.
func (srv *server) eval(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		api.WriteErr(w, fmt.Sprintf("read body: %v", err))
		return
	}
	var req api.EvalRequest
	if err = json.Unmarshal(bodyBytes, &req); err != nil {
		api.WriteErr(w, fmt.Sprintf("bad JSON: %v", err))
		return
	}

	slot := req.Slot
	if slot == 0 {
		slot = base.MaxSlot
	}
	lib := ledger.L(slot)

	results := make([]api.EvalResult, len(req.Sources))
	for i, src := range req.Sources {
		var bin []byte
		evalErr := util.CatchPanicOrError(func() error {
			b, e := lib.EvalFromSource(nil, src)
			if e != nil {
				return e
			}
			bin = b
			return nil
		})
		if evalErr != nil {
			results[i].Error = evalErr.Error()
			continue
		}
		results[i].Value = hex.EncodeToString(bin)
	}

	respBytes, err := json.Marshal(&api.EvalResponse{Results: results})
	if err != nil {
		api.WriteErr(w, fmt.Sprintf("failed to marshal response: %v", err))
		return
	}
	_, _ = w.Write(respBytes)
}

func (srv *server) getChainOutput(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["chainid"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameters in request 'get_chain_output'")
		return
	}
	chainID, err := base.ChainIDFromHexString(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := &api.ChainOutput{}
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		o, err1 := rdr.GetChainOutputWithID(chainID)
		if err1 != nil {
			return err1
		}
		resp.ID = o.ID.StringHex()
		resp.Data = hex.EncodeToString(o.Output.Bytes())
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getOutput(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["id"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter in request 'get_output'")
		return
	}
	oid, err := base.OutputIDFromHexString(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := &api.OutputData{}
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		oData, found := rdr.GetUTXO(oid)
		if !found {
			return errors.New(api.ErrGetOutputNotFound)
		}
		resp.OutputData = hex.EncodeToString(oData)
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return nil
	})
	if err != nil {
		api.WriteErr(w, api.ErrGetOutputNotFound)
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// maxTxUploadSize bounds the JSON request body. Sized to hold a tx
// plus up to 256 consumed_utxos at typical sizes plus JSON overhead.
const maxTxUploadSize = 2 * (1 << 20) // 2 MiB

// writeSubmitResp serialises a SubmitTxResponse onto w. Failures fall
// back to http.Error if JSON marshalling itself fails (should not
// happen for this fixed shape).
func writeSubmitResp(w http.ResponseWriter, resp api.SubmitTxResponse) {
	respBytes, err := json.Marshal(&resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(respBytes)
}

// writeSubmitErr writes a {ok:false, stage, error} response. Always
// HTTP 200 — the failure is reported in the JSON body, consistent
// with the rest of the API.
func writeSubmitErr(w http.ResponseWriter, stage, msg string) {
	writeSubmitResp(w, api.SubmitTxResponse{
		OK:    false,
		Stage: stage,
		Error: msg,
	})
}

// submitTx handles POST /api/v1/submit_tx. Body is a JSON
// SubmitTxRequest. Pipeline (fail-fast):
//
//  1. Parse + partial-context validate (always). On failure:
//     stage="parse".
//  2. Full-context validate (only if consumed_utxos non-empty).
//     The loader is built positionally from consumed_utxos[i] →
//     tx.InputIDs[i]. On failure: stage="full".
//  3. Submit (only if validate_only != true). Calls the existing
//     async SubmitTxBytesFromAPI; success means enqueued. On
//     panic/error: stage="submit".
//
// Success → {ok:true, tx_id:"<hex>"}.
func (srv *server) submitTx(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTxUploadSize)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeSubmitErr(w, api.SubmitStageParse, fmt.Sprintf("read body: %v", err))
		return
	}

	var req api.SubmitTxRequest
	if err = json.Unmarshal(bodyBytes, &req); err != nil {
		writeSubmitErr(w, api.SubmitStageParse, fmt.Sprintf("bad JSON: %v", err))
		return
	}

	txBytes, err := hex.DecodeString(req.TxBytes)
	if err != nil {
		writeSubmitErr(w, api.SubmitStageParse, fmt.Sprintf("tx_bytes hex: %v", err))
		return
	}

	// Stage 1: parse + partial-context validate.
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		writeSubmitErr(w, api.SubmitStageParse, err.Error())
		return
	}

	// Stage 2: full-context validate (opt-in via consumed_utxos).
	if len(req.ConsumedUTXOs) > 0 {
		if len(req.ConsumedUTXOs) != tx.NumInputs() {
			writeSubmitErr(w, api.SubmitStageFull,
				fmt.Sprintf("consumed_utxos length %d does not match tx inputs %d",
					len(req.ConsumedUTXOs), tx.NumInputs()))
			return
		}
		decoded := make([][]byte, len(req.ConsumedUTXOs))
		for i, s := range req.ConsumedUTXOs {
			raw, decErr := hex.DecodeString(s)
			if decErr != nil {
				writeSubmitErr(w, api.SubmitStageFull,
					fmt.Sprintf("consumed_utxos[%d] hex: %v", i, decErr))
				return
			}
			decoded[i] = raw
		}
		loader := func(i byte) ([]byte, error) {
			if int(i) >= len(decoded) {
				return nil, fmt.Errorf("consumed_utxos[%d] missing", i)
			}
			return decoded[i], nil
		}
		if err = tx.SetFullContext(loader); err != nil {
			writeSubmitErr(w, api.SubmitStageFull, err.Error())
			return
		}
		if err = tx.ValidateFullContext(); err != nil {
			writeSubmitErr(w, api.SubmitStageFull, err.Error())
			return
		}
	}

	// Stage 3: submit (async fire-and-forget) unless validate_only.
	if !req.ValidateOnly {
		err = util.CatchPanicOrError(func() error {
			srv.SubmitTxBytesFromAPI(slices.Clip(txBytes))
			return nil
		})
		if err != nil {
			srv.Tracef(TraceTag, "submit transaction: '%v'", err)
			writeSubmitErr(w, api.SubmitStageSubmit, err.Error())
			return
		}
	}

	txID := tx.ID()
	writeSubmitResp(w, api.SubmitTxResponse{
		OK:   true,
		TxID: txID.StringHex(),
	})
}

func (srv *server) getSyncInfo(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	syncInfo := srv.GetSyncInfo()
	respBin, err := json.MarshalIndent(syncInfo, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getPeersInfo(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	peersInfo := srv.GetPeersInfo()
	respBin, err := json.MarshalIndent(peersInfo, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getConnectivityMap(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	respBin, err := json.MarshalIndent(srv.GetConnectivityMap(), "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getConnectivityMatrix(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	respBin, err := json.MarshalIndent(srv.GetConnectivityMatrix(), "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getNodeInfo(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	nodeInfo := srv.GetNodeInfo()
	respBin, err := json.MarshalIndent(nodeInfo, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getMilestoneList(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	resp := api.KnownLatestMilestones{
		Sequencers: srv.GetKnownLatestMilestonesJSONAble(),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

const defaultMaxMainChainDepth = 20

func (srv *server) getMainChain(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	var err error
	maxDepth := defaultMaxMainChainDepth
	lst, ok := r.URL.Query()["max"]
	if ok || len(lst) == 1 {
		if maxDepth, err = strconv.Atoi(lst[0]); err != nil {
			api.WriteErr(w, "wrong parameter 'max'")
			return
		}
	}
	if maxDepth <= 0 {
		maxDepth = 1
	}
	main, err := multistate.GetMainChain(srv.StateStore(), global.FractionHealthyBranch(), maxDepth)
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := api.MainChain{
		Branches: make([]api.BranchData, 0),
	}

	for _, br := range main {
		txid := br.Stem.ID.TransactionID()
		resp.Branches = append(resp.Branches, api.BranchData{
			ID:   txid.StringHex(),
			Data: *br.JSONAble(),
		})
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

const defaultMaxBranchListSize = 100

// getBranchList returns the back-chain (its own lineage) of a specific branch, used by the
// forward-sync module. The syncing node sends the branch its stuck attacher needs; the source
// walks back from THAT branch itself, so the returned chain is guaranteed to be on the requested
// branch's lineage — this is what makes the forward (commit) and recursive (pull) sync waves
// stitch on the same lineage (claude/sync_semantics.md §3-§4).
//
// Parameters:
//   - to_branch=<hex txid> (required): return this branch's ancestry, oldest-first, down to
//     from_slot. Returns error if the source does not know the branch (it is on a fork the
//     source lacks) — the syncing node then tries the next source.
//   - from_slot=<slot>: stop the back-walk at this slot (the requesting node's committed frontier).
//   - max=<n>: cap the number of returned entries (default 100).
func (srv *server) getBranchList(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	maxEntries := defaultMaxBranchListSize
	if lst, ok := r.URL.Query()["max"]; ok && len(lst) == 1 {
		v, err := strconv.Atoi(lst[0])
		if err != nil || v <= 0 {
			api.WriteErr(w, "invalid 'max' parameter")
			return
		}
		maxEntries = v
	}

	var fromSlot uint32
	if lst, ok := r.URL.Query()["from_slot"]; ok && len(lst) == 1 {
		v, err := strconv.Atoi(lst[0])
		if err != nil || v < 0 {
			api.WriteErr(w, "invalid 'from_slot' parameter")
			return
		}
		fromSlot = uint32(v)
	}

	lst, ok := r.URL.Query()["to_branch"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "missing 'to_branch' parameter")
		return
	}
	toBranch, err := base.TransactionIDFromHexString(lst[0])
	if err != nil {
		api.WriteErr(w, "invalid 'to_branch' parameter")
		return
	}
	bd, found := multistate.FetchBranchData(srv.StateStore(), toBranch)
	if !found {
		api.WriteErr(w, "to_branch not known to this source (different fork or not synced)")
		return
	}
	var collected []string
	multistate.IterateBranchChainBack(srv.StateStore(), &bd, func(branchID *base.TransactionID, _ *multistate.BranchData) bool {
		if branchID.Slot() <= fromSlot {
			return false
		}
		collected = append(collected, branchID.StringHex())
		return true
	})

	// reverse to oldest-first order (closest to the requesting node's frontier first) and cap at max
	n := len(collected)
	for i := 0; i < n/2; i++ {
		collected[i], collected[n-1-i] = collected[n-1-i], collected[i]
	}
	if len(collected) > maxEntries {
		collected = collected[:maxEntries]
	}
	resp := api.BranchList{
		Branches: collected,
		TopSlot:  toBranch.Slot(),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getAllChains(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	var lst map[base.ChainID]multistate.ChainRecordInfo
	resp := api.Chains{
		Chains: make(map[string]api.OutputDataWithID),
	}

	err := srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		var err1 error
		lst, err1 = rdr.GetAllChainsOld()
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return err1
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	for chainID, ri := range lst {
		resp.Chains[chainID.StringHex()] = api.OutputDataWithID{
			ID:   ri.Output.ID.StringHex(),
			Data: hex.EncodeToString(ri.Output.Data),
		}
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getSequencers(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	resp := api.Sequencers{
		OutputData: make(map[string]api.SequencerData),
	}

	var err error

	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		var err1 error
		bySeq, err1 := rdr.GetSequencersWithDelegations()
		if err1 != nil {
			return err1
		}
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		for seqID, seqData := range bySeq {
			sd := api.SequencerData{
				NumDelegations: len(seqData.Delegations),
			}
			if seqData.SequencerOutput != nil {
				sd.OutputDataWithID = api.OutputDataWithID{
					ID:   seqData.SequencerOutput.ID.StringHex(),
					Data: seqData.SequencerOutput.Output.Hex(),
				}
			}
			resp.OutputData[seqID.StringHex()] = sd
		}
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
}

func (srv *server) getLatestReliableBranch(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	bd := srv.GetLatestReliableBranch()
	if bd == nil {
		api.WriteErr(w, "latest reliable branch (LRB) has not been found")
		return
	}

	resp := &api.LatestReliableBranch{
		BranchData: *bd.JSONAble(),
		BranchID:   bd.Stem.ID.TransactionID(),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getSnapshotBranchID(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	snapshotID := srv.GetSnapshotBranchID()
	resp := &api.SnapshotID{
		ID: snapshotID.StringHex(),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

const maxReturnInactive = 1000

func (srv *server) getInactive(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	var slotsBack uint32
	if lst, ok := r.URL.Query()["slots_back"]; ok {
		n, err := strconv.Atoi(lst[0])
		if err != nil {
			api.WriteErr(w, err.Error())
			return
		}
		slotsBack = uint32(n)
	} else {
		slotsBack = 360 // one hour by default
	}

	resp := api.InactiveUTXOs{
		UTXOs: make([]api.UTXOWithLock, 0),
	}

	var err, err1 error
	var since uint32
	var outs []ledger.OutputWithID

	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		if lrbid.Slot() > slotsBack {
			since = lrbid.Slot() - slotsBack
		}
		resp.SinceSlot = since
		// TODO incorrect if more than max. Reimplement
		outs, err1 = rdr.ScanInactive(lrbid.Slot(), since, maxReturnInactive)
		if err1 != nil {
			return err1
		}
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	for _, o := range outs {
		resp.UTXOs = append(resp.UTXOs, api.UTXOWithLock{
			ID:           o.ID.StringHex(),
			Lock:         o.Output.Lock().String(),
			Amount:       o.Output.TokenBalance(),
			OutputString: o.String(),
		})
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
}

func (srv *server) checkTxIDIncludedInLRB(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	var txid base.TransactionID
	var err error

	// mandatory parameter txid
	lst, ok := r.URL.Query()["txid"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "txid expected")
		return
	}
	txid, err = base.TransactionIDFromHexString(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	maxDepth := 1 // default max depth is 1
	// optional parameter
	lst, ok = r.URL.Query()["max_depth"]
	if ok && len(lst) == 1 {
		maxDepth, err = strconv.Atoi(lst[0])
		if err != nil {
			api.WriteErr(w, err.Error())
			return
		}
		if maxDepth < 0 {
			// wrong value reset to default
			maxDepth = 1
		}
	}

	lrbid, foundAtDepth := srv.CheckTransactionInLRB(txid, maxDepth)
	resp := api.CheckTxIDInLRB{
		TxID:         txid.StringHex(),
		LRBID:        lrbid.StringHex(),
		FoundAtDepth: foundAtDepth,
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getSequencerTargetInfo(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["chainid"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameters in request 'get_sequencer_target_info': expected ?chainid=<hex>")
		return
	}
	seqID, err := base.ChainIDFromHexString(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := &api.SequencerTargetInfo{}
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		o, err1 := rdr.GetChainOutputWithID(seqID)
		if err1 != nil {
			return err1
		}
		if !o.Output.IsSequencerOutput() {
			return fmt.Errorf("chain %s is not a sequencer", seqID.StringShort())
		}
		seqData, ok := o.Output.SequencerOutputData()
		if !ok {
			return fmt.Errorf("cannot parse sequencer output data for %s", seqID.StringShort())
		}

		nowSlot := ledger.SlotNow()
		lib := ledger.L(nowSlot)
		cc := seqData.ChainConstraint

		resp.SequencerID = seqID.String()
		resp.OriginSlot = cc.OriginSlot
		resp.CurrentOutputSlot = o.ID.Slot()
		resp.TransitionCounter = cc.TransitionCounter
		resp.BranchCounter = cc.BranchCounter

		if seqData.SequencerData != nil {
			sd := seqData.SequencerData
			resp.Name = sd.Name()
			resp.MinimumFee = sd.MinimumFee()
			resp.ProfitMarginPml = sd.InflationProfitMarginPromille()
			resp.Greedy = sd.IsGreedy()
			resp.Pace = sd.Pace()
			resp.IgnoreFreezeBound = sd.IsIgnoreFreezeBound()
		}

		resp.TokenBalance = o.Output.TokenBalance()
		resp.StorageDeposit = ledger.MinimumStorageDeposit(o.Output)
		// Epoch params from this sequencer chain's sequencer constraint
		// (which is what makes it a sequencer chain in the first place).
		// Sequencer chains always carry the constraint; non-sequencer
		// chains never reach this code path. Defaults preserved as a
		// safety fallback only.
		epochSlots := lib.DelegationEpochSlots
		maxFrozenEpochs := byte(lib.MaxFrozenEpochs)
		if seqBytes, seqErr := o.Output.At(int(ledger.SequencerConstraintFixedIndex)); seqErr == nil && len(seqBytes) > 0 {
			if seq, sErr := ledger.SequencerConstraintFromBytesWithLib(seqBytes, lib); sErr == nil {
				epochSlots = seq.EpochSlots
				maxFrozenEpochs = seq.MaxFrozenEpochs
				resp.CoverageDelta = seq.CoverageDelta
			}
		}
		resp.FrozenCoverage = o.Output.Amounts().FrozenCoverageVector(maxFrozenEpochs)
		resp.CumulativeChainInflation = cc.CumulativeChainInflation
		resp.CumulativeBranchBonus = cc.CumulativeBranchBonus

		resp.NowSlot = nowSlot
		resp.CurrentEpoch = lib.EpochFromSlotDirect(seqID, nowSlot, epochSlots)
		resp.NextEpochBoundarySlot = lib.LastSlotInEpochDirect(seqID, resp.CurrentEpoch, epochSlots)
		resp.MaxFrozenEpochs = uint32(maxFrozenEpochs)
		resp.EpochDurationSlots = epochSlots
		resp.CoverageLowerBound = lib.CoverageContributionLowerBound(nowSlot)
		resp.CoverageUpperBound = lib.CoverageContributionUpperBound(nowSlot)

		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// getOutputs is the unified state-query endpoint described in
// claude/get_outputs.md. Single mandatory parameter `index_value`
// (1..255 byte hex), optional sort/filter/limit parameters. Response
// is api.GetOutputsResponse.
func (srv *server) getOutputs(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	writeErr := func(msg string) {
		respBin, err := json.MarshalIndent(&api.GetOutputsResponse{
			Error: api.Error{Error: msg},
		}, "", "  ")
		util.AssertNoError(err)
		_, _ = w.Write(respBin)
	}

	q := r.URL.Query()

	indexValueLst, ok := q["index_value"]
	if !ok || len(indexValueLst) != 1 || indexValueLst[0] == "" {
		writeErr("get_outputs: missing required parameter 'index_value'")
		return
	}
	indexValue, err := hex.DecodeString(indexValueLst[0])
	if err != nil {
		writeErr(fmt.Sprintf("get_outputs: invalid hex in 'index_value': %v", err))
		return
	}
	if len(indexValue) < 1 || len(indexValue) > 255 {
		writeErr(fmt.Sprintf("get_outputs: 'index_value' must be 1..255 bytes, got %d", len(indexValue)))
		return
	}

	maxOutputs := api.GetOutputsDefaultMaxOutputs
	if v, ok := q["max_outputs"]; ok && len(v) == 1 && v[0] != "" {
		n, err := strconv.Atoi(v[0])
		if err != nil || n <= 0 {
			writeErr(fmt.Sprintf("get_outputs: invalid 'max_outputs': %s", v[0]))
			return
		}
		maxOutputs = n
	}

	sortBy := api.GetOutputsSortByTimestamp
	if v, ok := q["sort_by"]; ok && len(v) == 1 {
		switch v[0] {
		case api.GetOutputsSortByTimestamp, api.GetOutputsSortByAmount:
			sortBy = v[0]
		default:
			writeErr(fmt.Sprintf("get_outputs: invalid 'sort_by': %s", v[0]))
			return
		}
	}

	sortOrder := api.GetOutputsSortOrderAsc
	if v, ok := q["sort_order"]; ok && len(v) == 1 {
		switch v[0] {
		case api.GetOutputsSortOrderAsc, api.GetOutputsSortOrderDesc:
			sortOrder = v[0]
		default:
			writeErr(fmt.Sprintf("get_outputs: invalid 'sort_order': %s", v[0]))
			return
		}
	}

	var forAmount uint64
	if v, ok := q["for_amount"]; ok && len(v) == 1 && v[0] != "" && v[0] != "none" {
		n, err := strconv.ParseUint(v[0], 10, 64)
		if err != nil {
			writeErr(fmt.Sprintf("get_outputs: invalid 'for_amount': %s", v[0]))
			return
		}
		forAmount = n
	}

	lockType := api.GetOutputsLockTypeSigLock
	if v, ok := q["lock_type"]; ok && len(v) == 1 {
		switch v[0] {
		case api.GetOutputsLockTypeAll,
			api.GetOutputsLockTypeSigLock,
			api.GetOutputsLockTypeChainLock,
			api.GetOutputsLockTypeTagAlongMaster,
			api.GetOutputsLockTypeTagAlongTarget,
			api.GetOutputsLockTypeDelegateMaster,
			api.GetOutputsLockTypeDelegateTarget:
			lockType = v[0]
		default:
			writeErr(fmt.Sprintf("get_outputs: invalid 'lock_type': %s", v[0]))
			return
		}
	}

	// tri-state: nil = no filter (both chained and non-chained);
	// &true = only chained; &false = only non-chained.
	var chainedFilter *bool
	if v, ok := q["chained"]; ok && len(v) == 1 && v[0] != "" {
		switch v[0] {
		case "true":
			t := true
			chainedFilter = &t
		case "false":
			f := false
			chainedFilter = &f
		default:
			writeErr(fmt.Sprintf("get_outputs: invalid 'chained': %s", v[0]))
			return
		}
	}

	// spendable=true post-filters the result set to outputs claimable
	// by the given indexValue (treated as the wallet's holder ID)
	// under a SINGLE-input signature unlock at target_slot. The slot
	// is needed for the sendWithDeadline Δ check (accept-window vs
	// reclaim-window). See claude/wallet_eval_api.md (Phase C3').
	spendableFilter := false
	if v, ok := q["spendable"]; ok && len(v) == 1 {
		switch v[0] {
		case "true":
			spendableFilter = true
		case "false", "":
			// default
		default:
			writeErr(fmt.Sprintf("get_outputs: invalid 'spendable': %s", v[0]))
			return
		}
	}

	// target_slot is the slot the caller intends to use as the tx
	// timestamp slot — sendWithDeadline Δ is measured against it.
	// 0 / omitted → default to the server's current LRB slot.
	var targetSlot uint32
	if v, ok := q["target_slot"]; ok && len(v) == 1 && v[0] != "" {
		n, err := strconv.ParseUint(v[0], 10, 32)
		if err != nil {
			writeErr(fmt.Sprintf("get_outputs: invalid 'target_slot': %s", v[0]))
			return
		}
		targetSlot = uint32(n)
	}

	resp := &api.GetOutputsResponse{}

	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()

		// Step 1: trie iteration with cap. Collect raw {oid, odata}.
		type rawHit struct {
			oid   base.OutputID
			odata []byte
		}
		hits := make([]rawHit, 0, 64)
		err1 := rdr.IterateUTXOsForController(indexValue, func(oid base.OutputID, odata []byte) bool {
			if len(hits) >= api.GetOutputsIterationCap {
				resp.LimitExceeded = true
				return false
			}
			hits = append(hits, rawHit{oid: oid, odata: odata})
			return true
		})
		if err1 != nil {
			return err1
		}

		// Step 2: hydrate to *Output (lib-free structural parse).
		type parsedHit struct {
			oid    base.OutputID
			out    *ledger.Output
			amount uint64
		}
		parsed := make([]parsedHit, 0, len(hits))
		for _, h := range hits {
			o, err := ledger.OutputFromBytes(h.odata)
			if err != nil {
				return fmt.Errorf("get_outputs: parse output %s: %w", h.oid.String(), err)
			}
			parsed = append(parsed, parsedHit{oid: h.oid, out: o, amount: o.TokenBalance()})
		}

		// Step 3 + 4: filter by lock_type + role and by chained.
		filtered := parsed[:0]
		for _, p := range parsed {
			if !matchesLockType(p.out, indexValue, lockType) {
				continue
			}
			if chainedFilter != nil {
				isChained := p.out.ChainConstraint() != nil
				if *chainedFilter != isChained {
					continue
				}
			}
			filtered = append(filtered, p)
		}

		// Step 4b: spendable filter (sigLock owned + claim-eligible
		// sendWithDeadline at target_slot, defaulting to LRB slot).
		// Lock dispatch uses the library active at that same slot.
		// See claude/wallet_eval_api.md.
		if spendableFilter {
			slot := targetSlot
			if slot == 0 {
				slot = lrbid.Timestamp().Slot
			}
			lib := ledger.L(slot)
			kept := filtered[:0]
			for _, p := range filtered {
				if isSpendableForAccount(p.out, p.oid, indexValue, slot, lib) {
					kept = append(kept, p)
				}
			}
			filtered = kept
		}

		// Step 5: sort.
		sortDesc := sortOrder == api.GetOutputsSortOrderDesc
		sort.SliceStable(filtered, func(i, j int) bool {
			var less bool
			switch sortBy {
			case api.GetOutputsSortByAmount:
				less = filtered[i].amount < filtered[j].amount
			default: // timestamp — by ledger time of OutputID
				ti := filtered[i].oid.Timestamp()
				tj := filtered[j].oid.Timestamp()
				if ti == tj {
					less = bytes.Compare(filtered[i].oid[:], filtered[j].oid[:]) < 0
				} else {
					less = ti.Before(tj)
				}
			}
			if sortDesc {
				return !less
			}
			return less
		})

		// Step 6: AvailableAmount over the (possibly capped) filtered set.
		var avail uint64
		for _, p := range filtered {
			avail += p.amount
		}
		resp.AvailableAmount = avail

		// Step 7: for_amount prefix. for_amount == 0 means unset.
		out := filtered
		if forAmount > 0 {
			var sum uint64
			cut := len(filtered)
			for i, p := range filtered {
				sum += p.amount
				if sum >= forAmount {
					cut = i + 1
					break
				}
			}
			// If unreachable (avail < forAmount), keep the full set;
			// the caller detects shortfall from AvailableAmount.
			if sum >= forAmount {
				out = filtered[:cut]
			}
		}

		// Step 8: truncate to max_outputs.
		if len(out) > maxOutputs {
			out = out[:maxOutputs]
		}

		resp.Outputs = make([]api.OutputDataWithID, 0, len(out))
		for _, p := range out {
			resp.Outputs = append(resp.Outputs, api.OutputDataWithID{
				ID:   p.oid.StringHex(),
				Data: p.out.Hex(),
			})
		}
		return nil
	})
	if err != nil {
		writeErr(err.Error())
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// matchesLockType returns true iff the output's lock kind and the role
// the indexValue plays inside it match the requested filter.
func matchesLockType(o *ledger.Output, indexValue []byte, lockType string) bool {
	if lockType == api.GetOutputsLockTypeAll {
		return true
	}
	values := o.IndexValues()
	name := o.Lock().Name()
	switch lockType {
	case api.GetOutputsLockTypeSigLock:
		return name == ledger.SigLockName && len(values) > 0 && bytes.Equal(values[0], indexValue)
	case api.GetOutputsLockTypeChainLock:
		return name == ledger.ChainLockName && len(values) > 0 && bytes.Equal(values[0], indexValue)
	case api.GetOutputsLockTypeTagAlongMaster:
		return name == ledger.TagAlongLockName && len(values) > 0 && bytes.Equal(values[0], indexValue)
	case api.GetOutputsLockTypeTagAlongTarget:
		return name == ledger.TagAlongLockName && len(values) > 1 && bytes.Equal(values[1], indexValue)
	case api.GetOutputsLockTypeDelegateMaster:
		return name == ledger.DelegateLockName && len(values) > 0 && bytes.Equal(values[0], indexValue)
	case api.GetOutputsLockTypeDelegateTarget:
		return name == ledger.DelegateLockName && len(values) > 1 && bytes.Equal(values[1], indexValue)
	}
	return false
}

// isSpendableForAccount returns true iff accountHID has a recognised
// single-signature claim on the output at targetSlot. It delegates to the
// shared txbuildercore.ClassifySpendable so the node and the wallet agree
// on what's claimable; any class other than SpendNotForAccount means the
// account has a claim (the fine-grained simple / needs-return-receipt /
// unknown-structure distinction is left to the caller — e.g. `proxi node
// compact` consumes only the simple ones and surfaces the rest).
//
// Recognised claims: a sigLock(accountHID) output; a sendWithDeadline output
// where accountHID is master in the reclaim window (Δ ≥ acceptanceSlots) or
// the sigLock target in the acceptance window (Δ < acceptanceSlots).
// chainLock-target acceptance is excluded (needs the chain input). `lib`
// (the library at targetSlot) is the bytecode parser.
func isSpendableForAccount(o *ledger.Output, oid base.OutputID, accountHID []byte, targetSlot uint32, lib *ledger.Library) bool {
	var hid base.HolderID
	if len(accountHID) != len(hid) {
		return false
	}
	copy(hid[:], accountHID)
	cls, err := txbuildercore.ClassifySpendable(lib, o.Bytes(), oid.Slot(), hid, targetSlot)
	if err != nil {
		return false
	}
	return cls != txbuildercore.SpendNotForAccount
}

func (srv *server) withLRB(fun func(rdr multistate.SugaredStateReader) error) error {
	return util.CatchPanicOrError(func() error {
		rdr, err1 := srv.LatestReliableState()
		if err1 != nil {
			return err1
		}
		return fun(rdr)
	})
}

func Run(addr string, env environment) {
	srv := &server{
		Server: &http.Server{
			Addr:         addr,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  10 * time.Second,
		},
		environment: env,
	}
	srv.registerHandlers()
	srv.registerMetrics()

	// graceful shutdown: stop accepting new connections when global context is cancelled,
	// preventing "database is closed or unavailable" panics during shutdown
	go func() {
		<-env.Ctx().Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		env.Log().Errorf("API server error: %v", err)
	}
}

func (srv *server) registerMetrics() {
	srv.metrics.totalRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_api_totalRequests",
		Help: "total API requests",
	})
	srv.MetricsRegistry().MustRegister(srv.metrics.totalRequests)
}

func (srv *server) addHandler(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	http.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		srv.Tracef(TraceTag, "API request: %s from %s", r.URL.String(), r.RemoteAddr)
		handler(w, r)
		srv.metrics.totalRequests.Inc()
	})
}
