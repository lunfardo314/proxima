package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
)

func (srv *server) registerTxAPIHandlers() {
	// '/txapi/v1/compile_script?source=<script source in EasyFL>'
	srv.addHandler(api.PathCompileScript, srv.compileScript)
	// '/txapi/v1/decompile_bytecode?bytecode=<hex-encoded bytecode>'
	srv.addHandler(api.PathDecompileBytecode, srv.decompileBytecode)
	srv.addHandler(api.PathParseOutput, srv.parseOutput)
	// '/txapi/v1/get_txbytes?txid=<hex-encoded transaction ID>'
	srv.addHandler(api.PathGetTxBytes, srv.getTxBytes)
	srv.addHandler(api.PathGetParsedTransaction, srv.getParsedTransaction)
}

func (srv *server) compileScript(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	lst, ok := r.URL.Query()["source"]
	if !ok || len(lst) != 1 {
		writeErr(w, "script source is expected")
		return
	}

	_, _, bytecode, err := ledger.L().CompileExpression(lst[0])
	if err != nil {
		writeErr(w, fmt.Sprintf("EasyFL compile error: '%v'", err))
		return
	}

	resp := api.Bytecode{Bytecode: hex.EncodeToString(bytecode)}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
}

func (srv *server) decompileBytecode(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	lst, ok := r.URL.Query()["bytecode"]
	if !ok || len(lst) != 1 {
		writeErr(w, "hex encoded bytecode is expected")
		return
	}

	bytecode, err := hex.DecodeString(lst[0])
	if err != nil {
		writeErr(w, fmt.Sprintf("can't decode hex string: '%v'", err))
		return
	}

	src, err := ledger.L().DecompileBytecode(bytecode)
	if err != nil {
		writeErr(w, fmt.Sprintf("can't decompile bytecode: '%v'", err))
		return
	}

	resp := api.ScriptSource{ScriptSource: src}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
}

func (srv *server) parseOutput(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}

func (srv *server) getTxBytes(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	var txid ledger.TransactionID
	var err error

	lst, ok := r.URL.Query()["txid"]
	if !ok || len(lst) != 1 {
		writeErr(w, "hex encoded transaction ID expected")
		return
	}
	txid, err = ledger.TransactionIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, fmt.Sprintf("failed to parse transaction ID from hex encoded string: '%v'", err))
		return
	}

	txBytesWithMetadata := srv.TxBytesStore().GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMetadata) == 0 {
		writeErr(w, fmt.Sprintf("transaction %s has not been found in the txBytesStore", txid.String()))
		return
	}

	txBytes, metadata, err := txmetadata.ParseTxMetadata(txBytesWithMetadata)
	if err != nil {
		writeErr(w, fmt.Sprintf("error while parsing DB data: '%v'", err))
		return
	}

	resp := api.TxBytes{
		TxBytes:    hex.EncodeToString(txBytes),
		TxMetadata: metadata.JSONAble(),
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
}

func (srv *server) getParsedTransaction(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}
