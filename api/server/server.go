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
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/workflow"
)

func registerHandlers(wFlow *workflow.Workflow, getNodeInfo func() *global.NodeInfo) {
	// GET request format: 'get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc(api.PathGetAccountOutputs, getAccountOutputsHandle(wFlow.UTXOTangle()))
	// GET request format: 'get_chain_output?chainid=<hex-encoded chain ID>'
	http.HandleFunc(api.PathGetChainOutput, getChainOutputHandle(wFlow.UTXOTangle()))
	// GET request format: 'get_output?id=<hex-encoded output ID>'
	http.HandleFunc(api.PathGetOutput, getOutputHandle(wFlow.UTXOTangle()))
	// GET request format: 'inclusion?id=<hex-encoded output ID>'
	http.HandleFunc(api.PathGetOutputInclusion, getOutputInclusionHandle(wFlow.UTXOTangle()))
	// POST request format 'submit_wait'. Waiting until added to utangle or rejected
	http.HandleFunc(api.PathSubmitTransactionWait, submitTxHandle(wFlow, true))
	// POST request format 'submit_nowait'. Async posting to utangle. No feedback in case of wrong tx
	http.HandleFunc(api.PathSubmitTransactionNowait, submitTxHandle(wFlow, false))
	// GET sync info from the node
	http.HandleFunc(api.PathGetSyncInfo, getSyncInfoHandle(wFlow.UTXOTangle()))
	// GET sync info from the node
	http.HandleFunc(api.PathGetNodeInfo, getNodeInfoHandle(getNodeInfo))
}

func getAccountOutputsHandle(ut *utangle.UTXOTangle) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		lst, ok := r.URL.Query()["accountable"]
		if !ok || len(lst) != 1 {
			writeErr(w, "wrong parameters in request 'get_account_outputs'")
			return
		}
		accountable, err := core.AccountableFromSource(lst[0])
		if err != nil {
			writeErr(w, err.Error())
			return
		}

		oData, err := ut.HeaviestStateForLatestTimeSlot().GetUTXOsLockedInAccount(accountable.AccountID())
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
}

func getChainOutputHandle(ut *utangle.UTXOTangle) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		lst, ok := r.URL.Query()["chainid"]
		if !ok || len(lst) != 1 {
			writeErr(w, "wrong parameters in request 'get_chain_output'")
			return
		}
		chainID, err := core.ChainIDFromHexString(lst[0])
		if err != nil {
			writeErr(w, err.Error())
			return
		}

		oData, err := ut.HeaviestStateForLatestTimeSlot().GetChainOutput(&chainID)
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
}

func getOutputHandle(ut *utangle.UTXOTangle) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		lst, ok := r.URL.Query()["id"]
		if !ok || len(lst) != 1 {
			writeErr(w, "wrong parameter in request 'get_output'")
			return
		}
		oid, err := core.OutputIDFromHexString(lst[0])
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		oData, found := ut.HeaviestStateForLatestTimeSlot().GetUTXO(&oid)
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
}

func getOutputInclusionHandle(ut *utangle.UTXOTangle) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		lst, ok := r.URL.Query()["id"]
		if !ok || len(lst) != 1 {
			writeErr(w, "wrong parameter in request 'get_output'")
			return
		}
		oid, err := core.OutputIDFromHexString(lst[0])
		if err != nil {
			writeErr(w, err.Error())
			return
		}

		type branchState struct {
			vid *utangle.WrappedTx
			rdr multistate.SugaredStateReader
		}
		allBranches := make([]branchState, 0)
		err = ut.ForEachBranchStateDescending(ut.LatestTimeSlot(), func(vid *utangle.WrappedTx, rdr multistate.SugaredStateReader) bool {
			allBranches = append(allBranches, branchState{
				vid: vid,
				rdr: rdr,
			})
			return true
		})
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		resp := &api.OutputData{
			Inclusion: make([]api.InclusionDataEncoded, len(allBranches)),
		}

		for i, bs := range allBranches {
			resp.Inclusion[i] = api.InclusionDataEncoded{
				BranchID: bs.vid.ID().StringHex(),
				Coverage: bs.vid.LedgerCoverage(ut),
				Included: bs.rdr.HasUTXO(&oid),
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
}

const (
	maxTxUploadSize            = 64 * (1 << 10)
	defaultTxAppendWaitTimeout = 10 * time.Second
	maxTxAppendWaitTimeout     = 2 * time.Minute
)

func submitTxHandle(wFlow *workflow.Workflow, wait bool) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
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
		txBytes = util.CloneExactCap(txBytes)

		if wait {
			_, err = wFlow.TransactionInWaitAppend(txBytes, timeout)
		} else {
			err = wFlow.TransactionIn(txBytes)
		}
		if err != nil {
			writeErr(w, fmt.Sprintf("submit_tx: %v", err))
			return
		}
		writeOk(w)
	}
}

func getSyncInfoHandle(ut *utangle.UTXOTangle) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		syncInfo := ut.SyncData().GetSyncInfo()
		resp := api.SyncInfo{
			Synced:       syncInfo.Synced,
			InSyncWindow: syncInfo.InSyncWindow,
			PerSequencer: make(map[string]api.SequencerSyncInfo),
		}
		for seqID, si := range syncInfo.PerSequencer {
			resp.PerSequencer[seqID.StringHex()] = api.SequencerSyncInfo{
				Synced:           si.Synced,
				LatestBookedSlot: si.LatestBookedSlot,
				LatestSeenSlot:   si.LatestSeenSlot,
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
}

func getNodeInfoHandle(getNodeInfo func() *global.NodeInfo) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeInfo := getNodeInfo()
		respBin, err := json.MarshalIndent(nodeInfo, "", "  ")
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		_, err = w.Write(respBin)
		util.AssertNoError(err)
	}
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

func RunOn(addr string, w *workflow.Workflow, getNodeInfo func() *global.NodeInfo) {
	registerHandlers(w, getNodeInfo)
	err := http.ListenAndServe(addr, nil)
	util.AssertNoError(err)
}
