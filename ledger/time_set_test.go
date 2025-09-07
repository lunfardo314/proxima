package ledger

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// this test is in 'ledger' package because ledger.id singleton is not initialized here

func TestTimeConstSet(t *testing.T) {
	const d = 10 * time.Millisecond
	idParams, _ := GetTestingIdentityData()
	idParams.SetTickDuration(d)
	libraryID := LibraryYAMLFromLedgerParameters(idParams, true)
	MustInitSingleton(libraryID)
	t.Logf("\n%s", L().ID.TimeConstantsToString())
	require.EqualValues(t, d, TickDuration())
	t.Logf("------------------\n%s", idParams.String())
	t.Logf("------------------\n%s", L().ID.TimeConstantsToString())
}
