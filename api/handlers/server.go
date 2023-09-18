package handlers

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

type OutputList struct {
	// empty string of no error
	Error string `json:"error"`
	// key is hex-encoded outputID bytes
	// value is hex-encoded raw output data
	Outputs map[string]string `json:"outputs"`
}

func RegisterHandlers(ut *utangle.UTXOTangle) {
	// request format: 'get_outputs?accountable=<EasyFL source form of the accountable lock constraint>'
	http.HandleFunc("/get_outputs", getOutputsHandle(ut))
}

func getOutputsHandle(ut *utangle.UTXOTangle) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		lst, ok := r.URL.Query()["accountable"]
		if !ok || len(lst) != 1 {
			writeErr(w, "wrong request")
			return
		}
		addr, err := core.AddressED25519FromSource(lst[0])
		if err != nil {
			writeErr(w, err.Error())
			return
		}

		oData, err := ut.HeaviestStateForLatestTimeSlot().GetUTXOsLockedInAccount(addr.AccountID())
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		resp := &OutputList{
			Outputs: make(map[string]string),
		}
		for _, o := range oData {
			resp.Outputs[o.ID.StringHex()] = hex.EncodeToString(o.OutputData)
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
