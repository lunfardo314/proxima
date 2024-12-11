package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/tests"
	"github.com/lunfardo314/proxima/util"
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
	assert.Equal(t, "1182010281008100", ret.Bytecode) // Hex for "compiledBytecode"

}

func TestDecompileBytecode(t *testing.T) {
	srv := &server{}

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/decompile_bytecode?bytecode=1182010281008100", nil)
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

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/parse_output_data?output_data=40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff", nil)
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

	assert.Equal(t, ret.Data, "40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff")
	assert.Equal(t, ret.Amount, uint64(0x38d7ff693a50e))
	assert.Equal(t, ret.ChainID, "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc")
	assert.Equal(t, len(ret.Constraints), 6)
	assert.Equal(t, ret.Constraints[0], "amount(u64/1000005667366158)")
}

func TestParseOutput(t *testing.T) {
	env, _, err := tests.StartTestEnv()
	require.NoError(t, err)

	mockServer := &server{
		environment: env,
	}

	genesisOut := ledger.GenesisStemOutput()

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

	assert.Equal(t, ret.Data, "40020b45ab8800000000000000002445c6a1000000000000000000000000000000000000000000000000000000000000000000")
	assert.Equal(t, len(ret.Constraints), 2)
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
	assert.Equal(t, ret.TotalAmount, uint64(0x38d7ea4c68000))
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

	var ret api.VertexWithDependencies
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	assert.Equal(t, ret.TotalAmount, uint64(0x38d7ea4c68000))
	assert.Equal(t, ret.IsBranch, true)
	assert.Equal(t, len(ret.InputDependencies), 2)
}

// use this function is avoid crash for err = nil
func (srv *server) AssertNoError(err error, prefix ...string) {
	util.AssertNoError(err, prefix...)
}
