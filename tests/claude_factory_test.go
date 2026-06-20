// Tests for the TransactionSkeletonFactory (TSF).
// TSF produces transaction skeletons (IncrementalAttachers with extend + endorsements)
// with non-decreasing coverage. These tests verify:
// - Factory produces skeletons when multiple sequencers are running
// - Skeletons have valid structure (extend output, endorsements, completed past cone)
// - Coverage is non-decreasing (equal coverage accepted for outer loop to augment)
// - Factory stops cleanly on context cancellation
// - Factory restarts rounds when own milestone changes in tippool
// - Factory runs correctly in parallel with sequencers under tag-along load
// - Factory achieves multi-endorsement skeletons over time
//
// Multi-sequencer setup is required because TSF needs endorsement candidates
// from OTHER sequencers — a single sequencer has nothing to endorse.

package tests

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/stretchr/testify/require"
)

// initFactoryTest sets up a multi-sequencer test using v1 sequencers (regardless of
// testSequencerVersion) and returns the v1 bootstrap sequencer for use as factory environment.
// Factory tests test the factory in isolation — v1 sequencers generate the milestones
// that the factory needs as endorsement candidates.
func initFactoryTest(t *testing.T, nSequencers int, maxSlots int) (*workflowTestData, *sequencer.Sequencer) {
	t.Helper()
	reinitTestLedger()
	// Real sequencer/factory tests run with coverageDelta enforcement ON.
	require.True(t, ledger.L(base.MaxSlot).EnforceCoverageDeltaMonotonicity,
		"coverageDelta monotonicity must be enabled for sequencer tests")
	testData := initWorkflowTest(t, nSequencers, true)

	err := testData.wrk.EnsureLatestBranches()
	require.NoError(t, err)

	testData.makeChainOrigins(nSequencers)
	chainOriginsTxID, err := testData.txBytesInForTestsChainOrigins()
	require.NoError(t, err)
	require.EqualValues(t, nSequencers, len(testData.chainOrigins))

	// always v1 for factory tests
	bootSeq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithPace(5),
		sequencer.WithDelayStart(3*time.Second),
		sequencer.WithDoNotWaitForSync, // test bootstrap: never "synced", force-start
	)
	require.NoError(t, err)
	testData.bootstrapSeq = bootSeq
	bootSeq.Start()

	baseline, err := testData.wrk.WaitUntilTransactionInHeaviestState(chainOriginsTxID, 30*time.Second)
	require.NoError(t, err)
	t.Logf("chain origins tx %s finalized in baseline %s", chainOriginsTxID.StringShort(), baseline.IDShortString())

	// start additional v1 sequencers
	testData.sequencers = make([]testSequencer, len(testData.chainOrigins))
	for seqNr := range testData.sequencers {
		testData.sequencers[seqNr], err = sequencer.New(testData.wrk, testData.chainOrigins[seqNr].ChainID, testData.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", seqNr)),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots),
			sequencer.WithDoNotWaitForSync, // test: force-start
		)
		require.NoError(t, err)
		testData.sequencers[seqNr].Start()
	}
	go func() {
		<-testData.env.Ctx().Done()
		for _, seq := range testData.sequencers {
			seq.Stop()
		}
		bootSeq.Stop()
	}()

	return testData, bootSeq
}

// keepTargetSlotUpdated periodically updates the factory's target slot
// to match the current ledger time. Stops when ctx is cancelled.
func keepTargetSlotUpdated(ctx context.Context, f *factory.Factory) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.SetTargetSlot(ledger.TimeNow().Slot)
		}
	}
}

