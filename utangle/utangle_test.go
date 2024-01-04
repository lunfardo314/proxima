package utangle

import (
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle/attacher"
	"github.com/lunfardo314/proxima/utangle/dag"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestOrigin(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		attacher.SetTraceOn()
		par := genesis.DefaultIdentityData(testutil.GetTestingPrivateKey())

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, root := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess)

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
		attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []core.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess)

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

		distribVID := dagAccess.GetVertex(vidDistrib.ID())
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
		attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []core.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess)

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

		distribVID := dagAccess.GetVertex(vidDistrib.ID())
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
		attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []core.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := newTestingWorkflow(txBytesStore, dagAccess)

		txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vidDistrib, err := attacher.AttachTransactionFromBytes(txBytes, wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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

		distribVID := dagAccess.GetVertex(vidDistrib.ID())
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
		for _, txBytes := range testData.txBytes {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()
	})
	t.Run("n double spends consumed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 5
		testData := initConflictTest(t, nConflicts, 0, true)
		for _, txBytes := range testData.txBytes {
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
		inTS := []core.LogicalTime{chainOut.Timestamp()}
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        core.MaxLogicalTime(inTS...).AddTicks(core.TransactionPaceInTicks),
			AdditionalInputs: testData.conflictingOutputs,
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
		for _, txBytes := range testData.txBytes {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()

		amount := uint64(0)
		for _, o := range testData.conflictingOutputs {
			amount += o.Output.Amount()
		}

		inTS := make([]core.LogicalTime, 0)
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, core.MaxLogicalTime(inTS...).AddTicks(core.TransactionPaceInTicks))
		td.WithAmount(amount).
			WithTargetLock(core.ChainLockFromChainID(testData.bootstrapChainID)).
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
			Timestamp:        outToConsume.Timestamp().AddTicks(core.TransactionPaceInTicks),
			AdditionalInputs: []*core.OutputWithID{&outToConsume},
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})

		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
		for _, txBytes := range testData.txBytes {
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
		inTS := []core.LogicalTime{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.Amount()
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        core.MaxLogicalTime(inTS...).AddTicks(core.TransactionPaceInTicks),
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
			howLong    = 1 // 97 fails when crosses slot boundary
		)
		testData := initLongConflictTestData(t, nConflicts, 0, howLong)
		for _, txBytes := range testData.txBytes {
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
		inTS := []core.LogicalTime{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.Amount()
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        core.MaxLogicalTime(inTS...).AddTicks(core.TransactionPaceInTicks),
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       testData.privKey,
			TotalSupply:      0,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
		testData.makeSeqStarts(false)

		err := testData.txStore.SaveTxBytes(testData.chainOriginsTx.Bytes())
		require.NoError(t, err)

		submitted := make([]*vertex.WrappedTx, nChains)
		wg.Add(len(testData.seqStart))
		for i, txBytes := range testData.seqStart {
			submitted[i], err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
			pullYN     = false
		)
		var wg sync.WaitGroup
		var err error

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqStarts(true)
		testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
		} else {
			testData.txBytesAttach()
		}

		submittedSeq := make([]*vertex.WrappedTx, nChains)
		wg.Add(len(testData.seqStart))
		for i, txBytes := range testData.seqStart {
			submittedSeq[i], err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
			howLong    = 2 // 97 fails when crosses slot boundary
			pullYN     = false
		)
		var wg sync.WaitGroup
		var err error

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqStarts(true)
		testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
			testData.storeTransactions(testData.seqStart...)
		} else {
			testData.txBytesAttach()
			testData.attachTransactions(testData.seqStart...)
		}

		chainIn := make([]*core.OutputWithChainID, len(testData.seqStart))
		var ts core.LogicalTime
		for i, txBytes := range testData.seqStart {
			tx, _ := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
			chainIn[i] = o.MustAsChainOutput()
			ts = core.MaxLogicalTime(ts, o.Timestamp())
		}
		ts = ts.AddTicks(core.TransactionPaceInTicks)
		txBytesSeq, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:      "seq",
			ChainInput:   chainIn[0],
			Timestamp:    ts,
			Endorsements: util.List(util.Ref(chainIn[1].ID.TransactionID())),
			PrivateKey:   testData.privKeyAux,
		})
		require.NoError(t, err)
		txid, _, _ := transaction.IDAndTimestampFromTransactionBytes(txBytesSeq)
		t.Logf("expected to fail: %s", txid.StringShort())

		wg.Add(1)
		vidSeq, err := attacher.AttachTransactionFromBytes(txBytesSeq, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
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
		attacher.SetTraceOn()
		const (
			nConflicts = 2
			nChains    = 2
			howLong    = 2 // 97 fails when crosses slot boundary
			pullYN     = false
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
		testData.makeSeqStarts(true)
		testData.printTxIDs()

		if pullYN {
			testData.txBytesToStore()
			testData.storeTransactions(testData.seqStart...)
		} else {
			testData.txBytesAttach()
			testData.attachTransactions(testData.seqStart...)
		}
		time.Sleep(5 * time.Second)

		chainIn := make([]*core.OutputWithChainID, len(testData.seqStart))
		var ts core.LogicalTime
		for i, txBytes := range testData.seqStart {
			tx, _ := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
			chainIn[i] = o.MustAsChainOutput()
			ts = core.MaxLogicalTime(ts, o.Timestamp())
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
			branches[i], err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithFinalizationCallback(func(vid *vertex.WrappedTx) {
				wg.Done()
			}))
			require.NoError(t, err)
		}
		wg.Wait()

		testData.logDAGInfo()
		testData.wrk.SaveGraph("utangle")
	})
}
