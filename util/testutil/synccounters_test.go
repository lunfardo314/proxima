package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncCounters(t *testing.T) {
	sc := NewSynCounters()

	sc.Add("n25", 5)
	sc.Inc("n1")
	sc.Add("n25", 20)

	for i := 0; i < 100; i++ {
		sc.Inc("n100")
	}

	require.EqualValues(t, 25, sc.Value("n25"))
	require.EqualValues(t, 1, sc.Value("n1"))
	require.EqualValues(t, 100, sc.Value("n100"))
	t.Logf("\n%s", sc.String())
}
