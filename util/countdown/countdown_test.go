package countdown

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCountdown(t *testing.T) {
	const howManyThreads = 100
	const howManyInc = 10_000

	timeout := 5 * time.Second
	t.Logf("testing countdown. 2 x %d x %d = %d, timeoutReached: %v",
		howManyThreads, howManyInc, 2*howManyThreads*howManyInc, timeout)
	w := New(howManyThreads*howManyInc*2, timeout)

	for i := 0; i < howManyThreads; i++ {
		go func() {
			for k := 0; k < 2; k++ {
				for j := 0; j < howManyInc; j++ {
					w.Tick()
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	err := w.Wait()
	require.NoError(t, err)
}

func TestCountdownMulti(t *testing.T) {
	const howManyCounters = 100

	nticks := make([]int, howManyCounters)
	for i := range nticks {
		nticks[i] = rand.Intn(100) + 1
	}
	counters := make([]*Countdown, howManyCounters)
	timeout := 3 * time.Second
	for i := range counters {
		counters[i] = NewNamed(fmt.Sprintf("%d", i), nticks[i], timeout)
	}
	for i := range counters {
		i1 := i
		go func() {
			for j := 0; j < nticks[i1]; j++ {
				counters[i1].Tick()
			}
		}()
	}
	err := Wait(counters...)
	require.NoError(t, err)
}

func BenchmarkCountdown(b *testing.B) {
	cd := New(b.N, 30*time.Second)
	go func() {
		for i := 0; i < b.N; i++ {
			cd.Tick()
		}
	}()
	err := cd.Wait()
	require.NoError(b, err)
}
