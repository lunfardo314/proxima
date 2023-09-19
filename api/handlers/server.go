package handlers

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

// OutputList is returned by 'get_account_outputs'
type OutputList struct {
	// empty string when no error
	Error string `json:"error,omitempty"`
	// key is hex-encoded outputID bytes
	// value is hex-encoded raw output data
	Outputs map[string]string `json:"outputs,omitempty"`
}

func RegisterHandlers(ut *utangle.UTXOTangle) {
	// request format: 'get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc("/get_account_outputs", getAccountOutputsHandle(ut))
	http.HandleFunc("/get_chain_output", getChainOutputHandle(ut))
	http.HandleFunc("/get_output", getOutputHandle(ut))
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
		resp := &OutputList{}
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

// ChainOutput is returned by 'get_chain_output'
type ChainOutput struct {
	// empty string when no error
	Error string `json:"error,omitempty"`
	// hex-encoded outputID
	OutputID string `json:"output_id,omitempty"`
	// hex-encoded output data
	OutputData string `json:"output_data,omitempty"`
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
		resp := &ChainOutput{
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

// OutputData is returned by 'get_output'
type OutputData struct {
	// empty string when no error
	Error string `json:"error,omitempty"`
	// hex-encoded output data
	OutputData string `json:"output_data,omitempty"`
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
		resp := &OutputData{
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
	respBytes, err := json.Marshal(&OutputList{Error: errStr})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = w.Write(respBytes)
	util.AssertNoError(err)
}
