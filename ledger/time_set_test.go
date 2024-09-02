package ledger

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// this test is in 'ledger' package because ledger.ID singleton is not initialized here

func TestTimeConstSet(t *testing.T) {
	const d = 10 * time.Millisecond
	id, _ := GetTestingIdentityData()
	id.SetTickDuration(d)
	Init(id)
	t.Logf("\n%s", L().ID.TimeConstantsToString())
	require.EqualValues(t, d, TickDuration())
	t.Logf("------------------\n%s", id.String())
	t.Logf("------------------\n" + string(id.YAML()))
	t.Logf("------------------\n" + L().ID.TimeConstantsToString())
}
