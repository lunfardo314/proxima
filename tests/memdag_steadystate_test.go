package tests

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMemDAGSteadyStateGrowth reproduces the steady-state past-cone / memDAG
// growth observed on the testnet (2026-06-15): a SYNCED node (LRB at the tip)
// whose memDAG and proxima_past_cone_size climb in lockstep indefinitely
// (~N branches/slot accumulating, GC deleted≈0, oldestSlot frozen at the start
// slot), distinct from the lagging-node recursion fixed in TestMemDAGLaggingNodeRecursion.
//
// Key contrast established earlier: a SINGLE sequencer from genesis stays bounded
// (TestMemDAGLaggingNodeRecursion's node A held memDAG=56 over 80 slots). The leak
// is multi-sequencer specific — competing/sibling branches (one per sequencer per
// slot) that are never trimmed from past cones. So this test runs several
// sequencers and watches whether memDAG plateaus (correct) or grows with time (leak).
//
// runtime.GC() is forced periodically so NumVertices reflects truly-pinned
// vertices (weak-pointer map entries get reclaimed), the same way Test5SequencersIdlePruner does.
func TestMemDAGSteadyStateGrowth(t *testing.T) {
	const (
		nSequencers = 4
		maxSlots    = 100000
		warmup      = 30 * time.Second  // reach steady state (chain origins + ramp-up)
		observe     = 120 * time.Second // watch for growth
		sampleEvery = 15 * time.Second
	)

	testData := initMultiSequencerTest(t, nSequencers, true)
	testData.env.RepeatInBackground("test GC loop", 2*time.Second, func() bool {
		runtime.GC()
		return true
	})

	testData.startSequencersWithTimeout(maxSlots)

	time.Sleep(warmup)
	baseVertices := testData.wrk.NumVertices()
	_, baseSlot, _ := testData.wrk.LatestBranchSlots()
	t.Logf("after warmup %v: memDAG=%d (branch slot %d)", warmup, baseVertices, baseSlot)

	var samples []int
	for elapsed := sampleEvery; elapsed <= observe; elapsed += sampleEvery {
		time.Sleep(sampleEvery)
		n := testData.wrk.NumVertices()
		_, slot, _ := testData.wrk.LatestBranchSlots()
		samples = append(samples, n)
		t.Logf("  +%-4v memDAG=%d (branch slot %d)", elapsed, n, slot)
	}

	testData.stopAndWait(5 * time.Second)

	last := samples[len(samples)-1]
	// In a healthy steady state memDAG plateaus: vertices below the LRB are reclaimed
	// as the baseline advances, so the count tracks a bounded window, NOT wall-clock.
	// The leak shows the count climbing roughly linearly with elapsed slots.
	require.Lessf(t, last, baseVertices*2,
		"memDAG grew from %d (post-warmup) to %d over %v of steady-state operation — "+
			"unbounded growth = past-cone accumulation leak (vertices never reclaimed below the start slot)",
		baseVertices, last, observe)
}
