package tests

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// Test1SequencerPrunerIdle: base scenario, 1 sequencer, idle. Runtime ≈ 12s.
func Test1SequencerPrunerIdle(t *testing.T) {
	const (
		maxSlots = 10
	)
	testData := initWorkflowTest(t, 1, true)
	t.Logf("%s", testData.wrk.Info())

	//testData.env.StartTracingTags(task.TraceTagBaseProposer)

	testData.env.RepeatInBackground("test GC loop", time.Second, func() bool {
		runtime.GC()
		return true
	})

	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)
	var countBr atomic.Int32
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		if ms.IsBranchTransaction() {
			countBr.Add(1)
		}
	})
	seq.OnExitOnce(func() {
		testData.stop()
	})
	seq.Start()

	testData.waitStop()

	require.EqualValues(t, maxSlots, int(countBr.Load()))
	//t.Logf("%s", testData.wrk.Info(true))
	//t.Logf("------------------------------\n%s", testData.wrk.InfoRefLines("     ").String())
}

// Test1SequencerPrunerTransfers: base scenario, 1 sequencer, with transfers. Runtime ≈ 32s.
func Test1SequencerPrunerTransfers(t *testing.T) {
	const (
		maxSlots   = 30
		batchSize  = 10
		maxBatches = 5
		sendAmount = 100_000_000
	)
	testData := initWorkflowTest(t, 1, true)
	//t.Logf("%s", testData.wrk.Info())

	//testData.wrk.StartTracingTags(task.TraceTagBaseProposer)
	//testData.wrk.StartTracingTags(task.TraceTagInsertTagAlongInputs)

	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)
	var countBr, countSeq atomic.Int32
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		if ms.IsBranchTransaction() {
			countBr.Add(1)
		} else {
			countSeq.Add(1)
		}
	})
	seq.OnExitOnce(func() {
		testData.stop()
	})
	seq.Start()

	rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
	require.EqualValues(t, initBalance+tagAlongFee, int(rdr.BalanceOf(testData.addrAux.ControllerID())))

	auxOuts, err := rdr.GetOutputsForAccount(testData.addrAux.ControllerID())
	require.EqualValues(t, 1, len(auxOuts))
	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	ctx, cancel := context.WithTimeout(context.Background(), (maxSlots+1)*ledger.SlotDuration())
	//ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	par := &spammerParams{
		t:             t,
		privateKey:    testData.privKeyFaucet,
		remainder:     testData.faucetOutput,
		tagAlongSeqID: []base.ChainID{testData.bootstrapChainID},
		target:        targetAddr,
		pace:          30,
		batchSize:     batchSize,
		maxBatches:    maxBatches,
		sendAmount:    sendAmount,
		tagAlongFee:   tagAlongFee,
		spammedTxIDs:  make([]base.TransactionID, 0),
	}
	go testData.spamTransfers(par, ctx)

	<-ctx.Done()
	cancel()

	require.EqualValues(t, batchSize*maxBatches, len(par.spammedTxIDs))

	testData.waitStop()
	t.Logf("%s", testData.wrk.Info(false))

	require.EqualValues(t, maxSlots, int(countBr.Load()))

	rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
	for _, txid := range par.spammedTxIDs {
		//require.True(t, rdr.KnowsCommittedTransaction(&txid))
		t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(txid))
	}
	targetBalance := rdr.BalanceOf(targetAddr.ControllerID())
	require.EqualValues(t, maxBatches*batchSize*sendAmount, int(targetBalance))

	balanceLeft := rdr.BalanceOf(testData.addrFaucet.ControllerID())
	require.EqualValues(t, initBalance-len(par.spammedTxIDs)*sendAmount-par.numSpammedBatches*tagAlongFee, int(balanceLeft))
}