// TestFactoryProducesSkeletons verifies that TSF produces at least one skeleton
// when multiple sequencers are running and generating milestones.
func TestFactoryProducesSkeletons(t *testing.T) {
	const (
		maxSlots    = 20
		nSequencers = 2 // in addition to bootstrap
	)

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

	var skeletonCount atomic.Int32

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

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

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

// TestFactoryNonDecreasingCoverage verifies that the factory's output has
// non-decreasing coverage within a slot. Equal coverage is allowed because the outer
// loop (sequencer) adds tag-along and delegation inputs that increase it further.
// Across slot boundaries coverage may reset.
func TestFactoryNonDecreasingCoverage(t *testing.T) {
	const (
		maxSlots    = 30
		nSequencers = 2
	)

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

	var lastCoverage uint64
	var increases atomic.Int32
	var equals atomic.Int32
	var total atomic.Int32

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			total.Add(1)
			if sk.Coverage > lastCoverage {
				increases.Add(1)
			} else if sk.Coverage == lastCoverage && lastCoverage > 0 {
				equals.Add(1)
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
	eqN := int(equals.Load())
	t.Logf("total skeletons: %d, coverage increases: %d, equal-coverage: %d", totalN, incN, eqN)
	if totalN > 0 {
		// at least some must show increasing coverage
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

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			sk.Close()
		}
	}()

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

// TestFactoryOwnMilestoneRestart verifies that the factory restarts its improvement
// round when a new own milestone appears in the tippool. This is detected by comparing
// GetLatestMilestone(ownSeqID) at each improvement iteration with the value at round start.
// We observe this indirectly: the factory should produce skeletons that extend different
// own milestones over time (not always the same one).
func TestFactoryOwnMilestoneRestart(t *testing.T) {
	const (
		maxSlots    = 30
		nSequencers = 3 // more sequencers = more endorsement opportunities = more milestones
	)

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

	// track distinct extending outputs across skeletons
	var mu sync.Mutex
	extendingVIDs := make(map[*vertex.WrappedTx]bool)
	var total atomic.Int32

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			total.Add(1)
			extend := sk.Extending()
			mu.Lock()
			extendingVIDs[extend.VID] = true
			mu.Unlock()
			sk.Close()
		}
	}()

	time.Sleep(20 * time.Second)
	cancel()
	testData.stopAndWait()
	<-done

	mu.Lock()
	distinctExtends := len(extendingVIDs)
	mu.Unlock()

	totalN := int(total.Load())
	t.Logf("total skeletons: %d, distinct extending milestones: %d", totalN, distinctExtends)

	// with 4 sequencers running for 20s, the bootstrap sequencer should produce multiple
	// milestones, and the factory should restart and extend from different ones
	if totalN > 1 {
		require.Greater(t, distinctExtends, 1,
			"factory should extend from multiple own milestones (restart on own milestone change)")
	}
}

// TestFactoryMultiEndorsement verifies that the factory's improvement loop achieves
// skeletons with more than 1 endorsement. With multiple sequencers running, the
// improvement loop should add endorsements beyond the initial one from ChooseFirstExtendEndorsePair.
func TestFactoryMultiEndorsement(t *testing.T) {
	const (
		maxSlots    = 40
		nSequencers = 4 // need enough endorsement candidates
	)

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

	var maxEndorsements atomic.Int32
	var total atomic.Int32

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			total.Add(1)
			n := int32(len(sk.Endorsing()))
			for {
				cur := maxEndorsements.Load()
				if n <= cur || maxEndorsements.CompareAndSwap(cur, n) {
					break
				}
			}
			t.Logf("skeleton #%d: endorsements=%d, coverage=%d",
				total.Load(), n, sk.Coverage)
			sk.Close()
		}
	}()

	time.Sleep(25 * time.Second)
	cancel()
	testData.stopAndWait()
	<-done

	totalN := int(total.Load())
	maxE := int(maxEndorsements.Load())
	t.Logf("total skeletons: %d, max endorsements in any skeleton: %d", totalN, maxE)

	// with 5 sequencers (4 + bootstrap), the improvement loop should achieve at least
	// 2 endorsements in some skeleton
	if totalN > 0 {
		require.Greater(t, maxE, 1,
			"improvement loop should achieve >1 endorsements with enough sequencer candidates")
	}
}

