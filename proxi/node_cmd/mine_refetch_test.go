package node_cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The adaptive window is 2x the mean solve time (2^K/hashrate), clamped. These
// cases pin the clamps and, in particular, that a difficulty far above the local
// hashrate cannot overflow time.Duration into a negative/absurd window.
func TestAdaptiveRefetchWindow(t *testing.T) {
	const h = 150_000 // attempts/sec, roughly a laptop

	// hashrate not yet known -> the short first round that measures it
	require.EqualValues(t, initialRefetchWindow, adaptiveRefetchWindow(24, 0))
	require.EqualValues(t, initialRefetchWindow, adaptiveRefetchWindow(24, -1))

	// K=24 at 150k H/s: mean 2^24/150k ~= 112s, window 2x -> clamped to the max
	require.EqualValues(t, maxRefetchWindow, adaptiveRefetchWindow(24, h))

	// K=21: mean ~14s, window ~28s — inside the band, so neither clamp applies
	w := adaptiveRefetchWindow(21, h)
	require.Greater(t, w, minRefetchWindow)
	require.Less(t, w, maxRefetchWindow)
	require.InDelta(t, 28*time.Second, w, float64(3*time.Second))

	// tiny K: mean is sub-second -> floor
	require.EqualValues(t, minRefetchWindow, adaptiveRefetchWindow(4, h))

	// The overflow guard: 2^63 attempts at a slow hashrate is ~1e14 seconds, which
	// as a raw time.Duration would overflow int64 and go negative. Must clamp.
	for _, k := range []int{40, 50, 63} {
		w := adaptiveRefetchWindow(k, 1)
		require.EqualValues(t, maxRefetchWindow, w, "K=%d must clamp, got %v", k, w)
		require.Positive(t, w)
	}
}

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
