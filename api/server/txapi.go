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
	// '/txapi/v1/compile_script?source=<script source in EasyFL>'
	srv.addHandler(api.PathCompileScript, srv.compileScript)
	// '/txapi/v1/decompile_bytecode?bytecode=<hex-encoded bytecode>'
	srv.addHandler(api.PathDecompileBytecode, srv.decompileBytecode)
	// '/txapi/v1/parse_output?output_id=<hex-encoded output ID>'
	srv.addHandler(api.PathParseOutput, srv.parseOutput)
	// '/txapi/v1/parse_output_data?output_data=<hex-encoded output binary>'
	srv.addHandler(api.PathParseOutputData, srv.parseOutputData)
	// '/txapi/v1/get_txbytes?txid=<hex-encoded transaction ID>'
	srv.addHandler(api.PathGetTxBytes, srv.getTxBytes)
	// '/txapi/v1/get_parsed_transaction?txid=<hex-encoded transaction ID>'
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

	tx, err := transaction.FromBytes(txBytes, transaction.BaseValidation())
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
