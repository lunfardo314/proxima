package core

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestOrigin(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		//attacher.SetTraceOn()
		par := genesis.DefaultIdentityData(testutil.GetTestingPrivateKey())

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, root := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess, context.Background())

		id, _, err := genesis.ScanGenesisState(stateStore)
		require.NoError(t, err)
		genesisOut := genesis.StemOutput(id.InitialSupply, id.GenesisTimeSlot)
		vidGenesis, err := attacher.EnsureBranch(genesisOut.ID.TransactionID(), wrk)
		require.NoError(t, err)

		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(vidGenesis))
		genesisOut1 := rdr.GetStemOutput()
		require.EqualValues(t, genesisOut.ID, genesisOut1.ID)
		require.EqualValues(t, genesisOut.Output.Bytes(), genesisOut1.Output.Bytes())

		wrk.syncLog()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())
		t.Logf("genesis root: %s", root.String())
		t.Logf("%s", dagAccess.Info())
	})
	t.Run("with distribution", func(t *testing.T) {
		//attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess, context.Background())

		txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
		require.NoError(t, err)

		distribTxID, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
		require.NoError(t, err)

		vidDistrib, err := attacher.EnsureBranch(distribTxID, wrk)
		require.NoError(t, err)

		wrk.syncLog()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", dagAccess.Info())

		distribVID := dagAccess.GetVertex(&vidDistrib.ID)
		require.True(t, distribVID != nil)

		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(distribVID))
		stemOut := rdr.GetStemOutput()
		require.EqualValues(t, distribTxID, stemOut.ID.TransactionID())
		require.EqualValues(t, 0, stemOut.Output.Amount())
		stemLock, ok := stemOut.Output.StemLock()
		require.True(t, ok)
		require.EqualValues(t, genesis.DefaultSupply, int(stemLock.Supply))

		bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000, int(bal2))
		require.EqualValues(t, 1, n2)

		balChain, nChain := multistate.BalanceOnLock(rdr, bootstrapChainID.AsChainLock())
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = multistate.BalanceOnChainOutput(rdr, &bootstrapChainID)
		require.EqualValues(t, genesis.DefaultSupply-1_000_000-2_000_000, int(balChain))
	})
	t.Run("sync scenario", func(t *testing.T) {
		//attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess, context.Background())

		txBytes, err := txbuilder.MakeDistributionTransaction(stateStore, privKey, distrib)
		require.NoError(t, err)

		distribTxID, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
		require.NoError(t, err)

		err = txBytesStore.SaveTxBytes(txBytes)
		require.NoError(t, err)
		require.True(t, len(txBytesStore.GetTxBytes(&distribTxID)) > 0)

		vidDistrib, err := attacher.EnsureBranch(distribTxID, wrk)
		require.NoError(t, err)
		wrk.syncLog()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", dagAccess.Info())

		distribVID := dagAccess.GetVertex(&vidDistrib.ID)
		require.True(t, distribVID != nil)

		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(distribVID))
		stemOut := rdr.GetStemOutput()

		require.EqualValues(t, distribTxID, stemOut.ID.TransactionID())
		require.EqualValues(t, 0, stemOut.Output.Amount())
		stemLock, ok := stemOut.Output.StemLock()
		require.True(t, ok)
		require.EqualValues(t, genesis.DefaultSupply, int(stemLock.Supply))

		bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000, int(bal2))
		require.EqualValues(t, 1, n2)

		balChain, nChain := multistate.BalanceOnLock(rdr, bootstrapChainID.AsChainLock())
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = multistate.BalanceOnChainOutput(rdr, &bootstrapChainID)
		require.EqualValues(t, genesis.DefaultSupply-1_000_000-2_000_000, int(balChain))

	})
	t.Run("with distribution tx", func(t *testing.T) {
		//attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess, context.Background())

		txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vidDistrib, err := attacher.AttachTransactionFromBytes(txBytes, wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			status := vid.GetTxStatus()
			if status == vertex.Good {
				err := txBytesStore.SaveTxBytes(txBytes)
				util.AssertNoError(err)
			}
			t.Logf("distribution tx finalized. Status: %s", vid.StatusString())
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", dagAccess.Info())

		distribVID := dagAccess.GetVertex(&vidDistrib.ID)
		require.True(t, distribVID != nil)
		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(distribVID))
		stemOut := rdr.GetStemOutput()

		distribTxID, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
		require.NoError(t, err)

		require.EqualValues(t, int(stemOut.ID.TimeSlot()), int(distribTxID.TimeSlot()))
		require.EqualValues(t, 0, stemOut.Output.Amount())
		stemLock, ok := stemOut.Output.StemLock()
		require.True(t, ok)
		require.EqualValues(t, genesis.DefaultSupply, int(stemLock.Supply))

		bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000, int(bal2))
		require.EqualValues(t, 1, n2)

		balChain, nChain := multistate.BalanceOnLock(rdr, bootstrapChainID.AsChainLock())
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = multistate.BalanceOnChainOutput(rdr, &bootstrapChainID)
		require.EqualValues(t, genesis.DefaultSupply-1_000_000-2_000_000, int(balChain))
	})
}

