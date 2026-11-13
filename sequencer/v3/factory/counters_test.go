package factory

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCounters checks the per-slot diagnostic counters of SLOT STATS: concurrent increments from the
// factory's workers are all counted, the listing is sorted by key, and a reset empties it.
func TestCounters(t *testing.T) {
	var c Counters
	require.Equal(t, "none", c.String())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc("posted")
			c.Inc("pairErr")
		}()
	}
	wg.Wait()
	c.Inc("belowBest")
	require.Equal(t, "belowBest=1 pairErr=50 posted=50", c.String())

	c.Reset()
	require.Equal(t, "none", c.String())
}
