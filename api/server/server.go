package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
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
		SubmitTxBytesFromAPI(txBytes []byte)
		QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble
		GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion
		GetLatestReliableBranch() *multistate.BranchData
		StateStore() global.StateStore
		TxBytesStore() global.TxBytesStore
		GetKnownLatestMilestonesJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble
	}

	server struct {
		*http.Server
		environment
		metrics
	}

	TxStatus struct {
		vertex.TxIDStatus
		*multistate.TxInclusion
	}

	metrics struct {
		totalRequests prometheus.Counter
	}
)

const TraceTag = "apiServer"

func (srv *server) registerHandlers() {
	// GET request format: '/api/v1/get_ledger_id'
	srv.addHandler(api.PathGetLedgerID, srv.getLedgerID)
	// GET request format: '/api/v1/get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	srv.addHandler(api.PathGetAccountOutputs, srv.getAccountOutputs)
	// GET request format: '/api/v1/get_chain_output?chainid=<hex-encoded chain ID>'
	srv.addHandler(api.PathGetChainOutput, srv.getChainOutput)
	// GET request format: '/api/v1/get_output?id=<hex-encoded output ID>'
	srv.addHandler(api.PathGetOutput, srv.getOutput)
	// GET request format: '/api/v1/query_txid_status?txid=<hex-encoded transaction ID>[&slots=<slot span>]'
	srv.addHandler(api.PathQueryTxStatus, srv.queryTxStatus)
	// GET request format: '/api/v1/query_inclusion_score?txid=<hex-encoded transaction ID>&threshold=N-D[&slots=<slot span>]'
	srv.addHandler(api.PathQueryInclusionScore, srv.queryTxInclusionScore)
	// POST request format '/api/v1/submit_nowait'. Feedback only on parsing error, otherwise async posting
	srv.addHandler(api.PathSubmitTransaction, srv.submitTx)
	// GET sync info from the node '/api/v1/sync_info'
	srv.addHandler(api.PathGetSyncInfo, srv.getSyncInfo)
	// GET node info from the node '/api/v1/node_info'
	srv.addHandler(api.PathGetNodeInfo, srv.getNodeInfo)
	// GET peers info from the node '/api/v1/peers_info'
	srv.addHandler(api.PathGetPeersInfo, srv.getPeersInfo)
	// GET latest reliable branch '/api/v1/get_latest_reliable_branch'
	srv.addHandler(api.PathGetLatestReliableBranch, srv.getLatestReliableBranch)
	// GET latest reliable branch and check if transaction ID is in it '/check_txid_in_lrb?txid=<hex-encoded transaction ID>'
	srv.addHandler(api.PathCheckTxIDInLRB, srv.checkTxIDIncludedInLRB)
	// GET last milestone list
	srv.addHandler(api.PathGetLastKnownSequencerMilestones, srv.getMilestoneList)
	// GET main chain of branches /get_mainchain?[max=]
	srv.addHandler(api.PathGetMainChain, srv.getMainChain)
	// GET dashboard for node
	srv.addHandler(api.PathGetDashboard, srv.getDashboard)

	// register handlers of tx API
	srv.registerTxAPIHandlers()
}

