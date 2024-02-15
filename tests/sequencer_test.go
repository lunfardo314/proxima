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
	"github.com/lunfardo314/proxima/util"
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
	testData.env.EnableTraceTags("tippool")
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
	//dag.SaveGraphPastCone(vidBranch, "utangle")
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
	t.Run("idle", func(t *testing.T) {
		const maxSlots = 5
		testData := initWorkflowTest(t, 1)
		t.Logf("%s", testData.wrk.Info())

		//testData.wrk.EnableTraceTags("seq,factory,tippool,txinput, proposer, incAttach")
		//testData.wrk.EnableTraceTags(sequencer.TraceTag, factory.TraceTag, tippool.TraceTag, proposer_base.TraceTag)

		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			}
		})
		seq.OnExit(func() {
			testData.stop()
		})
		seq.Start()

		testData.waitStop()
		require.EqualValues(t, maxSlots, int(countBr.Load()))
		t.Logf("%s", testData.wrk.Info())
		//br := testData.wrk.HeaviestBranchOfLatestTimeSlot()
		//dag.SaveGraphPastCone(br, "latest_branch")
	})
	t.Run("tag along transfers", func(t *testing.T) {
		const (
			maxSlots   = 20
			batchSize  = 10
			maxBatches = 5
			sendAmount = 2000
		)
		testData := initWorkflowTest(t, 1)
		//t.Logf("%s", testData.wrk.Info())

		//testData.wrk.EnableTraceTags(factory.TraceTag)

		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})
		seq.OnExit(func() {
			testData.stop()
		})
		seq.Start()

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance+tagAlongFee, int(rdr.BalanceOf(testData.addrAux.AccountID())))

		initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

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
			tagAlongSeqID: []ledger.ChainID{testData.bootstrapChainID},
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			maxBatches:    maxBatches,
			sendAmount:    sendAmount,
			tagAlongFee:   tagAlongFee,
			spammedTxIDs:  make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)

		<-ctx.Done()
		cancel()

		require.EqualValues(t, batchSize*maxBatches, len(par.spammedTxIDs))

		testData.waitStop()
		t.Logf("%s", testData.wrk.Info(true))

		testData.wrk.SaveGraph("utangle")
		testData.wrk.SaveTree("utangle_tree")

		require.EqualValues(t, maxSlots, int(countBr.Load()))

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), )
		}
		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, maxBatches*batchSize*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		inflation := int(balanceOnChain) - int(initialBalanceOnChain) + len(par.spammedTxIDs)*tagAlongFee
		t.Logf("initialBalanceOnChain: %s", util.GoTh(initialBalanceOnChain))
		t.Logf("earned: %s", util.GoTh(len(par.spammedTxIDs)*tagAlongFee))
		t.Logf("inflation: %s", util.GoTh(inflation))

		//require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
	t.Run("tag along transfers with inflation", func(t *testing.T) {
		const (
			maxSlots   = 20
			batchSize  = 10
			maxBatches = 5
			sendAmount = 2000
		)
		testData := initWorkflowTest(t, 1)
		//t.Logf("%s", testData.wrk.Info())

		//testData.wrk.EnableTraceTags(factory.TraceTag)

		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})
		seq.OnExit(func() {
			testData.stop()
		})

		rr := multistate.FetchLatestRootRecords(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(rr))
		initialSupply := rr[0].Supply

		seq.Start()

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance+tagAlongFee, int(rdr.BalanceOf(testData.addrAux.AccountID())))

		initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

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
			tagAlongSeqID: []ledger.ChainID{testData.bootstrapChainID},
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			maxBatches:    maxBatches,
			sendAmount:    sendAmount,
			tagAlongFee:   tagAlongFee,
			spammedTxIDs:  make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)

		<-ctx.Done()
		cancel()

		require.EqualValues(t, batchSize*maxBatches, len(par.spammedTxIDs))

		testData.waitStop()
		t.Logf("%s", testData.wrk.Info(true))

		testData.wrk.SaveGraph("utangle")
		testData.wrk.SaveTree("utangle_tree")

		require.EqualValues(t, maxSlots, int(countBr.Load()))

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), )
		}
		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, maxBatches*batchSize*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

		rr = multistate.FetchLatestRootRecords(testData.wrk.StateStore())
		require.True(t, len(rr) > 0)
		finalSupply := rr[0].Supply

		totalInflation := finalSupply - initialSupply
		t.Logf("total inflation: %s", util.GoTh(totalInflation))
		require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee+int(totalInflation), int(balanceOnChain))
	})
}

func initMultiSequencerTest(t *testing.T, nSequencers int, startPruner ...bool) *workflowTestData {
	testData := initWorkflowTest(t, nSequencers, startPruner...)
	//testData.wrk.EnableTraceTags(tippool.TraceTag)
	//testData.wrk.EnableTraceTags(factory.TraceTag)
	//testData.wrk.EnableTraceTags(attacher.TraceTagEnsureLatestBranches)

	err := attacher.EnsureLatestBranches(testData.wrk)
	require.NoError(t, err)

	testData.makeChainOrigins(nSequencers)
	chainOriginsTxID, err := testData.wrk.TxBytesIn(testData.chainOriginsTx.Bytes())
	require.NoError(t, err)
	require.EqualValues(t, nSequencers, len(testData.chainOrigins))

	testData.bootstrapSeq, err = sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
		sequencer.WithName("boot"),
		sequencer.WithMaxTagAlongInputs(30),
		sequencer.WithPace(5),
	)
	require.NoError(t, err)

	testData.bootstrapSeq.Start()

	baseline, err := testData.wrk.WaitUntilTransactionInHeaviestState(*chainOriginsTxID, 5*time.Second)
	require.NoError(t, err)
	t.Logf("chain origins transaction %s has been created and finalized in baseline %s", chainOriginsTxID.StringShort(), baseline.IDShortString())
	return testData
}

