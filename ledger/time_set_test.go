package ledger

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// this test is in 'ledger' package because ledger.id singleton is not initialized here

func TestTimeConstSet(t *testing.T) {
	const d = 10 * time.Millisecond
	idParams, _ := GetTestingLedgerParams()
	idParams.TickDuration = d
	libraryID := LibraryJSONFromParameters(idParams, true)
	MustInitLibraryCacheFromJSON(libraryID)
	t.Logf("\n%s", L(0).TimeConstantsToString())
	require.EqualValues(t, d, TickDuration())
	t.Logf("------------------\n%s", L(0).String())
	t.Logf("------------------\n%s", L(0).TimeConstantsToString())
}
