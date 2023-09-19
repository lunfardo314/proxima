package server

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

func registerHandlers(ut *utangle.UTXOTangle) {
	// request format: 'get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc(api.PathGetAccountOutputs, getAccountOutputsHandle(ut))
	// request format: 'get_chain_output?chainid=<hex-encoded chain ID>'
	http.HandleFunc(api.PathGetChainOutput, getChainOutputHandle(ut))
	// request format: 'get_output?id=<hex-encoded output ID>'
	http.HandleFunc(api.PathGetOutput, getOutputHandle(ut))
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
			writeErr(w, "output not found")
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

func writeErr(w http.ResponseWriter, errStr string) {
	respBytes, err := json.Marshal(&api.Error{Error: errStr})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = w.Write(respBytes)
	util.AssertNoError(err)
}

func RunOn(addr string, ut *utangle.UTXOTangle) {
	registerHandlers(ut)
	err := http.ListenAndServe(addr, nil)
	util.AssertNoError(err)
}
