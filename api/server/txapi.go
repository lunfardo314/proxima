package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
)

func (srv *server) registerTxAPIHandlers() {
	// Compiles EasyFL script in the context of the ledger of the node and returns bytecode
	// '/txapi/v1/compile_script?source=<script source in EasyFL>'
	srv.addHandler(api.PathCompileScript, srv.compileScript)
	// Decompiles bytecode in the context of the ledger of the node to EasyFL script
	// '/txapi/v1/decompile_bytecode?bytecode=<hex-encoded bytecode>'
	srv.addHandler(api.PathDecompileBytecode, srv.decompileBytecode)
	// By given output ID, finds raw output data on LRB state, parses the it as lazyarray
	// and decompiles each of constraint scripts. Returns list of decompiled constraint scripts
	// '/txapi/v1/parse_output?output_id=<hex-encoded output ID>'
	srv.addHandler(api.PathParseOutput, srv.parseOutput)
	// By given raw data of the output, parses it as lazyarray
	// and decompiles each of constraint scripts. Returns list of decompiled constraint scripts
	// Essential difference with the parse-output is that it does not need to assume particular LRB
	// '/txapi/v1/parse_output_data?output_data=<hex-encoded output binary>'
	srv.addHandler(api.PathParseOutputData, srv.parseOutputData)
	// By given transaction ID, returns raw transaction bytes (canonical form of tx) and metadata (if it exists)
	// '/txapi/v1/get_txbytes?txid=<hex-encoded transaction ID>'
	srv.addHandler(api.PathGetTxBytes, srv.getTxBytes)
	// By the given transaction ID, returns parsed transaction in JSON form. The JSON form contains all elements
	// of the transaction except signature, but it is not a canonical form. Primary purpose of JSON form of the transaction
	// is to use it in frontends, like explorers and visualizers.
	// '/txapi/v1/get_parsed_transaction?txid=<hex-encoded transaction ID>'
	srv.addHandler(api.PathGetParsedTransaction, srv.getParsedTransaction)
	// By the given transaction ID, returns compressed for of the DAG vertex. Its primary use is DAG visualizers
	// '/txapi/v1/get_vertex_dep?txid=<hex-encoded transaction ID>'
	srv.addHandler(api.PathGetVertexWithDependencies, srv.getVertexWithDependencies)
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
	srv.AssertNoError(err)
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

	resp := api.ScriptSource{Source: src}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
}

func (srv *server) parseOutput(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	lst, ok := r.URL.Query()["output_id"]
	if !ok || len(lst) != 1 {
		writeErr(w, "hex encoded output data is expected")
		return
	}

	oid, err := ledger.OutputIDFromHexString(lst[0])
	if err != nil {
		writeErr(w, fmt.Sprintf("can't parse output ID: %v", err))
		return
	}

	var o *ledger.Output
	err = srv.withLRB(func(rdr multistate.SugaredStateReader) error {
		o = rdr.GetOutput(&oid)
		if o == nil {
			return fmt.Errorf("no found %s in LRB", oid.String())
		}
		return nil
	})
	if err != nil {
		writeErr(w, fmt.Sprintf("can't get output in LRB: %v", err))
		return
	}

	resp := api.ParsedOutput{
		Data:        hex.EncodeToString(o.Bytes()),
		Constraints: o.LinesPlain().Slice(),
		Amount:      o.Amount(),
	}
	if cc, pos := o.ChainConstraint(); pos != 0xff {
		var chainID ledger.ChainID
		if cc.IsOrigin() {
			chainID = ledger.MakeOriginChainID(&oid)
		} else {
			chainID = cc.ID
		}
		resp.ChainID = hex.EncodeToString(chainID[:])
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
}

func (srv *server) parseOutputData(w http.ResponseWriter, r *http.Request) {
	setHeader(w)

	lst, ok := r.URL.Query()["output_data"]
	if !ok || len(lst) != 1 {
		writeErr(w, "hex encoded output data is expected")
		return
	}

	outBin, err := hex.DecodeString(lst[0])
	if err != nil {
		writeErr(w, fmt.Sprintf("can't decode hex string: '%v'", err))
		return
	}

	o, err := ledger.OutputFromBytesReadOnly(outBin)
	if err != nil {
		writeErr(w, fmt.Sprintf("can't parse output: '%v'", err))
		return
	}

	resp := api.ParsedOutput{
		Data:        hex.EncodeToString(outBin),
		Constraints: o.LinesPlain().Slice(),
		Amount:      o.Amount(),
	}
	if cc, pos := o.ChainConstraint(); pos != 0xff {
		resp.ChainID = hex.EncodeToString(cc.ID[:])
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
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
	srv.AssertNoError(err)
}

func (srv *server) getParsedTransaction(w http.ResponseWriter, r *http.Request) {
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

	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		writeErr(w, fmt.Sprintf("internal error while parsing transaction: '%v'", err))
		return
	}

	resp := api.JSONAbleFromTransaction(tx)
	if metadata != nil {
		resp.TxMetadata = metadata.JSONAble()
	}

	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
}

func (srv *server) getVertexWithDependencies(w http.ResponseWriter, r *http.Request) {
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

	_, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		writeErr(w, fmt.Sprintf("error while parsing DB data: '%v'", err))
		return
	}

	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		writeErr(w, fmt.Sprintf("internal error while parsing transaction: '%v'", err))
		return
	}
	resp := api.VertexWithDependenciesFromTransaction(tx)
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	srv.AssertNoError(err)
}
