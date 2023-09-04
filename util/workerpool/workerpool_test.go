package workerpool

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestBasic(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		require.Panics(t, func() {
			NewWorkerPool(0)
		})
	})
	t.Run("2", func(t *testing.T) {
		wp := NewWorkerPool(1)
		var wg sync.WaitGroup
		wg.Add(1)
		counter := 0
		wp.Work(func() {
			counter++
			wg.Done()
		})
		wg.Wait()
		require.EqualValues(t, 1, counter)
	})
	t.Run("3", func(t *testing.T) {
		const numWorkers = 1
		wp := NewWorkerPool(numWorkers)
		var wg sync.WaitGroup
		var nw atomic.Int32
		const howMany = 100
		wg.Add(howMany)
		counter := 0
		for i := 0; i < howMany; i++ {
			wp.Work(func() {
				require.True(t, nw.Load() < numWorkers)
				nw.Inc()
				counter++
				time.Sleep(10 * time.Millisecond)
				nw.Dec()
				wg.Done()
			})
		}
		wg.Wait()
		require.EqualValues(t, howMany, counter)
	})
	t.Run("4", func(t *testing.T) {
		const numWorkers = 100
		wp := NewWorkerPool(numWorkers)
		var wg sync.WaitGroup
		var nw atomic.Int32
		const howMany = 100
		wg.Add(howMany)
		counter := 0
		for i := 0; i < howMany; i++ {
			wp.Work(func() {
				require.True(t, nw.Load() < numWorkers)
				nw.Inc()
				counter++
				time.Sleep(10 * time.Millisecond)
				nw.Dec()
				wg.Done()
			})
		}
		wg.Wait()
		require.EqualValues(t, howMany, counter)
	})
}