func TestFinalizeChainOrigins(t *testing.T) {
	t.Skip("sensitive to timing")

	const (
		nSequencers = 5 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	testData.stopAndWait()

	t.Logf("%s", testData.wrk.Info(true))
}

// TestIdle2: base scenario, 2 sequencers (bootstrap + 1), idle. Runtime ≈ 24s.
// Flaky under full-suite CPU pressure: the 30s WaitUntilTransactionInHeaviestState
// timeout in initMultiSequencerTest can slip when the CPU is busy with prior tests.
func TestIdle2(t *testing.T) {
	t.Skip("flaky when run with full suite")
	const (
		maxSlots    = 30
		nSequencers = 1 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	testData.startSequencersWithTimeout(maxSlots)
	time.Sleep(20 * time.Second)
	testData.stopAndWait()

	t.Logf("%s", testData.wrk.Info(false))
}

func Test5SequencersIdlePruner(t *testing.T) {
	const (
		maxSlots    = 1000
		nSequencers = 4                // in addition to bootstrap
		runTime     = 30 * time.Second // 60 * time.Second
	)
	// TODO make individual private keys for each sequencer

	testData := initMultiSequencerTest(t, nSequencers, true)
	//testData.env.StartTracingTags(task.TraceTagBaseProposerExit) //, sequencer.TraceTagTarget)

	testData.env.RepeatInBackground("test GC loop", 2*time.Second, func() bool {
		runtime.GC()
		return true
	})

	testData.wrk.OnTxDeleted(func(txid base.TransactionID) bool {
		t.Logf("REMOVED %s", txid.StringShort())
		return true
	})

	testData.startSequencersWithTimeout(maxSlots)
	t.Logf("after start sequencers")
	time.Sleep(runTime)
	t.Logf("before stop and wait")
	success := testData.stopAndWait(5 * time.Second)
	require.True(t, success)

	//t.Logf("--------\n%s", testData.wrk.Info(true))
	//runtime.GC()
	//time.Sleep(time.Second)
	//t.Logf("--------\n%s", testData.wrk.Info(true))

}

func Test3Seq1TagAlong(t *testing.T) {
	const (
		maxSlots        = 100
		nSequencers     = 2 // in addition to bootstrap
		batchSize       = 10
		sendAmount      = 100_000_000
		spammingTimeout = 20 * time.Second
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
	require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.ControllerID())))

	//initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	ctx, cancelSpam := context.WithTimeout(context.Background(), spammingTimeout)
	par := &spammerParams{
		t:             t,
		privateKey:    testData.privKeyFaucet,
		remainder:     testData.faucetOutput,
		tagAlongSeqID: []base.ChainID{testData.bootstrapChainID},
		target:        targetAddr,
		pace:          30,
		batchSize:     batchSize,
		//maxBatches:    maxBatches,
		sendAmount:   sendAmount,
		tagAlongFee:  tagAlongFee,
		spammedTxIDs: make([]base.TransactionID, 0),
	}
	go testData.spamTransfers(par, ctx)
	go func() {
		<-ctx.Done()
		cancelSpam()
		t.Log("spamming stopped")
	}()

	testData.startSequencersWithTimeout(maxSlots)

	<-ctx.Done()
	time.Sleep(5 * time.Second)
	testData.stopAndWait(3 * time.Second)

	t.Logf("%s", testData.wrk.Info())

	rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
	for _, txid := range par.spammedTxIDs {
		//require.True(t, rdr.KnowsCommittedTransaction(&txid))
		t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(txid))
	}
	//require.EqualValues(t, (maxBatches+1)*batchSize, len(par.spammedTxIDs))

	targetBalance := rdr.BalanceOf(targetAddr.ControllerID())
	require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

	balanceLeft := rdr.BalanceOf(testData.addrFaucet.ControllerID())
	require.EqualValues(t, initBalance-len(par.spammedTxIDs)*sendAmount-par.numSpammedBatches*tagAlongFee, int(balanceLeft))

	//balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
	//require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
}