func TestConflicts1Attacher(t *testing.T) {
	t.Run("n double spends", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 10
		testData := initConflictTest(t, nConflicts, 0, false)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()
	})
	t.Run("n double spends consumed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 5
		testData := initConflictTest(t, nConflicts, 0, true)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()

		amount := uint64(0)
		for _, o := range testData.conflictingOutputs {
			amount += o.Output.Amount()
		}

		branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(branches))
		bd := branches[0]

		chainOut := bd.SequencerOutput.MustAsChainOutput()
		inTS := []ledger.LogicalTime{chainOut.Timestamp()}
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        ledger.MaxLogicalTime(inTS...).AddTicks(ledger.TransactionPaceInTicks),
			AdditionalInputs: testData.conflictingOutputs,
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		wg.Wait()
		require.NoError(t, err)
		testData.logDAGInfo()

		if nConflicts > 1 {
			require.True(t, vertex.Bad == vid.GetTxStatus())
			t.Logf("reason: %v", vid.GetReason())
			util.RequireErrorWith(t, vid.GetReason(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
		} else {
			require.True(t, vertex.Good == vid.GetTxStatus())
		}

	})
	t.Run("conflicting tx consumed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 2
		testData := initConflictTest(t, nConflicts, 0, false)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()

		amount := uint64(0)
		for _, o := range testData.conflictingOutputs {
			amount += o.Output.Amount()
		}

		inTS := make([]ledger.LogicalTime, 0)
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, ledger.MaxLogicalTime(inTS...).AddTicks(ledger.TransactionPaceInTicks))
		td.WithAmount(amount).
			WithTargetLock(ledger.ChainLockFromChainID(testData.bootstrapChainID)).
			MustWithInputs(testData.conflictingOutputs...)
		txBytesConflicting, err := txbuilder.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		vidConflicting, err := attacher.AttachTransactionFromBytes(txBytesConflicting, testData.wrk)
		require.NoError(t, err)
		testData.logDAGInfo()

		branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(branches))

		outToConsume := vidConflicting.MustOutputWithIDAt(0)
		chainOut := branches[0].SequencerOutput.MustAsChainOutput()
		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        outToConsume.Timestamp().AddTicks(ledger.TransactionPaceInTicks),
			AdditionalInputs: []*ledger.OutputWithID{&outToConsume},
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})

		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		wg.Wait()
		require.NoError(t, err)
		testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("reason: %v", vid.GetReason())
		util.RequireErrorWith(t, vid.GetReason(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
	t.Run("long", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 5
			howLong    = 96 // 97 fails when crosses slot boundary
		)
		testData := initLongConflictTestData(t, nConflicts, 0, howLong)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		for _, txSeq := range testData.txSequences {
			for _, txBytes := range txSeq {
				_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
				require.NoError(t, err)
			}
		}

		branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(branches))
		bd := branches[0]

		chainOut := bd.SequencerOutput.MustAsChainOutput()
		inTS := []ledger.LogicalTime{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.Amount()
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        ledger.MaxLogicalTime(inTS...).AddTicks(ledger.TransactionPaceInTicks),
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		//testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetReason())
		util.RequireErrorWith(t, vid.GetReason(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
	t.Run("long with sync", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 2
			howLong    = 90 // 97 fails when crosses slot boundary
		)
		testData := initLongConflictTestData(t, nConflicts, 0, howLong)
		for _, txBytes := range testData.txBytesConflicting {
			err := testData.txStore.SaveTxBytes(txBytes)
			require.NoError(t, err)
		}
		for _, txSeq := range testData.txSequences {
			for _, txBytes := range txSeq {
				err := testData.txStore.SaveTxBytes(txBytes)
				require.NoError(t, err)
			}
		}

		branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(branches))
		bd := branches[0]

		chainOut := bd.SequencerOutput.MustAsChainOutput()
		inTS := []ledger.LogicalTime{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.Amount()
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        ledger.MaxLogicalTime(inTS...).AddTicks(ledger.TransactionPaceInTicks),
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetReason())
		util.RequireErrorWith(t, vid.GetReason(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
}

func TestConflictsNAttachers(t *testing.T) {
	t.Run("seq start tx", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 10
			nChains    = 10
			howLong    = 2 // 97 fails when crosses slot boundary
		)
		var wg sync.WaitGroup
		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqBeginnings(false)

		err := testData.txStore.SaveTxBytes(testData.chainOriginsTx.Bytes())
		require.NoError(t, err)

		submitted := make([]*vertex.WrappedTx, nChains)
		wg.Add(len(testData.seqChain))
		for i, seqChain := range testData.seqChain {
			submitted[i], err = attacher.AttachTransactionFromBytes(seqChain[0].Bytes(), testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
				wg.Done()
			}))
			require.NoError(t, err)
		}
		wg.Wait()

		testData.logDAGInfo()

		for _, vid := range submitted {
			require.EqualValues(t, vertex.Good, vid.GetTxStatus())
		}
	})
	t.Run("seq start tx fee with-without pull", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 5
			nChains    = 5
			howLong    = 5 // 97 fails when crosses slot boundary
			pullYN     = true
		)
		var wg sync.WaitGroup
		var err error

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqBeginnings(true)
		testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
		} else {
			testData.txBytesAttach()
		}

		submittedSeq := make([]*vertex.WrappedTx, nChains)
		wg.Add(len(testData.seqChain))
		for i, seqChain := range testData.seqChain {
			submittedSeq[i], err = attacher.AttachTransactionFromBytes(seqChain[0].Bytes(), testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
				wg.Done()
			}))
			require.NoError(t, err)
		}
		wg.Wait()

		testData.logDAGInfo()

		for _, vid := range submittedSeq {
			require.EqualValues(t, vertex.Good, vid.GetTxStatus())
		}

		testData.wrk.ForEachVertex(func(vid *vertex.WrappedTx) bool {
			vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
				require.True(t, !v.Tx.IsSequencerMilestone() || v.FlagsUp(vertex.FlagsSequencerVertexCompleted))
			}})
			return true
		})

		//testData.wrk.SaveGraph("utangle")
	})
	t.Run("one fork", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 2
			nChains    = 2
			howLong    = 20 // 97 fails when crosses slot boundary
			pullYN     = true
		)
		var wg sync.WaitGroup
		var err error

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqBeginnings(true)
		testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
			for seqNr := range testData.seqChain {
				testData.storeTransactions(testData.seqChain[seqNr]...)
			}
		} else {
			testData.txBytesAttach()
			for seqNr := range testData.seqChain {
				testData.attachTransactions(testData.seqChain[seqNr]...)
			}
		}

		chainIn := make([]*ledger.OutputWithChainID, len(testData.seqChain))
		var ts ledger.LogicalTime
		for seqNr := range testData.seqChain {
			tx := testData.seqChain[seqNr][0]
			o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
			chainIn[seqNr] = o.MustAsChainOutput()
			ts = ledger.MaxLogicalTime(ts, o.Timestamp())
		}
		ts = ts.AddTicks(ledger.TransactionPaceInTicks)
		txBytesSeq, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:      "seq",
			ChainInput:   chainIn[0],
			Timestamp:    ts,
			Endorsements: util.List(util.Ref(chainIn[1].ID.TransactionID())),
			PrivateKey:   testData.privKeyAux,
		})
		require.NoError(t, err)
		txid, _, _ := transaction.IDAndTimestampFromTransactionBytes(txBytesSeq)
		t.Logf("seq tx expected to fail: %s", txid.StringShort())
		t.Logf("   chain input: %s", chainIn[0].ID.StringShort())
		t.Logf("   endrosement: %s", chainIn[1].ID.StringShort())

		wg.Add(1)
		vidSeq, err := attacher.AttachTransactionFromBytes(txBytesSeq, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		testData.logDAGInfo()

		require.EqualValues(t, vertex.Bad.String(), vidSeq.GetTxStatus().String())
		util.RequireErrorWith(t, vidSeq.GetReason(), "conflicts with existing consumers in the baseline state", "(double spend)", testData.forkOutput.IDShort())
		testData.wrk.SaveGraph("utangle")
	})
	t.Run("one fork, branches", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 2
			nChains    = 2
			howLong    = 5 // 97 fails when crosses slot boundary
			pullYN     = true
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqBeginnings(true)
		testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
			for seqNr := range testData.seqChain {
				testData.storeTransactions(testData.seqChain[seqNr]...)
			}
		} else {
			testData.txBytesAttach()
			for seqNr := range testData.seqChain {
				testData.attachTransactions(testData.seqChain[seqNr]...)
			}
		}

		chainIn := make([]*ledger.OutputWithChainID, len(testData.seqChain))
		var ts ledger.LogicalTime
		for seqNr := range testData.seqChain {
			tx := testData.seqChain[seqNr][0]
			o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
			chainIn[seqNr] = o.MustAsChainOutput()
			ts = ledger.MaxLogicalTime(ts, o.Timestamp())
		}
		ts = ts.NextTimeSlotBoundary()

		var err error
		var wg sync.WaitGroup
		branches := make([]*vertex.WrappedTx, len(chainIn))
		var txBytes []byte
		stem := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot()).GetStemOutput()
		for i := range chainIn {
			txBytes, err = txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
				SeqName:    "seq",
				StemInput:  stem,
				ChainInput: chainIn[i],
				Timestamp:  ts,
				PrivateKey: testData.privKeyAux,
			})
			require.NoError(t, err)
			wg.Add(1)
			branches[i], err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
				wg.Done()
			}))
			require.NoError(t, err)
			t.Logf("attaching branch %s", branches[i].IDShortString())
		}
		wg.Wait()

		testData.logDAGInfo()
		testData.wrk.SaveGraph("utangle")
	})
	t.Run("one fork branches conflict", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 5
			nChains    = 5
			howLong    = 5 // 97 fails when crosses slot boundary
			pullYN     = true
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqBeginnings(true)
		//testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
			for seqNr := range testData.seqChain {
				testData.storeTransactions(testData.seqChain[seqNr]...)
			}
		} else {
			testData.txBytesAttach()
			for seqNr := range testData.seqChain {
				testData.attachTransactions(testData.seqChain[seqNr]...)
			}
		}

		chainIn := make([]*ledger.OutputWithChainID, len(testData.seqChain))
		var ts ledger.LogicalTime
		for seqNr := range testData.seqChain {
			tx := testData.seqChain[seqNr][0]
			o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
			chainIn[seqNr] = o.MustAsChainOutput()
			ts = ledger.MaxLogicalTime(ts, o.Timestamp())
		}
		ts = ts.NextTimeSlotBoundary()

		var err error
		txBytesBranch := make([][]byte, nChains)
		require.True(t, len(txBytesBranch) >= 2)

		stem := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot()).GetStemOutput()
		for i := range chainIn {
			txBytesBranch[i], err = txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
				SeqName:    "seq",
				StemInput:  stem,
				ChainInput: chainIn[i],
				Timestamp:  ts,
				PrivateKey: testData.privKeyAux,
			})
			require.NoError(t, err)

			err = testData.txStore.SaveTxBytes(txBytesBranch[i])
			require.NoError(t, err)

			tx, err := transaction.FromBytes(txBytesBranch[i], transaction.MainTxValidationOptions...)
			require.NoError(t, err)
			t.Logf("branch #%d : %s", i, tx.IDShortString())
		}

		tx0, err := transaction.FromBytes(txBytesBranch[0], transaction.MainTxValidationOptions...)
		require.NoError(t, err)
		t.Logf("will be extending %s", tx0.IDShortString())

		tx1, err := transaction.FromBytes(txBytesBranch[1], transaction.MainTxValidationOptions...)
		require.NoError(t, err)
		t.Logf("will be endorsing %s", tx1.IDShortString())

		txBytesConflicting, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:      "dummy",
			ChainInput:   tx0.SequencerOutput().MustAsChainOutput(),
			Timestamp:    ts.AddTicks(ledger.TransactionPaceInTicks),
			Endorsements: util.List(tx1.ID()),
			PrivateKey:   testData.privKeyAux,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytesConflicting, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		wg.Wait()

		testData.logDAGInfo()
		testData.wrk.SaveGraph("utangle")

		require.EqualValues(t, vid.GetTxStatus(), vertex.Bad)
		t.Logf("expected error: %v", vid.GetReason())
		util.RequireErrorWith(t, vid.GetReason(), "is incompatible with baseline state", tx1.IDShortString())
	})
}