func TestNSequencersIdle(t *testing.T) {
	t.Run("finalize chain origins", func(t *testing.T) {
		const (
			nSequencers = 5 // in addition to bootstrap
		)
		testData := initMultiSequencerTest(t, nSequencers)

		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info(true))
		//testData.wrk.SaveGraph("utangle")
	})
	t.Run("idle 2", func(t *testing.T) {
		const (
			maxSlots    = 50
			nSequencers = 1 // in addition to bootstrap
		)
		testData := initMultiSequencerTest(t, nSequencers)

		//testData.wrk.EnableTraceTags(proposer_endorse1.TraceTag)
		//testData.wrk.EnableTraceTags(proposer_base.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)

		testData.startSequencersWithTimeout(maxSlots)
		time.Sleep(20 * time.Second)
		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info(true))
		testData.wrk.SaveGraph("utangle")
		dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
	})
}

func Test5SequencersIdle(t *testing.T) {
	const (
		maxSlots    = 100
		nSequencers = 4 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers)

	//testData.wrk.EnableTraceTags(proposer_base.TraceTag)
	testData.startSequencersWithTimeout(maxSlots)
	time.Sleep(20 * time.Second)
	testData.stopAndWait()

	t.Logf("--------\n%s", testData.wrk.Info())
	//testData.wrk.SaveGraph("utangle")
	testData.wrk.SaveSequencerGraph(fmt.Sprintf("utangle_seq_tree_%d", nSequencers+1))
	dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))

}

func TestNSequencersTransfer(t *testing.T) {
	t.Run("seq 3 transfer 1 tag along", func(t *testing.T) {
		const (
			maxSlots        = 100
			nSequencers     = 2 // in addition to bootstrap
			batchSize       = 10
			sendAmount      = 2000
			spammingTimeout = 15 * time.Second
		)
		testData := initMultiSequencerTest(t, nSequencers)

		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)
		//testData.wrk.EnableTraceTags(proposer_endorse1.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(factory.TraceTag)

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
			tagAlongSeqID: []ledger.ChainID{testData.bootstrapChainID},
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			//maxBatches:    maxBatches,
			sendAmount:   sendAmount,
			tagAlongFee:  tagAlongFee,
			spammedTxIDs: make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)
		go func() {
			<-ctx.Done()
			cancelSpam()
			t.Log("spamming stopped")
		}()

		testData.startSequencersWithTimeout(maxSlots, spammingTimeout+(5*time.Second))

		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info())
		//testData.wrk.SaveGraph("utangle")
		dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
		}
		//require.EqualValues(t, (maxBatches+1)*batchSize, len(par.spammedTxIDs))

		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		//balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		//require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
	t.Run("seq 3 transfer multi tag along", func(t *testing.T) {
		const (
			maxSlots        = 100
			nSequencers     = 2 // in addition to bootstrap
			batchSize       = 10
			sendAmount      = 2000
			spammingTimeout = 10 * time.Second
		)
		testData := initMultiSequencerTest(t, nSequencers)

		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)
		//testData.wrk.EnableTraceTags(proposer_endorse1.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(factory.TraceTag)

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.AccountID())))

		targetPrivKey := testutil.GetTestingPrivateKey(10000)
		targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

		tagAlongSeqIDs := []ledger.ChainID{testData.bootstrapChainID}
		for _, o := range testData.chainOrigins {
			tagAlongSeqIDs = append(tagAlongSeqIDs, o.ChainID)
		}
		tagAlongInitBalances := make(map[ledger.ChainID]uint64)
		for _, seqID := range tagAlongSeqIDs {
			tagAlongInitBalances[seqID] = rdr.BalanceOnChain(&seqID)
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
			//maxBatches:    maxBatches,
			sendAmount:   sendAmount,
			tagAlongFee:  tagAlongFee,
			spammedTxIDs: make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)
		go func() {
			<-ctx.Done()
			cancelSpam()
			t.Log("spamming stopped")
		}()

		testData.startSequencersWithTimeout(maxSlots, spammingTimeout+(5*time.Second))

		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info())
		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
		}

		//testData.wrk.SaveSequencerGraph(fmt.Sprintf("utangle_seq_tree_%d", nSequencers+1))
		dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))

		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		for seqID, initBal := range tagAlongInitBalances {
			balanceOnChain := rdr.BalanceOnChain(&seqID)
			t.Logf("%s tx: %d, init: %s, final: %s", seqID.StringShort(), par.perChainID[seqID], util.GoTh(initBal), util.GoTh(balanceOnChain))
			//require.EqualValues(t, int(initBal)+par.perChainID[seqID]*tagAlongFee, int(balanceOnChain))
		}
	})
}
