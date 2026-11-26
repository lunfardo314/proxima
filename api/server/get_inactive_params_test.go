package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// lrbCountingEnv counts reads of the latest reliable state, so a test can tell whether a
// request was refused before the handler touched the state.
type lrbCountingEnv struct {
	environment
	reader multistate.SugaredStateReader
	calls  int
}

func (e *lrbCountingEnv) LatestReliableState() (multistate.SugaredStateReader, error) {
	e.calls++
	return e.reader, nil
}

// slots_back must be one unsigned 32-bit decimal: negative, out-of-range, empty, non-numeric and
// repeated values are refused before the state is read, instead of wrapping around to another
// threshold. Omitted, zero and every representable value are accepted.
func TestGetInactiveSlotsBackValidation(t *testing.T) {
	u := utxodb.NewUTXODB(ledger.InitWithTestingLedgerData())
	call := func(q string) (api.InactiveUTXOs, int) {
		env := &lrbCountingEnv{reader: u.SugaredStateReader()}
		srv := &server{environment: env}
		rec := httptest.NewRecorder()
		srv.getInactive(rec, httptest.NewRequest(http.MethodGet, api.PathGetInactive+"?"+q, nil))
		var resp api.InactiveUTXOs
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp, env.calls
	}
	for _, q := range []string{
		"slots_back=-1", "slots_back=-0", "slots_back=-4294967296",
		"slots_back=4294967296", "slots_back=18446744073709551616",
		"slots_back=word", "slots_back=", "slots_back=1&slots_back=2",
	} {
		t.Run("invalid_"+q, func(t *testing.T) {
			resp, calls := call(q)
			require.NotEmpty(t, resp.Error.Error)
			require.Zero(t, calls, "an invalid query must be refused before reading state")
		})
	}
	for _, q := range []string{"", "slots_back=0", "slots_back=1", "slots_back=360", "slots_back=8738", "slots_back=4294967295"} {
		t.Run("valid_"+q, func(t *testing.T) {
			resp, calls := call(q)
			require.Empty(t, resp.Error.Error)
			require.Equal(t, 1, calls)
		})
	}
}
