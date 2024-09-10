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
	Environment interface {
		global.Logging
		GetNodeInfo() *global.NodeInfo
		GetSyncInfo() *api.SyncInfo
		GetPeersInfo() *api.PeersInfo
		HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader
		SubmitTxBytesFromAPI(txBytes []byte, trace bool)
		QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble
		GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion
		GetLatestReliableBranch() (*multistate.BranchData, bool)
	}

	Server struct {
		Environment
	}

	TxStatus struct {
		vertex.TxIDStatus
		*multistate.TxInclusion
	}
)

const TraceTag = "apiServer"

func New(env Environment) *Server {
	return &Server{Environment: env}
}

func (srv *Server) registerHandlers() {
	// GET request format: '/get_ledger_id'
	http.HandleFunc(api.PathGetLedgerID, srv.getLedgerID)
	// GET request format: '/get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc(api.PathGetAccountOutputs, srv.getAccountOutputs)
	// GET request format: '/get_chain_output?chainid=<hex-encoded chain ID>'
	http.HandleFunc(api.PathGetChainOutput, srv.getChainOutput)
	// GET request format: '/get_output?id=<hex-encoded output ID>'
	http.HandleFunc(api.PathGetOutput, srv.getOutput)
	// GET request format: '/query_txid_status?txid=<hex-encoded transaction ID>[&slots=<slot span>]'
	http.HandleFunc(api.PathQueryTxStatus, srv.queryTxStatus)
	// GET request format: '/query_inclusion_score?txid=<hex-encoded transaction ID>&threshold=N-D[&slots=<slot span>]'
	http.HandleFunc(api.PathQueryInclusionScore, srv.queryTxInclusionScore)
	// POST request format '/submit_nowait'. Feedback only on parsing error, otherwise async posting
	http.HandleFunc(api.PathSubmitTransaction, srv.submitTx)
	// GET sync info from the node
	http.HandleFunc(api.PathGetSyncInfo, srv.getSyncInfo)
	// GET sync info from the node
	http.HandleFunc(api.PathGetNodeInfo, srv.getNodeInfo)
	// GET peers info from the node
	http.HandleFunc(api.PathGetPeersInfo, srv.getPeersInfo)
	// GET latest reliable branch '/get_latest_reliable_branch'
	http.HandleFunc(api.PathGetLatestReliableBranch, srv.getLatestReliableBranch)
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
	err = util.CatchPanicOrError(func() error {
		var err1 error
		oData, err1 = srv.HeaviestStateForLatestTimeSlot().GetUTXOsLockedInAccount(accountable.AccountID())
		return err1
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	resp := &api.OutputList{}
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
	var out *ledger.OutputWithID
	err = util.CatchPanicOrError(func() error {
		var err1 error
		out, err1 = srv.HeaviestStateForLatestTimeSlot().GetChainOutput(&chainID)
		return err1
	})
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	resp := &api.ChainOutput{
		OutputID:   out.ID.StringHex(),
		OutputData: hex.EncodeToString(out.Output.Bytes()),
	}

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
	var oData []byte
	err = util.CatchPanicOrError(func() error {
		var found bool
		oData, found = srv.HeaviestStateForLatestTimeSlot().GetUTXO(&oid)
		if !found {
			return errors.New(api.ErrGetOutputNotFound)
		}
		return nil
	})
	if err != nil {
		writeErr(w, api.ErrGetOutputNotFound)
		return
	}
	resp := &api.OutputData{
		OutputData: hex.EncodeToString(oData),
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

	bd, found := srv.GetLatestReliableBranch()
	if !found {
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

	return api.CalcTxInclusionScore(inclusion, thresholdNumerator, thresholdDenominator)
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

func RunOn(addr string, env Environment) {
	srv := New(env)
	srv.registerHandlers()
	err := http.ListenAndServe(addr, nil)
	util.AssertNoError(err)
}

func setHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