func Test3SeqMultiTagAlong(t *testing.T) {
	const (
		maxSlots        = 100 // 100
		nSequencers     = 2   // in addition to bootstrap
		batchSize       = 10  // 10
		sendAmount      = 100_000_000
		spammingTimeout = 30 * time.Second // 10
		startPruner     = true
		traceTx         = false
	)
	testData := initMultiSequencerTest(t, nSequencers, startPruner)

	//testData.env.StartTracingTags(attacher.TraceTagCoverageAdjustment)

	rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
	require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.ControllerID())))

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	tagAlongSeqIDs := []base.ChainID{testData.bootstrapChainID}
	for _, o := range testData.chainOrigins {
		tagAlongSeqIDs = append(tagAlongSeqIDs, o.ChainID)
	}
	tagAlongInitBalances := make(map[base.ChainID]uint64)
	for _, seqID := range tagAlongSeqIDs {
		tagAlongInitBalances[seqID] = rdr.BalanceOnChain(seqID)
	}

	ctx, cancelSpam := context.WithTimeout(context.Background(), spammingTimeout)
	par := &spammerParams{
		t:             t,
		privateKey:    testData.privKeyFaucet,
		remainder:     testData.faucetOutput,
		tagAlongSeqID: tagAlongSeqIDs,
		target:        targetAddr,
		pace:          30,
		batchSize:     batchSize,
		sendAmount:    sendAmount,
		tagAlongFee:   tagAlongFee,
		spammedTxIDs:  make([]base.TransactionID, 0),
		traceTx:       traceTx,
	}
	go testData.spamTransfers(par, ctx)
	go func() {
		<-ctx.Done()
		cancelSpam()
		t.Log("spamming stopped")
	}()

	testData.startSequencersWithTimeout(maxSlots)

	<-ctx.Done()
	time.Sleep(5 * time.Second)
	testData.stopAndWait(3 * time.Second)

	t.Logf("%s", testData.wrk.Info())
	rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
	for _, txid := range par.spammedTxIDs {
		require.True(t, rdr.KnowsCommittedTransaction(txid))
		//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
	}

	targetBalance := rdr.BalanceOf(targetAddr.ControllerID())
	require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

	balanceLeft := rdr.BalanceOf(testData.addrFaucet.ControllerID())
	require.EqualValues(t, initBalance-len(par.spammedTxIDs)*sendAmount-par.numSpammedBatches*tagAlongFee, int(balanceLeft))

	for seqID, initBal := range tagAlongInitBalances {
		balanceOnChain := rdr.BalanceOnChain(seqID)
		t.Logf("%s tx: %d, init: %s, final: %s", seqID.StringShort(), par.perChainID[seqID], util.Th(initBal), util.Th(balanceOnChain))
		// inflation etc...
		//require.EqualValues(t, int(initBal)+par.perChainID[seqID]*tagAlongFee, int(balanceOnChain))
	}
}

