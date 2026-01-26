package server

import (
	"bytes"
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
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/exp/slices"
)

type (
	environment interface {
		global.Logging
		global.Metrics
		GetNodeInfo() *global.NodeInfo
		GetSyncInfo() *api.SyncInfo
		GetPeersInfo() *api.PeersInfo
		LatestReliableState() (multistate.SugaredStateReader, error)
		CheckTransactionInLRB(txid base.TransactionID, maxDepth int) (lrbid base.TransactionID, foundAtDepth int)
		SubmitTxBytesFromAPI(txBytes []byte)
		GetLatestReliableBranch() *multistate.BranchData
		GetSnapshotBranchID() base.TransactionID
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
	// GET request format: '/api/v1/get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	srv.addHandler(api.PathGetAccountOutputs, srv.getAccountOutputs)
	// GET request format: '/api/v1/get_account_parsed_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	srv.addHandler(api.PathGetAccountParsedOutputs, srv.getAccountParsedOutputs)
	// GET request format: '/api/v1/get_account_simple_siglocked?addr=<a(0x....)>'
	srv.addHandler(api.PathGetAccountSimpleSiglockedOutputs, srv.getAccountSimpleSigLockedOutputs)
	// GET request format: '/api/v1/get_outputs_for_amount?addr=<a(0x....)>&amount=<amount>'
	srv.addHandler(api.PathGetOutputsForAmount, srv.getOutputsForAmount)
	// GET request format: '/api/v1/get_nonchain_balance?addr=<a(0x....)>'
	srv.addHandler(api.PathGetNonChainBalance, srv.getNonChainBalance)
	// GET request format: '/api/v1/get_chained_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	srv.addHandler(api.PathGetChainedOutputs, srv.getChainedOutputs)
	// GET request format: '/api/v1/get_delegation_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	srv.addHandler(api.PathGetDelegationOutputs, srv.getDelegationOutputs)
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
	// GET dashboard for node
	srv.addHandler(api.PathGetDashboard, srv.getDashboard)
	// GET inactive UTXOs in LRB /get_inactive?[slots_back=<slot>]
	srv.addHandler(api.PathGetInactive, srv.getInactive)

	// Transaction logger API
	// POST /api/v1/txlog/enable?level=<level>
	srv.addHandler(api.PathTxLogEnable, srv.txLogEnable)
	// GET /api/v1/txlog/get?prefix=<hex_prefix>&max=<max>
	srv.addHandler(api.PathTxLogGet, srv.txLogGet)
	// GET /api/v1/txlog/range?from=<unix_ns>&to=<unix_ns>&max=<max>
	srv.addHandler(api.PathTxLogRange, srv.txLogRange)

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
		LibraryYAML:     string(lib.DefinitionsYAML()),
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

func (srv *server) _getAccountOutputsWithFilter(r *http.Request, addr ledger.Accountable, filter func(oid base.OutputID, o *ledger.Output) bool) (
	outs []*ledger.OutputWithID, lrbid base.TransactionID, err error) {
	if filter == nil {
		filter = func(_ base.OutputID, _ *ledger.Output) bool { return true }
	}
	maxOutputs := absoluteMaximumOfReturnedOutputs
	lst, ok := r.URL.Query()["max_outputs"]
	if ok {
		if len(lst) != 1 {
			err = fmt.Errorf("wrong parameter 'max_outputs'")
			return
		}
		maxOutputs, err = strconv.Atoi(lst[0])
		if err != nil {
			return
		}
		if maxOutputs > absoluteMaximumOfReturnedOutputs {
			maxOutputs = absoluteMaximumOfReturnedOutputs
		}
	}

	doSorting := false
	sortDesc := false
	lst, ok = r.URL.Query()["sort"]
	if ok {
		if len(lst) != 1 || (lst[0] != "asc" && lst[0] != "desc") {
			err = fmt.Errorf("wrong parameter 'sort'")
			return
		}
		doSorting = true
		sortDesc = lst[0] == "desc"
	}

	outs = make([]*ledger.OutputWithID, 0)

	err = srv.withLRB(func(rdr multistate.SugaredStateReader) (errRet error) {
		lrbid = rdr.GetStemOutput().ID.TransactionID()
		err1 := rdr.IterateOutputsForAccount(addr, func(oid base.OutputID, o *ledger.Output) bool {
			if filter(oid, o) {
				outs = append(outs, &ledger.OutputWithID{
					ID:     oid,
					Output: o,
				})
			}
			return true
		})
		if err1 != nil {
			return err1
		}
		return
	})
	if err != nil {
		return
	}
	if doSorting {
		sort.Slice(outs, func(i, j int) bool {
			if sortDesc {
				return bytes.Compare(outs[i].ID[:], outs[j].ID[:]) > 0
			}
			return bytes.Compare(outs[i].ID[:], outs[j].ID[:]) < 0

		})
	}
	outs = util.TrimSlice(outs, maxOutputs)
	return
}

const absoluteMaximumOfReturnedOutputs = 2000

func _writeOutputs(w http.ResponseWriter, outs []*ledger.OutputWithID, lrbid base.TransactionID) {
	resp := &api.OutputList{
		Outputs: make(map[string]string),
		LRBID:   lrbid.StringHex(),
	}

	for _, o := range outs {
		resp.Outputs[o.ID.StringHex()] = o.Output.Hex()
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func _writeParsedOutputs(w http.ResponseWriter, outs []*ledger.OutputWithID, lrbid base.TransactionID) {
	resp := &api.ParsedOutputList{
		Outputs: make(map[string]api.ParsedOutput),
		LRBID:   lrbid.StringHex(),
	}

	for _, o := range outs {
		po := api.ParsedOutput{
			Data:        o.Output.Hex(),
			Constraints: o.Output.LinesPlainSource().Slice(),
			Amount:      o.Output.TokenBalance(),
			LockName:    o.Output.Lock().Name(),
			ChainID:     "",
		}
		if chainID, _, ok := o.ExtractChainID(); ok {
			po.ChainID = chainID.StringHex()
		}
		resp.Outputs[o.ID.StringHex()] = po
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// getAccountOutputs returns all outputs from the account
// Lock can be of any type
func (srv *server) getAccountOutputs(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["accountable"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'accountable' in request 'get_account_outputs'")
		return
	}
	accountable, err := ledger.AccountableFromSource(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	outs, lrbid, err := srv._getAccountOutputsWithFilter(r, accountable, nil)
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_writeOutputs(w, outs, lrbid)
}

// getAccountOutputs returns all outputs from the account
// Lock can be of any type
func (srv *server) getAccountParsedOutputs(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["accountable"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'accountable' in request 'get_account_outputs'")
		return
	}
	accountable, err := ledger.AccountableFromSource(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	outs, lrbid, err := srv._getAccountOutputsWithFilter(r, accountable, nil)
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_writeParsedOutputs(w, outs, lrbid)
}

// getAccountSimpleSigLockedOutputs returns outputs locked with simple AddressED25519 lock
func (srv *server) getAccountSimpleSigLockedOutputs(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["addr"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'addr' in request 'get_account_simple_siglocked_outputs'")
		return
	}
	addr, err := ledger.AddressED25519FromSource(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	outs, lrbid, err := srv._getAccountOutputsWithFilter(r, addr, func(_ base.OutputID, o *ledger.Output) bool {
		if o.Lock().Name() != ledger.AddressED25519Name {
			return false
		}
		if _, idx := o.ChainConstraint(); idx != 0xff {
			return false
		}
		return true
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_writeOutputs(w, outs, lrbid)
}

func (srv *server) getNonChainBalance(w http.ResponseWriter, r *http.Request) {
	lst, ok := r.URL.Query()["addr"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'addr' in request 'get_balance_addr25519'")
		return
	}
	targetAddr, err := ledger.AddressED25519FromSource(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	var resp api.Balance

	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		err1 := rdr.IterateOutputsForAccount(targetAddr, func(_ base.OutputID, o *ledger.Output) bool {
			if o.Lock().Name() != ledger.AddressED25519Name {
				return true
			}
			if _, idx := o.ChainConstraint(); idx != 0xff {
				return true
			}
			resp.Amount += o.TokenBalance()
			return true
		})
		if err1 != nil {
			return err1
		}
		return nil
	})
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// does not return chain output
func (srv *server) getOutputsForAmount(w http.ResponseWriter, r *http.Request) {
	lst, ok := r.URL.Query()["addr"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'addr' in request 'get_outputs_for_amount'")
		return
	}
	targetAddr, err := ledger.AddressED25519FromSource(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	lst, ok = r.URL.Query()["amount"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'amount' in request 'get_outputs_for_amount'")
		return
	}
	amount, err := strconv.Atoi(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := &api.OutputList{
		Outputs: make(map[string]string),
	}
	sum := uint64(0)
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		err1 := rdr.IterateOutputsForAccount(targetAddr, func(oid base.OutputID, o *ledger.Output) bool {
			if o.Lock().Name() != ledger.AddressED25519Name {
				return true
			}
			if _, idx := o.ChainConstraint(); idx != 0xff {
				// filter out chained outputs
				return true
			}
			if !ledger.EqualAccountables(targetAddr, o.Lock().(ledger.AddressED25519)) {
				return true
			}
			resp.Outputs[oid.StringHex()] = o.Hex()
			sum += o.TokenBalance()
			return sum < uint64(amount)
		})
		if err1 != nil {
			return err1
		}
		return nil
	})

	if sum < uint64(amount) {
		api.WriteErr(w, fmt.Sprintf("not enough tokens in non-chained UTXOs: %s < than requested %s", util.Th(sum), util.Th(amount)))
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

func (srv *server) getDelegationOutputs(w http.ResponseWriter, r *http.Request) {
	srv._getChainedOutputsFiltered(w, r, func(_ base.OutputID, o *ledger.Output) bool {
		return o.DelegationLock() != nil
	})
}

func (srv *server) getChainedOutputs(w http.ResponseWriter, r *http.Request) {
	srv._getChainedOutputsFiltered(w, r, func(_ base.OutputID, _ *ledger.Output) bool { return true })
}

func (srv *server) _getChainedOutputsFiltered(w http.ResponseWriter, r *http.Request, filter func(oid base.OutputID, o *ledger.Output) bool) {
	api.SetHeader(w)

	lst, ok := r.URL.Query()["accountable"]
	if !ok || len(lst) != 1 {
		api.WriteErr(w, "wrong parameter 'accountable' in request 'get_chained_outputs'")
		return
	}
	accountable, err := ledger.AccountableFromSource(lst[0])
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := api.ChainedOutputs{
		Outputs: make(map[string]string),
	}
	var err1 error
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()

		err1 = rdr.IterateChainsInAccount(accountable, func(oid base.OutputID, o *ledger.Output, _ base.ChainID) bool {
			if filter(oid, o) {
				resp.Outputs[oid.StringHex()] = hex.EncodeToString(o.Bytes())
			}
			return true
		})
		if err1 != nil {
			return err1
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

const (
	maxTxUploadSize            = 64 * (1 << 10)
	defaultTxAppendWaitTimeout = 10 * time.Second
	maxTxAppendWaitTimeout     = 2 * time.Minute
)

func (srv *server) submitTx(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	timeout := defaultTxAppendWaitTimeout
	lst, ok := r.URL.Query()["timeout"]
	if ok {
		wrong := len(lst) != 1
		var timeoutSec int
		var err error
		if !wrong {
			timeoutSec, err = strconv.Atoi(lst[0])
			wrong = err != nil || timeoutSec < 0
		}
		if wrong {
			api.WriteErr(w, "wrong 'timeout' parameter in request 'submit_wait'")
			return
		}
		timeout = time.Duration(timeoutSec) * time.Second
		if timeout > maxTxAppendWaitTimeout {
			timeout = maxTxAppendWaitTimeout
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTxUploadSize)
	txBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// tx tracing on server parameter
	err = util.CatchPanicOrError(func() error {
		srv.SubmitTxBytesFromAPI(slices.Clip(txBytes))
		return nil
	})
	if err != nil {
		api.WriteErr(w, fmt.Sprintf("submit_tx: %v", err))
		srv.Tracef(TraceTag, "submit transaction: '%v'", err)
		return
	}
	api.WriteOk(w)
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
	main, err := multistate.GetMainChain(srv.StateStore(), global.FractionHealthyBranch, maxDepth)
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
		RootData: *bd.RootRecord.JSONAble(),
		BranchID: bd.Stem.ID.TransactionID(),
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

	err := srv.ListenAndServe()
	util.AssertNoError(err)
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
