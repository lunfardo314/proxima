package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestEval_MixedBatch verifies that the /eval handler evaluates a
// mixed batch of valid + invalid closed formulas, emits per-entry
// results in input order, and uses plain hex (no "0x" prefix) for
// the value field.
func TestEval_MixedBatch(t *testing.T) {
	srv := &server{}

	body, err := json.Marshal(api.EvalRequest{
		Sources: []string{
			"constAttachmentCostBudget",
			"this_symbol_does_not_exist", // forces a server-side error
			"constTransactionPace",
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, api.PathEval, bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.eval(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out api.EvalResponse
	require.NoError(t, json.Unmarshal(data, &out))
	require.Len(t, out.Results, 3)

	// Entry 0: valid AttachmentCostBudget (uint64).
	require.Empty(t, out.Results[0].Error)
	require.NotContains(t, out.Results[0].Value, "0x", "value must be raw hex (no 0x prefix)")
	binBudget, err := hex.DecodeString(out.Results[0].Value)
	require.NoError(t, err)
	got, err := easyfl_util.Uint64FromBytes(binBudget)
	require.NoError(t, err)
	require.Equal(t, uint64(ledger.L(base.MaxSlot).Constants.AttachmentCostBudget), got)

	// Entry 1: failure surfaces in Error, value left empty.
	require.NotEmpty(t, out.Results[1].Error)
	require.Empty(t, out.Results[1].Value)

	// Entry 2: valid TransactionPace.
	require.Empty(t, out.Results[2].Error)
	binPace, err := hex.DecodeString(out.Results[2].Value)
	require.NoError(t, err)
	pace, err := easyfl_util.Uint64FromBytes(binPace)
	require.NoError(t, err)
	require.Equal(t, uint64(ledger.L(base.MaxSlot).Constants.TransactionPace), pace)
}

// TestEval_BadMethod rejects GET on the eval path.
func TestEval_BadMethod(t *testing.T) {
	srv := &server{}
	req := httptest.NewRequest(http.MethodGet, api.PathEval, nil)
	w := httptest.NewRecorder()
	srv.eval(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestEval_EmptyBatch returns an empty results slice without error.
func TestEval_EmptyBatch(t *testing.T) {
	srv := &server{}
	body, _ := json.Marshal(api.EvalRequest{Sources: nil})
	req := httptest.NewRequest(http.MethodPost, api.PathEval, bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.eval(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, _ := io.ReadAll(resp.Body)
	var out api.EvalResponse
	require.NoError(t, json.Unmarshal(data, &out))
	require.Empty(t, out.Results)
	require.Empty(t, out.Error.Error)
}
