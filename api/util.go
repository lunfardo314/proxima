package api

import (
	"encoding/json"
	"net/http"
)

func WriteErr(w http.ResponseWriter, errStr string) {
	respBytes, err := json.Marshal(&Error{Error: errStr})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// a failed write means the client hung up; nothing to do here, and never fatal
	_, _ = w.Write(respBytes)
}

func WriteOk(w http.ResponseWriter) {
	respBytes, err := json.Marshal(&Error{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// a failed write means the client hung up; nothing to do here, and never fatal
	_, _ = w.Write(respBytes)
}

func SetHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
