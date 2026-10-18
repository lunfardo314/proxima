package mine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The running hashrate estimate the stall timeout reads.
func TestUpdateHashrate(t *testing.T) {
	// first measurement seeds the estimate outright
	require.InDelta(t, 1000.0, updateHashrate(0, 1000, time.Second), 0.001)
	// degenerate rounds leave the estimate untouched
	require.InDelta(t, 500.0, updateHashrate(500, 0, time.Second), 0.001)
	require.InDelta(t, 500.0, updateHashrate(500, 1000, 0), 0.001)
	// subsequent measurements are smoothed, not replaced
	got := updateHashrate(1000, 2000, time.Second)
	require.InDelta(t, 1000*(1-hashrateEWMAWeight)+2000*hashrateEWMAWeight, got, 0.001)
	require.Greater(t, got, 1000.0)
	require.Less(t, got, 2000.0)
}
