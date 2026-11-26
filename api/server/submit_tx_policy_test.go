package server

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// inputsState serves the given consumed outputs as the LRB state, so submit_tx finds them there
// with the supplied bytes and runs its full-context stage.
type inputsState struct {
	multistate.IndexedStateReader
	outputs map[base.OutputID][]byte
}

func (s *inputsState) GetUTXO(id base.OutputID) ([]byte, bool) {
	data, ok := s.outputs[id]
	return data, ok
}

// validate_only must refuse what attachment refuses. The fixture (written by
// ledger/tests TestGenerateSubmitPolicyFixture) is a non-canonical on-hold delegation
// transaction that passes ValidateFullContext and fails the node's policy checks; the
// handler must report it at the full stage instead of ok.
func TestSubmitTxRunsNodePolicy(t *testing.T) {
	saved, savedBound := transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot
	transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot = 0, 0
	defer func() {
		transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot = saved, savedBound
	}()
	var f struct {
		TxBytes   string                     `json:"tx_bytes"`
		Consumed  []string                   `json:"consumed_utxos"`
		IDs       []string                   `json:"input_ids"`
		Libraries map[uint32]json.RawMessage `json:"libraries"`
	}
	file, err := os.Open("testdata/submit_policy_fixture.json.gz")
	require.NoError(t, err)
	defer file.Close()
	zr, err := gzip.NewReader(file)
	require.NoError(t, err)
	require.NoError(t, json.NewDecoder(zr).Decode(&f))
	libs := make(map[uint32][]byte, len(f.Libraries))
	for slot, def := range f.Libraries {
		libs[slot] = def
	}
	ledger.InitWithTestingLedgerData()
	ledger.MustInitLibraryCacheFromMap(libs)
	defer ledger.InitWithTestingLedgerData()

	state := &inputsState{outputs: make(map[base.OutputID][]byte)}
	for i, idHex := range f.IDs {
		raw, err := hex.DecodeString(idHex)
		require.NoError(t, err)
		oid, err := base.OutputIDFromBytes(raw)
		require.NoError(t, err)
		data, err := hex.DecodeString(f.Consumed[i])
		require.NoError(t, err)
		state.outputs[oid] = data
	}
	body, err := json.Marshal(api.SubmitTxRequest{TxBytes: f.TxBytes, ConsumedUTXOs: f.Consumed, ValidateOnly: true})
	require.NoError(t, err)
	srv := &server{environment: &stateEnv{rdr: multistate.MakeSugared(state)}}
	rec := httptest.NewRecorder()
	srv.submitTx(rec, httptest.NewRequest(http.MethodPost, api.PathSubmitTransaction, bytes.NewReader(body)))
	var resp api.SubmitTxResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.False(t, resp.OK, "the policy-invalid transaction must be refused: %s", rec.Body.String())
	require.EqualValues(t, api.SubmitStageFull, resp.Stage, rec.Body.String())
	require.Contains(t, rec.Body.String(), "non-canonical state")
}
