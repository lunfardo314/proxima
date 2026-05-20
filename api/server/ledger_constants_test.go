package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestGetLedgerConstants_Default verifies the GET /api/v1/ledger_constants
// handler returns a JSON body that round-trips into
// txbuildercore.Constants and whose values match the active library.
func TestGetLedgerConstants_Default(t *testing.T) {
	srv := &server{}

	req := httptest.NewRequest(http.MethodGet, api.PathGetLedgerConstants, nil)
	w := httptest.NewRecorder()
	srv.getLedgerConstants(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var got txbuildercore.Constants
	require.NoError(t, json.Unmarshal(data, &got))

	want := ledger.L(base.MaxSlot).Constants.ToWalletConstants()
	require.Equal(t, *want, got)

	// Sanity: AttachmentCostBudget is positive on a real library.
	require.Greater(t, got.AttachmentCostBudget, 0)
}

// TestGetLedgerConstants_BadSlot rejects a malformed slot parameter.
func TestGetLedgerConstants_BadSlot(t *testing.T) {
	srv := &server{}

	req := httptest.NewRequest(http.MethodGet, api.PathGetLedgerConstants+"?slot=abc", nil)
	w := httptest.NewRecorder()
	srv.getLedgerConstants(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	// api.WriteErr returns 2xx with an error envelope; assert by content.
	data, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(data), "invalid slot parameter")
}