func (srv *server) getLedgerID(w http.ResponseWriter, _ *http.Request) {
	setHeader(w)

	srv.Tracef(TraceTag, "getLedgerID invoked")

	resp := &api.LedgerID{
		LedgerIDBytes: hex.EncodeToString(ledger.L().ID.Bytes()),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

const absoluteMaximumOfReturnedOutputs = 2000

// getAccountOutputs return in general non-deterministic set of outputs because of random ordering and limits
func (srv *server) getAccountOutputs(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	// parse parameters

	lst, ok := r.URL.Query()["accountable"]
	if !ok || len(lst) != 1 {
		writeErr(w, "wrong parameter 'accountable' in request 'get_account_outputs'")
		return
	}
	accountable, err := ledger.AccountableFromSource(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	maxOutputs := 0
	lst, ok = r.URL.Query()["max_outputs"]
	if ok {
		if len(lst) != 1 {
			writeErr(w, "wrong parameter 'max_outputs' in request 'get_account_outputs'")
			return
		}
		maxOutputs, err = strconv.Atoi(lst[0])
		if err != nil {
			writeErr(w, err.Error())
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
			writeErr(w, "wrong parameter 'sort' in request 'get_account_outputs'")
			return
		}
		doSorting = true
		sortDesc = lst[0] == "desc"
	}

	var oData []*ledger.OutputDataWithID

	resp := &api.OutputList{}
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) (errRet error) {
		oData, errRet = rdr.GetUTXOsLockedInAccount(accountable.AccountID())
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	resp.Outputs = make(map[string]string)
	if !doSorting {
		if len(oData) > 0 {
			for _, o := range oData {
				if maxOutputs > 0 && len(resp.Outputs) >= maxOutputs {
					break
				}
				resp.Outputs[o.ID.StringHex()] = hex.EncodeToString(o.OutputData)
			}
		}
	} else {
		// return first max number of sorted outputs
		sorted := make(map[string]*ledger.Output)
		for _, o := range oData {
			sorted[o.ID.StringHex()], err = ledger.OutputFromBytesReadOnly(o.OutputData)
			if err != nil {
				writeErr(w, "server error while parsing UTXO: "+err.Error())
				return
			}
		}
		idsSorted := util.KeysSorted(sorted, func(k1, k2 string) bool {
			if sortDesc {
				return sorted[k1].Amount() > sorted[k2].Amount()
			}
			return sorted[k1].Amount() < sorted[k2].Amount()
		})
		for _, id := range idsSorted {
			if maxOutputs > 0 && len(resp.Outputs) >= maxOutputs {
				break
			}
			resp.Outputs[id] = hex.EncodeToString(sorted[id].Bytes())
		}
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getChainOutput(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	lst, ok := r.URL.Query()["chainid"]
	if !ok || len(lst) != 1 {
		writeErr(w, "wrong parameters in request 'get_chain_output'")
		return
	}
	chainID, err := ledger.ChainIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	resp := &api.ChainOutput{}
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		o, err1 := rdr.GetChainOutput(&chainID)
		if err1 != nil {
			return err1
		}
		resp.OutputID = o.ID.StringHex()
		resp.OutputData = hex.EncodeToString(o.Output.Bytes())
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return nil
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getOutput(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	lst, ok := r.URL.Query()["id"]
	if !ok || len(lst) != 1 {
		writeErr(w, "wrong parameter in request 'get_output'")
		return
	}
	oid, err := ledger.OutputIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	resp := &api.OutputData{}
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		oData, found := rdr.GetUTXO(&oid)
		if !found {
			return errors.New(api.ErrGetOutputNotFound)
		}
		resp.OutputData = hex.EncodeToString(oData)
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		return nil
	})
	if err != nil {
		writeErr(w, api.ErrGetOutputNotFound)
		return
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
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
	setHeader(w)

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
			writeErr(w, "wrong 'timeout' parameter in request 'submit_wait'")
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
		writeErr(w, fmt.Sprintf("submit_tx: %v", err))
		srv.Tracef(TraceTag, "submit transaction: '%v'", err)
		return
	}
	writeOk(w)
}

func (srv *server) getSyncInfo(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	syncInfo := srv.GetSyncInfo()
	respBin, err := json.MarshalIndent(syncInfo, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getPeersInfo(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	peersInfo := srv.GetPeersInfo()
	respBin, err := json.MarshalIndent(peersInfo, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getNodeInfo(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	nodeInfo := srv.GetNodeInfo()
	respBin, err := json.MarshalIndent(nodeInfo, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getMilestoneList(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	resp := api.KnownLatestMilestones{
		Sequencers: srv.GetKnownLatestMilestonesJSONAble(),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

const defaultMaxMainChainDepth = 20

func (srv *server) getMainChain(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	var err error
	maxDepth := defaultMaxMainChainDepth
	lst, ok := r.URL.Query()["max"]
	if ok || len(lst) == 1 {
		if maxDepth, err = strconv.Atoi(lst[0]); err != nil {
			writeErr(w, "wrong parameter 'max'")
			return
		}
	}
	if maxDepth <= 0 {
		maxDepth = 1
	}
	main, err := multistate.GetMainChain(srv.StateStore(), global.FractionHealthyBranch, maxDepth)
	if err != nil {
		writeErr(w, err.Error())
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
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

const maxSlotsSpan = 10

func (srv *server) queryTxStatus(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	var txid ledger.TransactionID
	var err error

	lst, ok := r.URL.Query()["txid"]
	if len(lst) != 1 {
		writeErr(w, "txid expected")
		return
	}
	txid, err = ledger.TransactionIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	slotSpan := 1
	lst, ok = r.URL.Query()["slots"]
	if ok && len(lst) == 1 {
		slotSpan, err = strconv.Atoi(lst[0])

		if slotSpan < 1 || slotSpan > maxSlotsSpan {
			writeErr(w, fmt.Sprintf("parameter 'slots' must be between 1 and %d", maxSlotsSpan))
			return
		}
	}

	// query tx ID status
	var resp api.QueryTxStatus
	err = util.CatchPanicOrError(func() error {
		resp = api.QueryTxStatus{
			TxIDStatus: srv.QueryTxIDStatusJSONAble(&txid),
			Inclusion:  srv.GetTxInclusion(&txid, slotSpan).JSONAble(),
		}
		return nil
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func decodeThreshold(par string) (int, int, error) {
	thrSplit := strings.Split(par, "-")
	if len(thrSplit) != 2 {
		return 0, 0, fmt.Errorf("wrong parameter 'threshold'")
	}
	num, err := strconv.Atoi(thrSplit[0])
	if err != nil {
		return 0, 0, fmt.Errorf("wrong parameter 'threshold': %v", err)
	}
	denom, err := strconv.Atoi(thrSplit[1])
	if err != nil {
		return 0, 0, fmt.Errorf("wrong parameter 'threshold': %v", err)
	}
	if !multistate.ValidInclusionThresholdFraction(num, denom) {
		return 0, 0, fmt.Errorf("wrong parameter 'threshold': %s", par)
	}
	return num, denom, nil
}

const TraceTagQueryInclusion = "inclusion"

func (srv *server) queryTxInclusionScore(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	var txid ledger.TransactionID
	var err error

	lst, ok := r.URL.Query()["txid"]
	if len(lst) != 1 {
		writeErr(w, "txid expected")
		return
	}

	txid, err = ledger.TransactionIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	slotSpan := 1
	lst, ok = r.URL.Query()["slots"]
	if ok && len(lst) == 1 {
		slotSpan, err = strconv.Atoi(lst[0])

		if slotSpan < 1 || slotSpan > maxSlotsSpan {
			writeErr(w, fmt.Sprintf("parameter 'slots' must be between 1 and %d", maxSlotsSpan))
			return
		}
	}

	var thresholdNumerator, thresholdDenominator int
	lst, ok = r.URL.Query()["threshold"]
	if ok && len(lst) == 1 {
		thresholdNumerator, thresholdDenominator, err = decodeThreshold(lst[0])
		if err != nil {
			writeErr(w, err.Error())
			return
		}
	} else {
		writeErr(w, fmt.Sprintf("wrong or missing parameter 'threshold': %+v", lst))
		return
	}
	var inclusion *multistate.TxInclusion
	err = util.CatchPanicOrError(func() error {
		inclusion = srv.GetTxInclusion(&txid, slotSpan)
		return nil
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	resp := api.QueryTxInclusionScore{
		TxInclusionScore: srv.calcTxInclusionScore(inclusion, thresholdNumerator, thresholdDenominator),
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getLatestReliableBranch(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	bd := srv.GetLatestReliableBranch()
	if bd == nil {
		writeErr(w, "latest reliable branch (LRB) has not been found")
		return
	}

	resp := &api.LatestReliableBranch{
		RootData: *bd.RootRecord.JSONAble(),
		BranchID: bd.Stem.ID.TransactionID(),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) checkTxIDIncludedInLRB(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	var txid ledger.TransactionID
	var err error

	lst, ok := r.URL.Query()["txid"]
	if !ok || len(lst) != 1 {
		writeErr(w, "txid expected")
		return
	}
	txid, err = ledger.TransactionIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	var resp api.CheckRxIDInLRB
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		lrbid := rdr.GetStemOutput().ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		resp.TxID = txid.StringHex()
		resp.Included = rdr.KnowsCommittedTransaction(&txid)
		return nil
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// calcTxInclusionScore calculates inclusion score response from inclusion data
func (srv *server) calcTxInclusionScore(inclusion *multistate.TxInclusion, thresholdNumerator, thresholdDenominator int) api.TxInclusionScore {
	srv.Tracef(TraceTagQueryInclusion, "calcTxInclusionScore: %s, threshold: %d/%d", inclusion.String(), thresholdNumerator, thresholdDenominator)

	ret := api.CalcTxInclusionScore(inclusion, thresholdNumerator, thresholdDenominator)
	ret.LRBID = inclusion.LRBID.StringHex()
	ret.IncludedInLRB = inclusion.IncludedInLRB
	return ret
}

func writeErr(w http.ResponseWriter, errStr string) {
	respBytes, err := json.Marshal(&api.Error{Error: errStr})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = w.Write(respBytes)
	util.AssertNoError(err)
}

func writeOk(w http.ResponseWriter) {
	respBytes, err := json.Marshal(&api.Error{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = w.Write(respBytes)
	util.AssertNoError(err)
}

func writeNotImplemented(w http.ResponseWriter) {
	writeErr(w, "not implemented")
}

func setHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