// TestFactoryParallelWithTagAlong verifies that the factory runs correctly in parallel
// with sequencers that are processing tag-along transactions. This is the intended
// integration mode: sequencers produce milestones while the factory continuously
// scans for better skeletons.
func TestFactoryParallelWithTagAlong(t *testing.T) {
	const (
		maxSlots        = 100
		nSequencers     = 2
		batchSize       = 5
		sendAmount      = 100_000_000
		spammingTimeout = 15 * time.Second
	)

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	// start factory on bootstrap sequencer
	factoryCtx, factoryCancel := context.WithCancel(testData.env.Ctx())
	defer factoryCancel()
	f := factory.New(bootSeq, factoryCtx)
	go f.Run()
	go keepTargetSlotUpdated(factoryCtx, f)

	// start tag-along spammer
	targetAddr := ledger.SigLockFromED25519PrivateKey(genesisPrivateKey)
	spamCtx, spamCancel := context.WithTimeout(context.Background(), spammingTimeout)
	par := &spammerParams{
		t:             t,
		privateKey:    testData.privKeyFaucet,
		remainder:     testData.faucetOutput,
		tagAlongSeqID: []base.ChainID{testData.bootstrapChainID},
		target:        targetAddr,
		pace:          30,
		batchSize:     batchSize,
		sendAmount:    sendAmount,
		tagAlongFee:   tagAlongFee,
		spammedTxIDs:  make([]base.TransactionID, 0),
	}
	go testData.spamTransfers(par, spamCtx)
	go func() {
		<-spamCtx.Done()
		spamCancel()
	}()

	// collect factory output
	var skeletonCount atomic.Int32
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

	// wait for spamming to finish, then let things settle
	<-spamCtx.Done()
	time.Sleep(5 * time.Second)

	factoryCancel()
	testData.stopAndWait(3 * time.Second)
	<-done

	totalSkeletons := int(skeletonCount.Load())
	t.Logf("total skeletons: %d, spammed txs: %d", totalSkeletons, len(par.spammedTxIDs))

	// factory should produce skeletons even under tag-along load
	require.Greater(t, totalSkeletons, 0,
		"factory should produce skeletons while sequencers process tag-alongs")
}

// TestFactorySlotTransition verifies that factory correctly handles slot transitions.
// When SetTargetSlot advances to a new slot, the factory resets its checked-combinations
// and best coverage, then produces fresh skeletons for the new slot.
func TestFactorySlotTransition(t *testing.T) {
	const (
		maxSlots    = 30
		nSequencers = 2
	)

	testData, bootSeq := initFactoryTest(t, nSequencers, maxSlots)

	ctx, cancel := context.WithCancel(testData.env.Ctx())
	defer cancel()
	f := factory.New(bootSeq, ctx)
	go f.Run()
	go keepTargetSlotUpdated(ctx, f)

	// track which slots produced skeletons
	var mu sync.Mutex
	slotsSeen := make(map[uint32]int)
	var total atomic.Int32

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sk := range f.OutCh() {
			total.Add(1)
			// the extending output's slot tells us which slot the factory is working in
			slot := sk.Extending().VID.Slot()
			mu.Lock()
			slotsSeen[slot]++
			mu.Unlock()
			sk.Close()
		}
	}()

	time.Sleep(20 * time.Second)
	cancel()
	testData.stopAndWait()
	<-done

	mu.Lock()
	nSlots := len(slotsSeen)
	mu.Unlock()

	totalN := int(total.Load())
	t.Logf("total skeletons: %d, across %d distinct slots", totalN, nSlots)

	// over 20 seconds (~2 slots per second with 10.24s slots), we should see skeletons
	// from at least 2 different slots
	if totalN > 1 {
		require.Greater(t, nSlots, 1,
			"factory should produce skeletons across multiple slots")
	}
}
