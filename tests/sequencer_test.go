package tests

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/tippool"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

type tippoolEnvironment struct {
	*workflow.Workflow
	seqID ledger.ChainID
}

func (t *tippoolEnvironment) SequencerID() ledger.ChainID {
	return t.seqID
}

func TestTippool(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	//attacher.SetTraceOn()
	const (
		nConflicts            = 3
		howLongConflictChains = 0 // 97 fails when crosses slot boundary
		nChains               = 3
		howLongSeqChains      = 3 // 95 fails
		nSlots                = 3
	)

	testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
	testData.makeSeqBeginnings(false)

	slotTransactions := make([][][]*transaction.Transaction, nSlots)
	branches := make([]*transaction.Transaction, nSlots)

	testData.txBytesAttach()
	extend := make([]*transaction.Transaction, nChains)
	for i := range extend {
		extend[i] = testData.seqChain[i][0]
	}
	testData.storeTransactions(extend...)
	prevBranch := testData.distributionBranchTx

	for branchNr := range branches {
		slotTransactions[branchNr] = testData.makeSlotTransactionsWithTagAlong(howLongSeqChains, extend)
		for _, txSeq := range slotTransactions[branchNr] {
			testData.storeTransactions(txSeq...)
		}

		extendSeqIdx := branchNr % nChains
		lastInChain := len(slotTransactions[branchNr][extendSeqIdx]) - 1
		extendOut := slotTransactions[branchNr][extendSeqIdx][lastInChain].SequencerOutput().MustAsChainOutput()
		branches[branchNr] = testData.makeBranch(extendOut, prevBranch)
		prevBranch = branches[branchNr]
		extend = testData.extendToNextSlot(slotTransactions[branchNr], prevBranch)
		testData.storeTransactions(extend...)
	}

	testData.storeTransactions(testData.transferChain...)

	testData.storeTransactions(branches...)

	tpools := make([]*tippool.SequencerTipPool, len(testData.chainOrigins))
	testData.wrk.EnableTraceTags("tippool")
	var err error

	for i := range tpools {
		env := &tippoolEnvironment{
			Workflow: testData.wrk,
			seqID:    testData.chainOrigins[i].ChainID,
		}
		tpools[i], err = tippool.New(env, fmt.Sprintf("seq_test%d", i), tippool.OptionDoNotLoadOwnMilestones)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
		wg.Done()
	}))
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()
	dag.SaveGraphPastCone(vidBranch, "utangle")
	require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())

	time.Sleep(500 * time.Millisecond)
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	t.Logf("Memory stats: allocated %.1f MB, Num GC: %d, Goroutines: %d, ",
		float32(memStats.Alloc*10/(1024*1024))/10,
		memStats.NumGC,
		runtime.NumGoroutine(),
	)
}

func Test1Sequencer(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	t.Run("idle", func(t *testing.T) {
		const maxSlots = 5
		testData := initWorkflowTest(t, 1)
		t.Logf("%s", testData.wrk.Info())

		//attacher.SetTraceOn()
		//testData.wrk.EnableTraceTags("seq,factory,tippool,txinput, proposer, incAttach")
		//testData.wrk.EnableTraceTags("persist_txbytes")
		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			ctx, sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})
		seq.Start()
		seq.WaitStop()
		testData.stopAndWait()
		require.EqualValues(t, maxSlots, int(countBr.Load()))
		require.EqualValues(t, maxSlots, int(countSeq.Load()))
		t.Logf("%s", testData.wrk.Info())
		//br := testData.wrk.HeaviestBranchOfLatestTimeSlot()
		//dag.SaveGraphPastCone(br, "latest_branch")
	})
	t.Run("tag along transfers", func(t *testing.T) {
		const (
			maxSlots    = 20
			batchSize   = 10
			maxBatches  = 5
			sendAmount  = 2000
			tagAlongFee = 200
		)
		testData := initWorkflowTest(t, 1)
		//t.Logf("%s", testData.wrk.Info())

		//attacher.SetTraceOn()
		//testData.wrk.EnableTraceTags(factory.TraceTag, proposer_base.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTag, pull_client.TraceTag)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachOutput, attacher.TraceTagAttachVertex)
		//testData.wrk.EnableTraceTags(factory.TraceTag)
		//testData.wrk.EnableTraceTags(workflow.TraceTagDelay)

		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			ctx, sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})

		seq.Start()

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance, rdr.BalanceOf(testData.addrAux.AccountID()))

		initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

		auxOuts, err := rdr.GetOutputsForAccount(testData.addrAux.AccountID())
		require.EqualValues(t, 1, len(auxOuts))
		targetPrivKey := testutil.GetTestingPrivateKey(10000)
		targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

		ctx, cancel := context.WithTimeout(context.Background(), (maxSlots+1)*ledger.SlotDuration())
		par := &spammerParams{
			privateKey:    testData.privKeyAux,
			remainder:     auxOuts[0],
			tagAlongSeqID: testData.bootstrapChainID,
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			maxBatches:    maxBatches,
			sendAmount:    sendAmount,
			tagAlongFee:   tagAlongFee,
			spammedTxIDs:  make([]ledger.TransactionID, 0),
		}
		go testData.spam(par, ctx)

		<-ctx.Done()
		cancel()

		require.EqualValues(t, batchSize*maxBatches, len(par.spammedTxIDs))

		seq.WaitStop()
		testData.stopAndWait()
		t.Logf("%s", testData.wrk.Info(true))

		testData.wrk.SaveGraph("utangle")

		require.EqualValues(t, maxSlots, int(countBr.Load()))

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), )
		}
		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, maxBatches*batchSize*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrAux.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), balanceLeft)

		balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
}

func TestNSequencers(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	t.Run("idle", func(t *testing.T) {
		const (
			maxSlots    = 3
			nSequencers = 1 // in addition to bootstrap
		)
		testData := initWorkflowTest(t, nSequencers)
		err := attacher.EnsureLatestBranches(testData.wrk)
		require.NoError(t, err)

		testData.makeChainOrigins(nSequencers)
		attacher.AttachTransaction(testData.chainOriginsTx, testData.wrk)

		require.EqualValues(t, nSequencers, len(testData.chainOrigins))

		//testData.wrk.EnableTraceTags("seq,factory,tippool,txinput, proposer, incAttach")
		//testData.wrk.EnableTraceTags("persist_txbytes")

		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			ctx, sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})
		seq.Start()
		seq.WaitStop()
		testData.stopAndWait()
		t.Logf("%s", testData.wrk.Info(true))

		require.EqualValues(t, maxSlots, int(countBr.Load()))
		require.EqualValues(t, maxSlots, int(countSeq.Load()))
		t.Logf("%s", testData.wrk.Info())
		//br := testData.wrk.HeaviestBranchOfLatestTimeSlot()
		//dag.SaveGraphPastCone(br, "latest_branch")
	})

}
