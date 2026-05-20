package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/tests"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileScript(t *testing.T) {
	srv := &server{}

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/compile_script?source=slice(0x0102,0,0)", nil)
	w := httptest.NewRecorder()

	// Call handler
	srv.compileScript(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.Bytecode
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	assert.Equal(t, "1082010281008100", ret.Bytecode) // Hex for "compiledBytecode"

}

func TestDecompileBytecode(t *testing.T) {
	srv := &server{}

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/decompile_bytecode?bytecode=1082010281008100", nil)
	w := httptest.NewRecorder()

	// Call handler
	srv.decompileBytecode(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.ScriptSource
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	assert.Equal(t, "slice(0x0102,0,0)", ret.Source) // Hex for "compiledBytecode"
}

func TestParseOutputData(t *testing.T) {
	srv := &server{}

	const amount = uint64(31415926535)
	addr := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(100))
	chainID := base.RandomChainID()
	cc := ledger.NewChainConstraint(chainID, 1, 0, 0, 0, 1, 0)
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount).
			WithLock(addr)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	})
	oDataStr := hex.EncodeToString(o.Bytes())
	reqStr := fmt.Sprintf("/txapi/v1/parse_output_data?output_data=%s&human_readable=", oDataStr)

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, reqStr, nil)
	w := httptest.NewRecorder()

	// Call handler
	srv.parseOutputData(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.ParsedOutput
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)

	assert.Equal(t, oDataStr, ret.Data)
	assert.Equal(t, amount, ret.Amount)
	assert.Equal(t, chainID.StringHex(), ret.ChainID)
	// Layout: [0] amounts, [1] index-values tuple (holderID), [2] lock
	// (0-arg sigLock symbol — no embedded data), [3] chain constraint.
	assert.Equal(t, 4, len(ret.Constraints))
	assert.Equal(t, "amounts(31_415_926_535)", ret.Constraints[0])
	assert.Equal(t, "index values: [0x"+hex.EncodeToString(addr[:])+"]", ret.Constraints[1])
	assert.Equal(t, ledger.SigLockName, ret.Constraints[2])
	assert.Equal(t, cc.String(), ret.Constraints[3])
}

func TestParseOutput(t *testing.T) {
	env, _, err := tests.StartTestEnv()
	require.NoError(t, err)

	mockServer := &server{
		environment: env,
	}

	genesisOut := ledger.GenesisStemOutput()
	oDataStr := hex.EncodeToString(genesisOut.Output.Bytes())

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/parse_output?output_id=%s", genesisOut.ID.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.parseOutput(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.ParsedOutput
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)

	assert.Equal(t, oDataStr, ret.Data)
	// Stem layout: [0] amounts, [1] index-values placeholder, [2] stem lock.
	assert.Equal(t, 3, len(ret.Constraints))
}

func TestGetTXBytes(t *testing.T) {

	env, txid, err := tests.StartTestEnv()
	require.NoError(t, err)

	// Mock server
	mockServer := &server{
		environment: env,
	}

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/get_txbytes?txid=%s", txid.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.getTxBytes(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.TxBytes
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	//assert.Equal(ret.TxBytes, txBytes
	assert.NotEmpty(t, ret.TxBytes)
}

func TestGetParsedTransaction(t *testing.T) {
	//privKey := genesisPrivateKey
	env, txid, err := tests.StartTestEnv()
	require.NoError(t, err)

	// Mock server
	mockServer := &server{
		environment: env,
	}

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/get_parsed_transaction?txid=%s", txid.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.getParsedTransaction(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.TransactionJSONAble
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	// TotalAmount includes branch inflation (VRF-based, non-zero on branch transactions)
	assert.True(t, ret.TotalAmount > ledger.DefaultInitialSupply-1)
	assert.Equal(t, ret.IsBranch, true)
	assert.Equal(t, len(ret.Inputs), 2)
	assert.Equal(t, len(ret.Outputs), 5)
}

func TestGetVertexDep(t *testing.T) {
	env, txid, err := tests.StartTestEnv()
	require.NoError(t, err)

	// Mock server
	mockServer := &server{
		environment: env,
	}

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/get_vertex_dep?txid=%s", txid.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.getVertexWithDependencies(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	t.Logf("JSON data:\n%s", string(data))
	var ret api.VertexWithDependencies
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	txidBack, err := base.TransactionIDFromHexString(ret.ID)
	assert.NoError(t, err)
	assert.EqualValues(t, ret.SequencerID, ledger.BoostrapSequencerIDHex)
	assert.EqualValues(t, *txid, txidBack)
	assert.True(t, txid.IsSequencerTransaction())
	assert.True(t, txid.IsBranchTransaction())
	// TotalAmount includes branch inflation (VRF-based, non-zero on branch transactions)
	assert.True(t, ret.TotalInflation > 0)
	assert.EqualValues(t, ledger.DefaultInitialSupply-1+ret.TotalInflation, ret.TotalAmount)
	assert.True(t, ret.SequencerInputTxIndex != nil && *ret.SequencerInputTxIndex == 0)
	assert.True(t, ret.StemInputTxIndex != nil && *ret.StemInputTxIndex == 0)
	assert.EqualValues(t, 1, len(ret.Inputs))
	assert.EqualValues(t, 0, len(ret.Endorsements))
}

// use this function is avoid crash for err = nil
func (srv *server) AssertNoError(err error, prefix ...string) {
	util.AssertNoError(err, prefix...)
}

