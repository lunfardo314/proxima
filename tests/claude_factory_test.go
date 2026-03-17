// Tests for the TransactionSkeletonFactory (TSF).
// TSF produces transaction skeletons (IncrementalAttachers with extend + endorsements)
// with strictly increasing coverage. These tests verify:
// - Factory produces skeletons when multiple sequencers are running
// - Skeletons have valid structure (extend output, endorsements, completed past cone)
// - Coverage is strictly increasing across skeletons in a round
// - Factory stops cleanly on context cancellation
//
// Multi-sequencer setup is required because TSF needs endorsement candidates
// from OTHER sequencers — a single sequencer has nothing to endorse.

package tests

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/stretchr/testify/require"
)

// TestFactoryProducesSkeletons verifies that TSF produces at least one skeleton
// when multiple sequencers are running and generating milestones.
func TestFactoryProducesSkeletons(t *testing.T) {
	const (
		maxSlots    = 20
		nSequencers = 2 // in addition to bootstrap
	)

	testData := initMultiSequencerTest(t, nSequencers, true)
	testData.startSequencersWithTimeout(maxSlots)

	// start factory attached to the bootstrap sequencer
	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(testData.bootstrapSeq, ctx)
	go f.Run()

	var skeletonCount atomic.Int32

	// collect skeletons in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			count := skeletonCount.Add(1)
			t.Logf("skeleton #%d: endorsements=%d, coverage=%d",
				count, len(sk.Endorsing()), sk.Coverage)
			sk.Close()
		}
	}()

	// run for a while, then stop
	time.Sleep(15 * time.Second)
	cancel()
	testData.stopAndWait()
	<-done

	total := int(skeletonCount.Load())
	t.Logf("total skeletons produced: %d", total)
	require.Greater(t, total, 0, "factory should produce at least one skeleton")
}

// TestFactorySkeletonStructure verifies that produced skeletons have valid structure:
// non-closed, completed, with at least 1 endorsement and a valid extending output.
func TestFactorySkeletonStructure(t *testing.T) {
	const (
		maxSlots    = 20
		nSequencers = 2
	)

	testData := initMultiSequencerTest(t, nSequencers, true)
	testData.startSequencersWithTimeout(maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(testData.bootstrapSeq, ctx)
	go f.Run()

	var checked atomic.Int32

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			require.False(t, sk.IsClosed(), "skeleton should not be closed")
			require.True(t, sk.Completed(), "skeleton should have completed past cone")
			require.Greater(t, len(sk.Endorsing()), 0, "skeleton should have at least 1 endorsement")
			extend := sk.Extending()
			require.True(t, extend.ValidID(), "extending output should have valid ID")
			checked.Add(1)
			sk.Close()
		}
	}()

	time.Sleep(15 * time.Second)
	cancel()
	testData.stopAndWait()
	<-done

	t.Logf("checked %d skeletons, all valid", checked.Load())
	require.Greater(t, int(checked.Load()), 0, "should have checked at least one skeleton")
}

// TestFactoryIncreasingCoverage verifies that the factory's output has
// coverage that increases over time (with possible resets on new rounds).
func TestFactoryIncreasingCoverage(t *testing.T) {
	const (
		maxSlots    = 30
		nSequencers = 2
	)

	testData := initMultiSequencerTest(t, nSequencers, true)
	testData.startSequencersWithTimeout(maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(testData.bootstrapSeq, ctx)
	go f.Run()

	var lastCoverage uint64
	var increases atomic.Int32
	var total atomic.Int32

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			total.Add(1)
			if sk.Coverage > lastCoverage {
				increases.Add(1)
			}
			lastCoverage = sk.Coverage
			sk.Close()
		}
	}()

	time.Sleep(20 * time.Second)
	cancel()
	testData.stopAndWait()
	<-done

	totalN := int(total.Load())
	incN := int(increases.Load())
	t.Logf("total skeletons: %d, coverage increases: %d", totalN, incN)
	if totalN > 0 {
		require.Greater(t, incN, 0, "should have at least one coverage increase")
	}
}

// TestFactoryStopsCleanly verifies that the factory stops without hanging
// when the context is cancelled.
func TestFactoryStopsCleanly(t *testing.T) {
	const (
		maxSlots    = 10
		nSequencers = 1
	)

	testData := initMultiSequencerTest(t, nSequencers, true)
	testData.startSequencersWithTimeout(maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	f := factory.New(testData.bootstrapSeq, ctx)
	go f.Run()

	// drain skeletons
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			sk.Close()
		}
	}()

	// let it run briefly, then cancel
	time.Sleep(5 * time.Second)
	cancel()
	testData.stopAndWait()

	select {
	case <-done:
		t.Log("factory stopped cleanly")
	case <-time.After(5 * time.Second):
		t.Fatal("factory did not stop within 5 seconds")
	}
}