func TestSeqChains(t *testing.T) {
	t.Run("no pull order normal", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5
			nChains               = 5
			howLongConflictChains = 2  // 97 fails when crosses slot boundary
			howLongSeqChains      = 10 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		testData.printTxIDs()

		var wg sync.WaitGroup

		testData.txBytesAttach()
		vids := make([][]*vertex.WrappedTx, len(testData.seqChain))
		for seqNr, txSequence := range testData.seqChain {
			vids[seqNr] = make([]*vertex.WrappedTx, len(txSequence))
			for i, tx := range txSequence {
				wg.Add(1)
				vids[seqNr][i] = attacher.AttachTransaction(tx, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
					wg.Done()
				}))
			}
		}
		wg.Wait()
		testData.logDAGInfo()
		for _, txSequence := range vids {
			for _, vid := range txSequence {
				require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String())
			}
		}
	})
	t.Run("no pull transposed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5
			nChains               = 5
			howLongConflictChains = 2  // 97 fails when crosses slot boundary
			howLongSeqChains      = 10 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		testData.printTxIDs()

		var wg sync.WaitGroup

		testData.txBytesAttach()
		vids := make([][]*vertex.WrappedTx, len(testData.seqChain))

		seqlen := len(testData.seqChain[0])
		for seqNr := range testData.seqChain {
			vids[seqNr] = make([]*vertex.WrappedTx, seqlen)
		}
		for i := 0; i < seqlen; i++ {
			for seqNr, txSequence := range testData.seqChain {
				wg.Add(1)
				vids[seqNr][i] = attacher.AttachTransaction(txSequence[i], testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
					wg.Done()
				}))
			}
		}
		wg.Wait()
		testData.logDAGInfo()
		for _, txSequence := range vids {
			for _, vid := range txSequence {
				require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String())
			}
		}
	})
	t.Run("no pull reverse", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5
			nChains               = 5
			howLongConflictChains = 2  // 97 fails when crosses slot boundary
			howLongSeqChains      = 10 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		testData.printTxIDs()

		var wg sync.WaitGroup

		testData.txBytesAttach()
		vids := make([][]*vertex.WrappedTx, len(testData.seqChain))
		for seqNr, txSequence := range testData.seqChain {
			vids[seqNr] = make([]*vertex.WrappedTx, len(txSequence))
			for i := len(txSequence) - 1; i >= 0; i-- {
				wg.Add(1)
				vids[seqNr][i] = attacher.AttachTransaction(txSequence[i], testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
					wg.Done()
				}))
			}
		}
		wg.Wait()
		testData.logDAGInfo()
		for _, txSequence := range vids {
			for _, vid := range txSequence {
				require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String())
			}
		}
	})
	t.Run("with pull", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 10
			nChains               = 10
			howLongConflictChains = 2  // 97 fails when crosses slot boundary
			howLongSeqChains      = 90 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		//testData.printTxIDs()

		var wg sync.WaitGroup

		testData.txBytesAttach()
		vids := make([]*vertex.WrappedTx, len(testData.seqChain))
		for seqNr, txSequence := range testData.seqChain {
			for i, tx := range txSequence {
				if i < len(txSequence)-1 {
					err := testData.wrk.txBytesStore.SaveTxBytes(tx.Bytes())
					require.NoError(t, err)
				} else {
					wg.Add(1)
					vids[seqNr] = attacher.AttachTransaction(tx, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
						wg.Done()
					}))
				}
			}
		}
		wg.Wait()

		testData.logDAGInfo()
		for _, vid := range vids {
			require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String())
		}
		testData.wrk.SaveGraph("utangle")
	})
	t.Run("with 1 branch pull", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 10
			nChains               = 10
			howLongConflictChains = 2  // 97 fails when crosses slot boundary
			howLongSeqChains      = 50 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		//testData.printTxIDs()

		var wg sync.WaitGroup

		testData.txBytesAttach()
		for _, txSequence := range testData.seqChain {
			for _, tx := range txSequence {
				err := testData.wrk.txBytesStore.SaveTxBytes(tx.Bytes())
				require.NoError(t, err)
			}
		}

		distribBD, ok := multistate.FetchBranchData(testData.wrk.StateStore(), testData.distributionBranchTxID)
		require.True(t, ok)

		chainIn := testData.seqChain[0][len(testData.seqChain[0])-1].SequencerOutput().MustAsChainOutput()
		txBytesBranch, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:    "seq0",
			ChainInput: chainIn,
			StemInput:  distribBD.Stem,
			Timestamp:  chainIn.Timestamp().NextTimeSlotBoundary(),
			PrivateKey: testData.privKeyAux,
		})
		require.NoError(t, err)

		wg.Add(1)
		vidBranch, err := attacher.AttachTransactionFromBytes(txBytesBranch, testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		wg.Wait()

		testData.logDAGInfo()
		require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())
		//testData.wrk.SaveGraph("utangle")
		dag.SaveGraphPastCone(vidBranch, "utangle")
	})
	t.Run("with N branches pull", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5
			howLongConflictChains = 2 // 97 fails when crosses slot boundary
			nChains               = 5
			howLongSeqChains      = 10 // 95 fails
			nSlots                = 20
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
			slotTransactions[branchNr] = testData.makeSlotTransactions(howLongSeqChains, extend)
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

		testData.storeTransactions(branches...)
		var wg sync.WaitGroup
		wg.Add(1)
		vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		wg.Wait()

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
	})
	t.Run("N branches and transfers", func(t *testing.T) {
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
		var wg sync.WaitGroup
		wg.Add(1)
		vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.OptionWithFinalizationCallback(func(vid *vertex.WrappedTx) {
			wg.Done()
		}))
		wg.Wait()

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
	})
}