// TestBranchCoverageBounds verifies that sequencers with coverage (tokenBalance + frozenCoverage)
// outside [lowerBound, upperBound] cannot produce branch transactions, while those within bounds can.
// Based on Test5SequencersIdlePruner.
//
// Setup: 5 non-bootstrap sequencers with different token balances:
//   - seq0: 5T  (below lower bound 10T)  → should NOT produce branches
//   - seq1: 50T (within bounds)           → should produce branches
//   - seq2: 50T (within bounds)           → should produce branches
//   - seq3: 50T (within bounds)           → should produce branches
//   - seq4: 110T (above upper bound 100T) → should NOT produce branches
//
// Bootstrap sequencer (~715T) produces branches normally.
// Note: boot must have enough coverage to pass the IsHealthyCoverageDelta health check
// (>7/12 of supply) since it runs alone initially before other sequencers start.
func TestBranchCoverageBounds(t *testing.T) {
	const runTime = 30 * time.Second

	// Coverage bounds for the test
	lowerBound := uint64(10_000_000_000_000)  // 10T
	upperBound := uint64(100_000_000_000_000) // 100T

	// Chain amounts: [below, ok, ok, ok, above]
	// Total chains: 5T + 50T + 50T + 50T + 110T = 265T
	// Bootstrap gets: ~1000T - 265T - 10T(primary) - 10T(faucet) = ~715T (healthy and within bounds)
	chainAmounts := []uint64{
		5_000_000_000_000,   // 5T - below lower bound (10T)
		50_000_000_000_000,  // 50T - within bounds
		50_000_000_000_000,  // 50T - within bounds
		50_000_000_000_000,  // 50T - within bounds
		110_000_000_000_000, // 110T - above upper bound (100T)
	}
	nSequencers := len(chainAmounts)

	var totalChainAmount uint64
	for _, a := range chainAmounts {
		totalChainAmount += a
	}
	auxBalance := totalChainAmount + tagAlongFee

	// Reinit ledger with custom coverage bounds
	cleanup := reinitTestLedgerWithCoverageBounds(lowerBound, upperBound)
	defer cleanup()

	lib := ledger.L(base.MaxSlot)
	t.Logf("coverage bounds: lower=%s, upper=%s", util.Th(lowerBound), util.Th(upperBound))
	t.Logf("library lower=%s, upper=%s (at slot 1)",
		util.Th(lib.BranchCoverageLowerBound(1)), util.Th(lib.BranchCoverageUpperBound(1)))

	// Initialize workflow test with custom aux balance
	testData := initWorkflowTestWithAuxBalance(t, auxBalance, true)

	testData.env.RepeatInBackground("test GC loop", 2*time.Second, func() bool {
		runtime.GC()
		return true
	})

	err := testData.wrk.EnsureLatestBranches()
	require.NoError(t, err)

	// Create chain origins with specified amounts
	testData.makeChainOriginsWithAmounts(chainAmounts)
	chainOriginsTxID, err := testData.txBytesInForTestsChainOrigins()
	require.NoError(t, err)
	require.EqualValues(t, nSequencers, len(testData.chainOrigins))

	// Start bootstrap sequencer
	testData.bootstrapSeq, err = newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithPace(5),
		sequencer.WithDelayStart(3*time.Second),
	)
	require.NoError(t, err)

	var bootBranchCount atomic.Int32
	testData.bootstrapSeq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		if ms.IsBranchTransaction() {
			bootBranchCount.Add(1)
		}
	})
	testData.bootstrapSeq.Start()

	// Wait for chain origins to be finalized
	baseline, err := testData.wrk.WaitUntilTransactionInHeaviestState(chainOriginsTxID, 30*time.Second)
	require.NoError(t, err)
	t.Logf("chain origins tx %s finalized in baseline %s", chainOriginsTxID.StringShort(), baseline.IDShortString())

	// Start sequencers and track branch counts per sequencer
	branchCounts := make([]atomic.Int32, nSequencers)
	testData.sequencers = make([]testSequencer, nSequencers)
	for i := range testData.sequencers {
		testData.sequencers[i], err = newTestSequencer(testData.wrk, testData.chainOrigins[i].ChainID, testData.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", i)),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(1000),
		)
		require.NoError(t, err)
		idx := i
		testData.sequencers[i].OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				branchCounts[idx].Add(1)
			}
		})
		testData.sequencers[i].Start()
	}

	// Let the sequencers run
	time.Sleep(runTime)

	// Stop all sequencers
	for _, seq := range testData.sequencers {
		seq.Stop()
	}
	testData.bootstrapSeq.Stop()
	success := testData.stopAndWait(5 * time.Second)
	require.True(t, success)

	// Log results
	t.Logf("Bootstrap branches: %d", bootBranchCount.Load())
	for i, amount := range chainAmounts {
		t.Logf("  seq%d (amount: %s): %d branches", i, util.Th(amount), branchCounts[i].Load())
	}

	// Bootstrap must have produced branches (it's within bounds and required for the system to function)
	require.Greater(t, bootBranchCount.Load(), int32(0), "bootstrap should produce branches")

	// seq0 (5T, below lower bound 10T) should produce NO branches
	require.EqualValues(t, 0, branchCounts[0].Load(), "seq0 (below lower bound) should not produce branches")

	// seq4 (810T, above upper bound 800T) should produce NO branches
	require.EqualValues(t, 0, branchCounts[4].Load(), "seq4 (above upper bound) should not produce branches")

	// seq1, seq2, seq3 (50T, within bounds) should produce branches
	require.Greater(t, branchCounts[1].Load(), int32(0), "seq1 (within bounds) should produce branches")
	require.Greater(t, branchCounts[2].Load(), int32(0), "seq2 (within bounds) should produce branches")
	require.Greater(t, branchCounts[3].Load(), int32(0), "seq3 (within bounds) should produce branches")
}

func initMultiSequencerTest(t *testing.T, nSequencers int, startPruner ...bool) *workflowTestData {
	// Reinitialize ledger with fresh genesis timestamp to avoid timing issues
	// when tests run sequentially and the original genesis time becomes stale
	reinitTestLedger()

	testData := initWorkflowTest(t, nSequencers, startPruner...)
	//testData.wrk.StartTracingTags(tippool.TraceTag)
	//testData.wrk.StartTracingTags(factory.TraceTag)
	//testData.wrk.StartTracingTags(attacher.TraceTagEnsureLatestBranches)

	err := testData.wrk.EnsureLatestBranches()
	require.NoError(t, err)

	testData.makeChainOrigins(nSequencers)
	chainOriginsTxID, err := testData.txBytesInForTestsChainOrigins()
	require.NoError(t, err)
	require.EqualValues(t, nSequencers, len(testData.chainOrigins))

	testData.bootstrapSeq, err = newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithPace(5),
		sequencer.WithDelayStart(3*time.Second),
	)
	require.NoError(t, err)

	testData.bootstrapSeq.Start()

	baseline, err := testData.wrk.WaitUntilTransactionInHeaviestState(chainOriginsTxID, 30*time.Second)
	require.NoError(t, err)
	t.Logf("chain origins transaction %s has been created and finalized in baseline %s", chainOriginsTxID.StringShort(), baseline.IDShortString())
	return testData
}
