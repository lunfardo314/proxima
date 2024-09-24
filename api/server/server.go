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
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/exp/slices"
)

type (
	environment interface {
		global.Logging
		GetNodeInfo() *global.NodeInfo
		GetSyncInfo() *api.SyncInfo
		GetPeersInfo() *api.PeersInfo
		LatestReliableState() (multistate.SugaredStateReader, error)
		SubmitTxBytesFromAPI(txBytes []byte, trace bool)
		QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble
		GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion
		GetLatestReliableBranch() *multistate.BranchData
	}

	Server struct {
		environment
	}

	TxStatus struct {
		vertex.TxIDStatus
		*multistate.TxInclusion
	}
)

const TraceTag = "apiServer"

func New(env environment) *Server {
	return &Server{environment: env}
}

func addHandler(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	http.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
		_ = r.Body.Close()
	})
}

func (srv *Server) registerHandlers() {
	// GET request format: '/get_ledger_id'
	addHandler(api.PathGetLedgerID, srv.getLedgerID)
	// GET request format: '/get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	addHandler(api.PathGetAccountOutputs, srv.getAccountOutputs)
	// GET request format: '/get_chain_output?chainid=<hex-encoded chain ID>'
	addHandler(api.PathGetChainOutput, srv.getChainOutput)
	// GET request format: '/get_output?id=<hex-encoded output ID>'
	addHandler(api.PathGetOutput, srv.getOutput)
	// GET request format: '/query_txid_status?txid=<hex-encoded transaction ID>[&slots=<slot span>]'
	addHandler(api.PathQueryTxStatus, srv.queryTxStatus)
	// GET request format: '/query_inclusion_score?txid=<hex-encoded transaction ID>&threshold=N-D[&slots=<slot span>]'
	addHandler(api.PathQueryInclusionScore, srv.queryTxInclusionScore)
	// POST request format '/submit_nowait'. Feedback only on parsing error, otherwise async posting
	addHandler(api.PathSubmitTransaction, srv.submitTx)
	// GET sync info from the node
	addHandler(api.PathGetSyncInfo, srv.getSyncInfo)
	// GET sync info from the node
	addHandler(api.PathGetNodeInfo, srv.getNodeInfo)
	// GET peers info from the node
	addHandler(api.PathGetPeersInfo, srv.getPeersInfo)
	// GET latest reliable branch '/get_latest_reliable_branch'
	addHandler(api.PathGetLatestReliableBranch, srv.getLatestReliableBranch)
}

func (srv *Server) getLedgerID(w http.ResponseWriter, r *http.Request) {
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

func (srv *Server) getAccountOutputs(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTag, "getAccountOutputs invoked")
	setHeader(w)

	lst, ok := r.URL.Query()["accountable"]
	if !ok || len(lst) != 1 {
		writeErr(w, "wrong parameters in request 'get_account_outputs'")
		return
	}
	accountable, err := ledger.AccountableFromSource(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
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
	if len(oData) > 0 {
		resp.Outputs = make(map[string]string)
		for _, o := range oData {
			resp.Outputs[o.ID.StringHex()] = hex.EncodeToString(o.OutputData)
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

func (srv *Server) getChainOutput(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTag, "getChainOutput invoked")
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

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *Server) getOutput(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTag, "getOutput invoked")
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

func (srv *Server) submitTx(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTag, "submitTx invoked")
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
	_, trace := r.URL.Query()["trace"]
	var txid *ledger.TransactionID
	err = util.CatchPanicOrError(func() error {
		srv.SubmitTxBytesFromAPI(slices.Clip(txBytes), trace)
		return nil
	})
	if err != nil {
		writeErr(w, fmt.Sprintf("submit_tx: %v", err))
		srv.Tracef(TraceTag, "submit transaction: '%v'", err)
		return
	}
	srv.Tracef(TraceTag, "submitted transaction %s, trace = %v", txid.StringShort, trace)

	writeOk(w)
}

func (srv *Server) getSyncInfo(w http.ResponseWriter, r *http.Request) {
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

func (srv *Server) getPeersInfo(w http.ResponseWriter, r *http.Request) {
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

func (srv *Server) getNodeInfo(w http.ResponseWriter, r *http.Request) {
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

const maxSlotsSpan = 10

func (srv *Server) queryTxStatus(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTag, "queryTxStatus invoked")
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

func (srv *Server) queryTxInclusionScore(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTagQueryInclusion, "queryTxInclusionScore invoked")
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

func (srv *Server) getLatestReliableBranch(w http.ResponseWriter, r *http.Request) {
	srv.Tracef(TraceTag, "getLatestReliableBranch invoked")

	bd := srv.GetLatestReliableBranch()
	if bd == nil {
		writeErr(w, "latest reliable branch has not been found")
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

// calcTxInclusionScore calculates inclusion score response from inclusion data
func (srv *Server) calcTxInclusionScore(inclusion *multistate.TxInclusion, thresholdNumerator, thresholdDenominator int) api.TxInclusionScore {
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

func RunOn(addr string, env environment) {
	srv := New(env)
	srv.registerHandlers()
	err := http.ListenAndServe(addr, nil)
	util.AssertNoError(err)
}

func setHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func (srv *Server) withLRB(fun func(rdr multistate.SugaredStateReader) error) error {
	return util.CatchPanicOrError(func() error {
		rdr, err1 := srv.LatestReliableState()
		if err1 != nil {
			return err1
		}
		return fun(rdr)
	})
}
