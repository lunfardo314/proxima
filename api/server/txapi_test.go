package server

import (
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
	// Layout: [0] amounts, [1] index-values placeholder, [2] lock, [3] chain.
	assert.Equal(t, 4, len(ret.Constraints))
	assert.Equal(t, "amounts(31_415_926_535)", ret.Constraints[0])
	assert.Equal(t, "index values: <empty>", ret.Constraints[1])
	assert.Equal(t, addr.String(), ret.Constraints[2])
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