// ---------------------------------------------------------------------
// submitTx tests
// ---------------------------------------------------------------------

// recordingEnv wraps a real environment and records every
// SubmitTxBytesFromAPI invocation. Used to verify that the submit
// stage either fires (default) or is skipped (validate_only=true).
type recordingEnv struct {
	environment
	submitted [][]byte
}

func (r *recordingEnv) SubmitTxBytesFromAPI(b []byte) {
	r.submitted = append(r.submitted, append([]byte(nil), b...))
}

// doSubmit runs the submitTx handler against srv with the given JSON
// body (or nil for raw-bytes-style misuse), returning the decoded
// response.
func doSubmit(t *testing.T, srv *server, body []byte) api.SubmitTxResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, api.PathSubmitTransaction, bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.submitTx(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out api.SubmitTxResponse
	require.NoError(t, json.Unmarshal(raw, &out), "decode response: %s", string(raw))
	return out
}

// mustMarshal is a test helper.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// TestSubmitTx_BadJSON verifies a malformed body fails at stage="parse".
func TestSubmitTx_BadJSON(t *testing.T) {
	srv := &server{environment: &recordingEnv{}}
	out := doSubmit(t, srv, []byte("not-json"))
	require.False(t, out.OK)
	require.Equal(t, api.SubmitStageParse, out.Stage)
	require.NotEmpty(t, out.Error)
}

// TestSubmitTx_BadHex verifies a non-hex tx_bytes fails at stage="parse".
func TestSubmitTx_BadHex(t *testing.T) {
	srv := &server{environment: &recordingEnv{}}
	body := mustMarshal(t, api.SubmitTxRequest{TxBytes: "not-hex!!"})
	out := doSubmit(t, srv, body)
	require.False(t, out.OK)
	require.Equal(t, api.SubmitStageParse, out.Stage)
}

// TestSubmitTx_ParseFails verifies garbage hex bytes that don't form a
// valid transaction fail at stage="parse".
func TestSubmitTx_ParseFails(t *testing.T) {
	srv := &server{environment: &recordingEnv{}}
	body := mustMarshal(t, api.SubmitTxRequest{TxBytes: "deadbeef"})
	out := doSubmit(t, srv, body)
	require.False(t, out.OK)
	require.Equal(t, api.SubmitStageParse, out.Stage)
}

// TestSubmitTx_HappyPath uses the test env's distribution tx (already
// stored in TxBytesStore). With no consumed_utxos it goes parse → submit
// (recorded by the fake env). Returns ok:true and the matching tx_id.
func TestSubmitTx_HappyPath(t *testing.T) {
	env, txid, err := tests.StartTestEnv()
	require.NoError(t, err)
	txBytes := env.TxBytesStore().GetTxBytes(txid)
	require.NotEmpty(t, txBytes)

	rec := &recordingEnv{environment: env}
	srv := &server{environment: rec}

	body := mustMarshal(t, api.SubmitTxRequest{TxBytes: hex.EncodeToString(txBytes)})
	out := doSubmit(t, srv, body)
	require.True(t, out.OK, "stage=%s error=%s", out.Stage, out.Error)
	require.Equal(t, txid.StringHex(), out.TxID)
	require.Len(t, rec.submitted, 1, "submit stage must enqueue exactly once")
	require.Equal(t, txBytes, rec.submitted[0])
}

// TestSubmitTx_ValidateOnly verifies validate_only=true skips the
// submit stage entirely — the fake env records zero calls.
func TestSubmitTx_ValidateOnly(t *testing.T) {
	env, txid, err := tests.StartTestEnv()
	require.NoError(t, err)
	txBytes := env.TxBytesStore().GetTxBytes(txid)
	require.NotEmpty(t, txBytes)

	rec := &recordingEnv{environment: env}
	srv := &server{environment: rec}

	body := mustMarshal(t, api.SubmitTxRequest{
		TxBytes:      hex.EncodeToString(txBytes),
		ValidateOnly: true,
	})
	out := doSubmit(t, srv, body)
	require.True(t, out.OK, "stage=%s error=%s", out.Stage, out.Error)
	require.Equal(t, txid.StringHex(), out.TxID)
	require.Len(t, rec.submitted, 0, "validate_only must skip submission")
}

// TestSubmitTx_ConsumedUTXOsLengthMismatch verifies that when
// consumed_utxos is non-empty but its length doesn't equal NumInputs,
// the handler fails at stage="full" without calling submit.
func TestSubmitTx_ConsumedUTXOsLengthMismatch(t *testing.T) {
	env, txid, err := tests.StartTestEnv()
	require.NoError(t, err)
	txBytes := env.TxBytesStore().GetTxBytes(txid)
	require.NotEmpty(t, txBytes)

	rec := &recordingEnv{environment: env}
	srv := &server{environment: rec}

	// Provide 99 garbage entries — guaranteed to mismatch any real
	// NumInputs (max is 256, but the distribution tx has only a
	// handful).
	wrongLength := make([]string, 99)
	for i := range wrongLength {
		wrongLength[i] = "00"
	}
	body := mustMarshal(t, api.SubmitTxRequest{
		TxBytes:       hex.EncodeToString(txBytes),
		ConsumedUTXOs: wrongLength,
	})
	out := doSubmit(t, srv, body)
	require.False(t, out.OK)
	require.Equal(t, api.SubmitStageFull, out.Stage)
	require.Contains(t, out.Error, "consumed_utxos length")
	require.Len(t, rec.submitted, 0, "must not submit when validation fails")
}
