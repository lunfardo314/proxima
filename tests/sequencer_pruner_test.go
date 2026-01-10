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

	seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)
	var countBr atomic.Int32
	seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
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
	testData.saveFullDAG("full_dag")
}

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

	seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)
	var countBr, countSeq atomic.Int32
	seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
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
	require.EqualValues(t, initBalance+tagAlongFee, int(rdr.BalanceOf(testData.addrAux.AccountID())))

	auxOuts, err := rdr.GetOutputsForAccount(testData.addrAux.AccountID())
	require.EqualValues(t, 1, len(auxOuts))
	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

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

	testData.saveFullDAG("utangle_full")

	require.EqualValues(t, maxSlots, int(countBr.Load()))

	rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
	for _, txid := range par.spammedTxIDs {
		//require.True(t, rdr.KnowsCommittedTransaction(&txid))
		t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(txid))
	}
	targetBalance := rdr.BalanceOf(targetAddr.AccountID())
	require.EqualValues(t, maxBatches*batchSize*sendAmount, int(targetBalance))

	balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
	require.EqualValues(t, initBalance-len(par.spammedTxIDs)*sendAmount-par.numSpammedBatches*tagAlongFee, int(balanceLeft))
}

func TestFinalizeChainOrigins(t *testing.T) {
	const (
		nSequencers = 5 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	testData.stopAndWait()

	t.Logf("%s", testData.wrk.Info(true))
	testData.saveFullDAG("utangle_full")
}

func TestIdle2(t *testing.T) {
	const (
		maxSlots    = 50
		nSequencers = 1 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	//testData.env.StartTracingTags(task.TraceTagBootProposer)

	testData.startSequencersWithTimeout(maxSlots)
	time.Sleep(20 * time.Second)
	testData.stopAndWait()

	t.Logf("%s", testData.wrk.Info(false))
	testData.saveFullDAG("utangle_full")
	multistate.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
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

	testData.saveFullDAG("utangle_full")
	multistate.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
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
	require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.AccountID())))

	//initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

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
	//testData.saveFullDAG("utangle_full_3")

	rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
	for _, txid := range par.spammedTxIDs {
		//require.True(t, rdr.KnowsCommittedTransaction(&txid))
		t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(txid))
	}
	//require.EqualValues(t, (maxBatches+1)*batchSize, len(par.spammedTxIDs))

	targetBalance := rdr.BalanceOf(targetAddr.AccountID())
	require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

	balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
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
	require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.AccountID())))

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

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

	//testData.saveFullDAG(fmt.Sprintf("utangle_full_%d_2", nSequencers+1))
	multistate.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d_2", nSequencers+1))

	targetBalance := rdr.BalanceOf(targetAddr.AccountID())
	require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

	balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
	require.EqualValues(t, initBalance-len(par.spammedTxIDs)*sendAmount-par.numSpammedBatches*tagAlongFee, int(balanceLeft))

	for seqID, initBal := range tagAlongInitBalances {
		balanceOnChain := rdr.BalanceOnChain(seqID)
		t.Logf("%s tx: %d, init: %s, final: %s", seqID.StringShort(), par.perChainID[seqID], util.Th(initBal), util.Th(balanceOnChain))
		// inflation etc...
		//require.EqualValues(t, int(initBal)+par.perChainID[seqID]*tagAlongFee, int(balanceOnChain))
	}
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
	chainOriginsTxID, err := testData.wrk.TxBytesIn(testData.chainOriginsTx.Bytes())
	require.NoError(t, err)
	require.EqualValues(t, nSequencers, len(testData.chainOrigins))

	testData.bootstrapSeq, err = sequencer.New(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithPace(5),
		sequencer.WithDelayStart(3*time.Second),
	)
	require.NoError(t, err)

	//testData.wrk.StartTracingTags(sequencer.TraceTag)

	testData.bootstrapSeq.Start()

	baseline, err := testData.wrk.WaitUntilTransactionInHeaviestState(chainOriginsTxID, 10*time.Second)
	require.NoError(t, err)
	t.Logf("chain origins transaction %s has been created and finalized in baseline %s", chainOriginsTxID.StringShort(), baseline.IDShortString())
	return testData
}
