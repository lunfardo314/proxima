package server

import (
	"net/http"

	"github.com/lunfardo314/proxima/api"
)

func (srv *server) registerTxAPIHandlers() {
	srv.addHandler(api.PathCompileScript, srv.compileScript)
	srv.addHandler(api.PathDecompileBytecode, srv.decompileBytecode)
	srv.addHandler(api.PathParseOutput, srv.parseOutput)
	srv.addHandler(api.PathGetTxBytes, srv.getTxBytes)
	srv.addHandler(api.PathGetParsedTransaction, srv.getParsedTransaction)
}

func (srv *server) compileScript(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}

func (srv *server) decompileBytecode(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}

func (srv *server) parseOutput(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}

func (srv *server) getTxBytes(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}

func (srv *server) getParsedTransaction(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	writeNotImplemented(w)
}
