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

// TestSeqDoNotProduceBranches verifies no-branch mode, which is now the DEFAULT: of two extra
// sequencers, seq0 runs with the default (no ProduceBranches) and seq1 opts in with
// WithProduceBranches. seq0 must never issue a branch transaction, yet keep producing
// (non-branch) milestones; meanwhile the rest of the network (bootstrap + seq1) keeps branching,
// so the ledger still advances and seq0's milestones ride into committed state via those
// branches. Runtime ≈ 20s.
func TestSeqDoNotProduceBranches(t *testing.T) {
	const (
		maxSlots    = 1000
		nSequencers = 2 // in addition to bootstrap
		runTime     = 15 * time.Second
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	var noBranchBranches, noBranchMilestones atomic.Int32
	var otherBranches atomic.Int32

	sequencers := make([]testSequencer, nSequencers)
	var err error
	for seqNr := range sequencers {
		opts := []sequencer.ConfigOption{
			sequencer.WithName(fmt.Sprintf("seq%d", seqNr)),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots),
		}
		// seq0 runs in no-branch mode (the default); seq1 opts in to branching
		if seqNr == 1 {
			opts = append(opts, sequencer.WithProduceBranches)
		}
		sequencers[seqNr], err = newTestSequencer(testData.wrk, testData.chainOrigins[seqNr].ChainID, testData.privKeyAux, opts...)
		require.NoError(t, err)

		nr := seqNr
		sequencers[seqNr].OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
			switch {
			case nr == 0 && ms.IsBranchTransaction():
				noBranchBranches.Add(1)
			case nr == 0:
				noBranchMilestones.Add(1)
			case ms.IsBranchTransaction():
				otherBranches.Add(1)
			}
		})
		sequencers[seqNr].Start()
	}

	time.Sleep(runTime)
	for _, s := range sequencers {
		s.Stop()
	}
	testData.bootstrapSeq.Stop()
	testData.stopAndWait(5 * time.Second)

	t.Logf("no-branch seq: %d milestones, %d branches; other seq: %d branches",
		noBranchMilestones.Load(), noBranchBranches.Load(), otherBranches.Load())

	// the no-branch sequencer must never issue a branch, but must still produce milestones
	require.EqualValues(t, 0, int(noBranchBranches.Load()), "no-branch sequencer must not produce any branch")
	require.Greater(t, int(noBranchMilestones.Load()), 0, "no-branch sequencer must still produce milestones")
	// the network keeps advancing: the other extra sequencer branches normally
	require.Greater(t, int(otherBranches.Load()), 0, "the other sequencer must branch normally")
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

// TestCoverageContributionUpperBound verifies that a sequencer with coverage (tokenBalance + frozenCoverage)
// above the upper bound cannot produce branch transactions, while those below it can, however small:
// there is no lower bound. Based on Test5SequencersIdlePruner.
//
// Setup: 5 non-bootstrap sequencers with different token balances (amounts
// scaled to the fair-launch genesis supply, InitialSupply = 100T):
//   - seq0: 0.5T (small)                 → should produce branches
//   - seq1: 5T                           → should produce branches
//   - seq2: 5T                           → should produce branches
//   - seq3: 5T                           → should produce branches
//   - seq4: 11T (above upper bound 10T)  → should NOT produce branches
//
// Bootstrap sequencer (~71.5T) produces branches normally.
// Note: boot must have enough coverage to pass the IsHealthyCoverageDelta health check
// (>7/12 of the actual supply) since it runs alone initially before other sequencers start.
func TestCoverageContributionUpperBound(t *testing.T) {
	const runTime = 30 * time.Second

	upperBound := uint64(10_000_000_000_000) // 10T

	// Chain amounts: [small, ok, ok, ok, above]
	// Total chains: 0.5T + 5T + 5T + 5T + 11T = 26.5T
	// Bootstrap gets: ~100T - 26.5T - 1T(primary) - 1T(faucet) = ~71.5T (healthy and within bounds)
	chainAmounts := []uint64{
		500_000_000_000,    // 0.5T - small
		5_000_000_000_000,  // 5T
		5_000_000_000_000,  // 5T
		5_000_000_000_000,  // 5T
		11_000_000_000_000, // 11T - above upper bound (10T)
	}
	nSequencers := len(chainAmounts)

	var totalChainAmount uint64
	for _, a := range chainAmounts {
		totalChainAmount += a
	}
	auxBalance := totalChainAmount + tagAlongFee

	cleanup := reinitTestLedgerWithCoverageUpperBound(upperBound)
	defer cleanup()

	lib := ledger.L(base.MaxSlot)
	t.Logf("coverage upper bound: %s, library at slot 1: %s",
		util.Th(upperBound), util.Th(lib.CoverageContributionUpperBound(1)))

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
		// all sequencers opt in to branching; the coverage-contribution upper bound (tested here)
		// is what suppresses branching for seq4, not the no-branch default.
		testData.sequencers[i], err = newTestSequencer(testData.wrk, testData.chainOrigins[i].ChainID, testData.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", i)),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(1000),
			sequencer.WithProduceBranches,
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

	// Bootstrap must have produced branches (exempt, and required for the system to function)
	require.Greater(t, bootBranchCount.Load(), int32(0), "bootstrap should produce branches")

	// seq4 (11T, above upper bound 10T) should produce NO branches
	require.EqualValues(t, 0, branchCounts[4].Load(), "seq4 (above upper bound) should not produce branches")

	// seq0..seq3 are below the upper bound and should produce branches, the small seq0 included
	for i := 0; i < 4; i++ {
		require.Greater(t, branchCounts[i].Load(), int32(0), "seq%d (below upper bound) should produce branches", i)
	}
}

func initMultiSequencerTest(t *testing.T, nSequencers int, startPruner ...bool) *workflowTestData {
	// Reinitialize ledger with fresh genesis timestamp to avoid timing issues
	// when tests run sequentially and the original genesis time becomes stale
	reinitTestLedger()

	// Real sequencer tests must run with the per-milestone coverageDelta
	// enforcement ON (the sequencers produce honest coverage). Guards against a
	// stray reinit having left it off.
	require.True(t, ledger.L(base.MaxSlot).EnforceCoverageDeltaMonotonicity,
		"coverageDelta monotonicity must be enabled for sequencer tests")

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
