package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func Test1SequencerPruner(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		const (
			maxSlots = 20
		)
		testData := initWorkflowTest(t, 1, true)
		t.Logf("%s", testData.wrk.Info())

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
		testData.saveFullDAG("full_dag")
	})
	t.Run("tag along transfers", func(t *testing.T) {
		const (
			maxSlots          = 40
			batchSize         = 10
			maxBatches        = 5
			sendAmount        = 2000
			branchMiningSteps = 100
		)
		testData := initWorkflowTest(t, 1, true)
		//t.Logf("%s", testData.wrk.Info())

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

		//initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

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
		t.Logf("%s", testData.wrk.Info(false))

		testData.saveFullDAG("utangle_full")

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

		//balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		//require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
}

func TestNSequencersIdlePruner(t *testing.T) {
	t.Run("finalize chain origins", func(t *testing.T) {
		const (
			nSequencers = 5 // in addition to bootstrap
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info(true))
		testData.saveFullDAG("utangle_full")
	})
	t.Run("idle 2", func(t *testing.T) {
		const (
			maxSlots    = 50
			nSequencers = 1 // in addition to bootstrap
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		//testData.env.StartTracingTags(attacher.TraceTagCoverageAdjustment)

		testData.startSequencersWithTimeout(maxSlots)
		time.Sleep(20 * time.Second)
		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info(false))
		testData.saveFullDAG("utangle_full")
		multistate.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
	})
}

func Test5SequencersIdlePruner(t *testing.T) {
	const (
		maxSlots    = 100
		nSequencers = 4 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	testData.startSequencersWithTimeout(maxSlots)
	time.Sleep(30 * time.Second)
	testData.stopAndWait()

	//t.Logf("--------\n%s", testData.wrk.Info(true))
	testData.saveFullDAG("utangle_full")
	multistate.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
}

func TestNSequencersTransferPruner(t *testing.T) {
	t.Run("seq 3 transfer 1 tag along", func(t *testing.T) {
		const (
			maxSlots        = 100
			nSequencers     = 2 // in addition to bootstrap
			batchSize       = 10
			sendAmount      = 2000
			spammingTimeout = 15 * time.Second
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		//testData.wrk.StartTracingTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.StartTracingTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)
		//testData.wrk.StartTracingTags(proposer_endorse1.TraceTag)
		//testData.wrk.StartTracingTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.StartTracingTags(factory.TraceTag)

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

		testData.startSequencersWithTimeout(maxSlots)

		<-ctx.Done()
		time.Sleep(5 * time.Second)
		testData.stopAndWait(3 * time.Second)

		t.Logf("%s", testData.wrk.Info())
		//testData.saveFullDAG("utangle_full_3")

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			//require.True(t, rdr.KnowsCommittedTransaction(&txid))
			t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
		}
		//require.EqualValues(t, (maxBatches+1)*batchSize, len(par.spammedTxIDs))

		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		//balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		//require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
	t.Run("seq 3 transfer multi tag along last only", func(t *testing.T) {
		const (
			maxSlots        = 100 // 100
			nSequencers     = 2   // in addition to bootstrap
			batchSize       = 10  // 10
			sendAmount      = 2000
			spammingTimeout = 60 * time.Second // 10
			startPruner     = true
			traceTx         = false
		)
		testData := initMultiSequencerTest(t, nSequencers, startPruner)

		//testData.env.StartTracingTags(attacher.TraceTagCoverageAdjustment)

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
			t:                t,
			privateKey:       testData.privKeyFaucet,
			remainder:        testData.faucetOutput,
			tagAlongSeqID:    tagAlongSeqIDs,
			target:           targetAddr,
			pace:             30,
			batchSize:        batchSize,
			tagAlongLastOnly: true,
			sendAmount:       sendAmount,
			tagAlongFee:      tagAlongFee,
			spammedTxIDs:     make([]ledger.TransactionID, 0),
			traceTx:          traceTx,
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
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
		}

		//testData.saveFullDAG(fmt.Sprintf("utangle_full_%d_2", nSequencers+1))
		multistate.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d_2", nSequencers+1))

		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*sendAmount-par.numSpammedBatches*tagAlongFee, int(balanceLeft))

		for seqID, initBal := range tagAlongInitBalances {
			balanceOnChain := rdr.BalanceOnChain(&seqID)
			t.Logf("%s tx: %d, init: %s, final: %s", seqID.StringShort(), par.perChainID[seqID], util.Th(initBal), util.Th(balanceOnChain))
			// inflation etc...
			//require.EqualValues(t, int(initBal)+par.perChainID[seqID]*tagAlongFee, int(balanceOnChain))
		}
	})
}
