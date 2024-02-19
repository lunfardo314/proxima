package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
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
		HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader
		SubmitTxBytesFromAPI(txBytes []byte) error
		QueryTxIDStatus(txid *ledger.TransactionID) (mode string, status string, err error)
	}

	Server struct {
		Environment
	}
)

func New(env Environment) *Server {
	return &Server{env}
}

func (srv *Server) registerHandlers() {
	// GET request format: 'get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc(api.PathGetLedgerID, getLedgerID)
	// GET request format: 'get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc(api.PathGetAccountOutputs, srv.getAccountOutputs)
	// GET request format: 'get_chain_output?chainid=<hex-encoded chain ID>'
	http.HandleFunc(api.PathGetChainOutput, srv.getChainOutput)
	// GET request format: 'get_output?id=<hex-encoded output ID>'
	http.HandleFunc(api.PathGetOutput, srv.getOutput)
	// GET request format: 'query_txid_status?txid=<hex-encoded transaction ID>'
	http.HandleFunc(api.PathQueryTxIDStatus, srv.queryTxIDStatus)
	// GET request format: 'inclusion?id=<hex-encoded output ID>'
	http.HandleFunc(api.PathGetOutputInclusion, srv.getOutputInclusion)
	// POST request format 'submit_nowait'. Feedback only on parsing error, otherwise async posting
	http.HandleFunc(api.PathSubmitTransaction, srv.submitTx)
	// GET sync info from the node
	http.HandleFunc(api.PathGetSyncInfo, srv.getSyncInfo)
	// GET sync info from the node
	http.HandleFunc(api.PathGetNodeInfo, srv.getNodeInfo)
}

func getLedgerID(w http.ResponseWriter, r *http.Request) {
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

	oData, err := srv.HeaviestStateForLatestTimeSlot().GetUTXOsLockedInAccount(accountable.AccountID())
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

	oData, err := srv.HeaviestStateForLatestTimeSlot().GetChainOutput(&chainID)
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	resp := &api.ChainOutput{
		OutputID:   oData.ID.StringHex(),
		OutputData: hex.EncodeToString(oData.Output.Bytes()),
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
	oData, found := srv.HeaviestStateForLatestTimeSlot().GetUTXO(&oid)
	if !found {
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

func (srv *Server) getOutputInclusion(w http.ResponseWriter, r *http.Request) {
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
	writeErr(w, fmt.Sprintf("getOutputInclusion(%s): not implemented", oid.StringShort()))

	//type branchState struct {
	//	vid *utangle_old.WrappedTx
	//	rdr multistate.SugaredStateReader
	//}
	//allBranches := make([]branchState, 0)
	//err = ut.ForEachBranchStateDescending(ut.LatestTimeSlot(), func(vid *utangle_old.WrappedTx, rdr multistate.SugaredStateReader) bool {
	//	allBranches = append(allBranches, branchState{
	//		vid: vid,
	//		rdr: rdr,
	//	})
	//	return true
	//})
	//if err != nil {
	//	writeErr(w, err.Error())
	//	return
	//}
	//resp := &api.OutputData{
	//	Inclusion: make([]api.InclusionDataEncoded, len(allBranches)),
	//}
	//
	//for i, bs := range allBranches {
	//	resp.Inclusion[i] = api.InclusionDataEncoded{
	//		BranchID: bs.vid.ID().StringHex(),
	//		Coverage: bs.vid.LedgerCoverage(ut),
	//		Included: bs.rdr.HasUTXO(&oid),
	//	}
	//}
	//
	//respBin, err := json.MarshalIndent(resp, "", "  ")
	//if err != nil {
	//	writeErr(w, err.Error())
	//	return
	//}
	//_, err = w.Write(respBin)
	//util.AssertNoError(err)
}

const (
	maxTxUploadSize            = 64 * (1 << 10)
	defaultTxAppendWaitTimeout = 10 * time.Second
	maxTxAppendWaitTimeout     = 2 * time.Minute
)

func (srv *Server) submitTx(w http.ResponseWriter, r *http.Request) {
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
	if err = srv.SubmitTxBytesFromAPI(slices.Clip(txBytes)); err != nil {
		writeErr(w, fmt.Sprintf("submit_tx: %v", err))
		return
	}
	writeOk(w)
}

func (srv *Server) getSyncInfo(w http.ResponseWriter, r *http.Request) {
	writeErr(w, "getSyncInfo: not implemented")
	//syncInfo := ut.SyncData().GetSyncInfo()
	//resp := api.SyncInfo{
	//	Synced:       syncInfo.Synced,
	//	InSyncWindow: syncInfo.InSyncWindow,
	//	PerSequencer: make(map[string]api.SequencerSyncInfo),
	//}
	//for seqID, si := range syncInfo.PerSequencer {
	//	resp.PerSequencer[seqID.StringHex()] = api.SequencerSyncInfo{
	//		Synced:           si.Synced,
	//		LatestBookedSlot: si.LatestBookedSlot,
	//		LatestSeenSlot:   si.LatestSeenSlot,
	//	}
	//}
	//respBin, err := json.MarshalIndent(resp, "", "  ")
	//if err != nil {
	//	writeErr(w, err.Error())
	//	return
	//}
	//_, err = w.Write(respBin)
	//util.AssertNoError(err)
}

func (srv *Server) getNodeInfo(w http.ResponseWriter, r *http.Request) {
	nodeInfo := srv.GetNodeInfo()
	respBin, err := json.MarshalIndent(nodeInfo, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *Server) queryTxIDStatus(w http.ResponseWriter, r *http.Request) {
	lst, ok := r.URL.Query()["txid"]
	if !ok || len(lst) != 1 {
		writeErr(w, "wrong parameter in request 'get_txid_status'")
		return
	}
	txid, err := ledger.TransactionIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, err.Error())
		return
	}

	resp := api.QueryTxIDStatus{
		TxID: lst[0],
	}
	resp.Mode, resp.Status, resp.Err = srv.QueryTxIDStatus(&txid)

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
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
