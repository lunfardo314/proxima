package attacher

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// catchUpTestEnvironment adds to countTestEnvironment what the attacher's run loop calls.
type catchUpTestEnvironment struct {
	countTestEnvironment
}

func (catchUpTestEnvironment) DeadlockCatchingDisabled() bool { return true }

// A reattach cascade is a chain: each reattached milestone waits for an older one. Started newest
// first, as a cascade discovers them, the waiting attachers queue for a pass ahead of the ones they
// wait on. The cap must bound the passes running at once and still let every attacher finish: a
// waiting attacher holds no pass, so it never blocks the dependency it waits for.
func TestCatchUpPassesBoundedAndComplete(t *testing.T) {
	const n = 50
	env := catchUpTestEnvironment{}
	now := ledger.TimeNow().Slot

	attachers := make([]*milestoneAttacher, n)
	finished := make([]atomic.Bool, n)
	for i := range attachers {
		attachers[i] = &milestoneAttacher{
			attacher:  attacher{Environment: env},
			vid:       vertex.WrapTxID(base.RandomTransactionID(true, 1, base.LedgerTime{Slot: now, Tick: 10})),
			pokeChan:  make(chan struct{}, 1),
			ctx:       context.Background(),
			startSlot: now,
			catchUp:   true,
		}
	}

	var running, maxRunning atomic.Int32
	var wg sync.WaitGroup
	for i := n - 1; i >= 0; i-- {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status := attachers[i].lazyRepeat("test", func() vertex.Status {
				r := running.Add(1)
				for m := maxRunning.Load(); r > m && !maxRunning.CompareAndSwap(m, r); m = maxRunning.Load() {
				}
				time.Sleep(time.Millisecond) // a pass does some work
				running.Add(-1)
				if i > 0 && !finished[i-1].Load() {
					return vertex.Undefined // the older dependency is not attached yet
				}
				return vertex.Good
			})
			require.Equal(t, vertex.Good, status)
			finished[i].Store(true)
			if i+1 < n {
				attachers[i+1]._doPoke() // the dependent attacher learns its dependency is done
			}
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("catch-up attachers did not all finish: deadlocked on passes")
	}
	require.LessOrEqual(t, int(maxRunning.Load()), cap(catchUpPasses))
	for i := range finished {
		require.True(t, finished[i].Load(), "attacher %d did not finish", i)
	}
}
