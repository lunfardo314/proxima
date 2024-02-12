package tests

import (
	"context"
	"crypto/ed25519"
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
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}

func TestBasic(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		//attacher.SetTraceOn()

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, root := multistate.InitStateStore(*ledger.L().ID, stateStore)
		dagAccess := dag.New(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		wrk := workflow.New(stateStore, txBytesStore, peering.NewPeersDummy(),
			workflow.WithLogLevel(zapcore.DebugLevel), workflow.OptionDoNotStartPruner)
		ctx, stop := context.WithCancel(context.Background())
		wrk.Start(ctx)

		id, _, err := multistate.ScanGenesisState(stateStore)
		require.NoError(t, err)
		genesisOut := multistate.StemOutput(id.GenesisSlot)
		vidGenesis, err := attacher.EnsureBranch(genesisOut.ID.TransactionID(), wrk)
		require.NoError(t, err)

		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(&vidGenesis.ID))
		genesisOut1 := rdr.GetStemOutput()
		require.EqualValues(t, genesisOut.ID, genesisOut1.ID)
		require.EqualValues(t, genesisOut.Output.Bytes(), genesisOut1.Output.Bytes())

		stop()
		wrk.WaitStop()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())
		t.Logf("genesis root: %s", root.String())
		t.Logf("%s", dagAccess.Info())
	})
	t.Run("with distribution", func(t *testing.T) {
		//attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := ledger.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := multistate.InitStateStore(*par, stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

		wrk := workflow.New(stateStore, txBytesStore, peering.NewPeersDummy(),
			workflow.WithLogLevel(zapcore.DebugLevel), workflow.OptionDoNotStartPruner)
		ctx, stop := context.WithCancel(context.Background())
		wrk.Start(ctx)

		txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
		require.NoError(t, err)

		distribTxID, err := transaction.IDFromTransactionBytes(txBytes)
		require.NoError(t, err)

		vidDistrib, err := attacher.EnsureBranch(distribTxID, wrk)
		require.NoError(t, err)

		stop()
		wrk.WaitStop()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", wrk.Info())

		distribVID := wrk.GetVertex(&vidDistrib.ID)
		require.True(t, distribVID != nil)

		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(&distribVID.ID))
		stemOut := rdr.GetStemOutput()
		require.EqualValues(t, distribTxID, stemOut.ID.TransactionID())
		require.EqualValues(t, 0, stemOut.Output.Amount())

		rr, ok := multistate.FetchRootRecord(wrk.StateStore(), distribVID.ID)
		require.True(t, ok)
		require.EqualValues(t, ledger.DefaultInitialSupply, int(rr.Supply))
		require.EqualValues(t, 0, int(rr.SlotInflation))

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
		require.EqualValues(t, ledger.DefaultInitialSupply-1_000_000-2_000_000, int(balChain))
	})
	t.Run("sync scenario", func(t *testing.T) {
		//attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := ledger.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := multistate.InitStateStore(*par, stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

		wrk := workflow.New(stateStore, txBytesStore, peering.NewPeersDummy(),
			workflow.WithLogLevel(zapcore.DebugLevel), workflow.OptionDoNotStartPruner)
		ctx, stop := context.WithCancel(context.Background())

		//wrk.EnableTraceTags(attacher.TraceTagAttach, attacher.TraceTagAttachMilestone, attacher.TraceTagAttachVertex)
		//wrk.EnableTraceTags(attacher.TraceTagAttachEndorsements, attacher.TraceTagAttachOutput)
		//wrk.EnableTraceTags(attacher.TraceTagMarkDefUndef)
		wrk.Start(ctx)

		txBytes, err := txbuilder.MakeDistributionTransaction(stateStore, privKey, distrib)
		require.NoError(t, err)

		distribTxID, err := transaction.IDFromTransactionBytes(txBytes)
		require.NoError(t, err)

		_, err = txBytesStore.PersistTxBytesWithMetadata(txBytes, nil)
		require.NoError(t, err)
		require.True(t, len(txBytesStore.GetTxBytesWithMetadata(&distribTxID)) > 0)

		vidDistrib, err := attacher.EnsureBranch(distribTxID, wrk)
		require.NoError(t, err)

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", wrk.Info())

		stop()
		wrk.WaitStop()

		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(&vidDistrib.ID))
		stemOut := rdr.GetStemOutput()

		require.EqualValues(t, distribTxID, stemOut.ID.TransactionID())
		require.EqualValues(t, 0, stemOut.Output.Amount())

		rr, ok := multistate.FetchRootRecord(wrk.StateStore(), distribTxID)
		require.True(t, ok)
		require.EqualValues(t, ledger.DefaultInitialSupply, int(rr.Supply))
		require.EqualValues(t, 0, int(rr.SlotInflation))

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
		require.EqualValues(t, ledger.DefaultInitialSupply-1_000_000-2_000_000, int(balChain))

	})
	t.Run("with distribution tx", func(t *testing.T) {
		//attacher.SetTraceOn()
		privKey := testutil.GetTestingPrivateKey()
		par := ledger.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := multistate.InitStateStore(*par, stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

		wrk := workflow.New(stateStore, txBytesStore, peering.NewPeersDummy(),
			workflow.WithLogLevel(zapcore.DebugLevel), workflow.OptionDoNotStartPruner)
		ctx, stop := context.WithCancel(context.Background())
		wrk.Start(ctx)

		txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vidDistrib, err := attacher.AttachTransactionFromBytes(txBytes, wrk, attacher.OptionWithAttachmentCallback(func(vid *vertex.WrappedTx, err error) {
			status := vid.GetTxStatus()
			if status == vertex.Good {
				_, err := txBytesStore.PersistTxBytesWithMetadata(txBytes, nil)
				util.AssertNoError(err)
			}
			t.Logf("distribution tx finalized. Status: %s", vid.StatusString())
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", wrk.Info())

		stop()
		wrk.WaitStop()

		distribVID := wrk.GetVertex(&vidDistrib.ID)
		require.True(t, distribVID != nil)
		rdr := multistate.MakeSugared(wrk.GetStateReaderForTheBranch(&distribVID.ID))
		stemOut := rdr.GetStemOutput()

		distribTxID, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
		require.NoError(t, err)

		require.EqualValues(t, int(stemOut.ID.TimeSlot()), int(distribTxID.Slot()))
		require.EqualValues(t, 0, stemOut.Output.Amount())

		rr, ok := multistate.FetchRootRecord(wrk.StateStore(), stemOut.ID.TransactionID())
		require.True(t, ok)
		require.EqualValues(t, ledger.DefaultInitialSupply, int(rr.Supply))
		require.EqualValues(t, 0, int(rr.SlotInflation))

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
		require.EqualValues(t, ledger.DefaultInitialSupply-1_000_000-2_000_000, int(balChain))
	})
}

func TestConflicts1Attacher(t *testing.T) {
	t.Run("n double spends", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 10
		testData := initWorkflowTestWithConflicts(t, nConflicts, 1, false)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()
	})
	t.Run("n double spends consumed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 5
		testData := initWorkflowTestWithConflicts(t, nConflicts, 1, true)
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
		inTS := []ledger.Time{chainOut.Timestamp()}
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        ledger.MaxTime(inTS...).AddTicks(ledger.TransactionPaceSequencer()),
			AdditionalInputs: testData.conflictingOutputs,
			PrivateKey:       testData.privKey,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()
		require.NoError(t, err)
		testData.logDAGInfo()

		if nConflicts > 1 {
			require.True(t, vertex.Bad == vid.GetTxStatus())
			t.Logf("reason: %v", vid.GetError())
			util.RequireErrorWith(t, vid.GetError(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
		} else {
			require.True(t, vertex.Good == vid.GetTxStatus())
		}

	})
	t.Run("conflicting tx consumed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 2
		testData := initWorkflowTestWithConflicts(t, nConflicts, 1, false)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()

		amount := uint64(0)
		for _, o := range testData.conflictingOutputs {
			amount += o.Output.Amount()
		}

		inTS := make([]ledger.Time, 0)
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, ledger.MaxTime(inTS...).AddTicks(ledger.TransactionPace()))
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
			Timestamp:        outToConsume.Timestamp().AddTicks(ledger.TransactionPaceSequencer()),
			AdditionalInputs: []*ledger.OutputWithID{&outToConsume},
			PrivateKey:       testData.privKey,
		})

		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()
		require.NoError(t, err)
		testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("reason: %v", vid.GetError())
		util.RequireErrorWith(t, vid.GetError(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
	t.Run("long", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 5
			howLong    = 96 // 97 fails when crosses slot boundary
		)
		testData := initLongConflictTestData(t, nConflicts, 5, howLong)
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
		inTS := []ledger.Time{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.Amount()
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        ledger.MaxTime(inTS...).AddTicks(ledger.TransactionPaceSequencer()),
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       testData.privKey,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		//testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetError())
		util.RequireErrorWith(t, vid.GetError(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
	t.Run("long with sync", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 2
			howLong    = 90 // 97 fails when crosses slot boundary
		)
		testData := initLongConflictTestData(t, nConflicts, nConflicts, howLong)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := testData.txStore.PersistTxBytesWithMetadata(txBytes, nil)
			require.NoError(t, err)
		}
		for _, txSeq := range testData.txSequences {
			for _, txBytes := range txSeq {
				_, err := testData.txStore.PersistTxBytesWithMetadata(txBytes, nil)
				require.NoError(t, err)
			}
		}

		branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(branches))
		bd := branches[0]

		chainOut := bd.SequencerOutput.MustAsChainOutput()
		inTS := []ledger.Time{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.Amount()
		}

		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOut,
			Timestamp:        ledger.MaxTime(inTS...).AddTicks(ledger.TransactionPaceSequencer()),
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       testData.privKey,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		testData.wrk.EnableTraceTags("delay")

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		testData.stopAndWait()

		testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetError())
		util.RequireErrorWith(t, vid.GetError(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
}

func TestConflictsNAttachersSeqStartTx(t *testing.T) {
	//attacher.SetTraceOn()
	const (
		nConflicts = 10
		nChains    = 10
		howLong    = 2 // 97 fails when crosses slot boundary
	)
	var wg sync.WaitGroup
	testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
	testData.makeSeqBeginnings(false)

	_, err := testData.txStore.PersistTxBytesWithMetadata(testData.chainOriginsTx.Bytes(), nil)
	require.NoError(t, err)

	submitted := make([]*vertex.WrappedTx, nChains)
	wg.Add(len(testData.seqChain))
	for i, seqChain := range testData.seqChain {
		submitted[i], err = attacher.AttachTransactionFromBytes(seqChain[0].Bytes(), testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
	}
	wg.Wait()
	testData.stopAndWait()

	testData.logDAGInfo()

	for _, vid := range submitted {
		require.EqualValues(t, vertex.Good, vid.GetTxStatus())
	}
}

func TestConflictsNAttachersSeqStartTxFee(t *testing.T) {
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
		submittedSeq[i], err = attacher.AttachTransactionFromBytes(seqChain[0].Bytes(), testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
	}
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()

	for _, vid := range submittedSeq {
		require.EqualValues(t, vertex.Good, vid.GetTxStatus())
	}

	for _, vid := range testData.wrk.Vertices() {
		if !vid.FlagsUp(vertex.FlagVertexConstraintsValid | vertex.FlagVertexTxBytesPersisted) {
			t.Logf("wrong flags: %s", vid.String())
		}
		if vid.IsVirtualTx() {
			require.True(t, vid.FlagsUp(vertex.FlagVertexDefined))
		} else {
			require.True(t, vid.FlagsUp(vertex.FlagVertexConstraintsValid|vertex.FlagVertexTxBytesPersisted))
		}
		if vid.IsSequencerMilestone() {
			require.True(t, vid.GetTxStatus() == vertex.Good)
		} else {
			require.True(t, vid.GetTxStatus() == vertex.Undefined)
		}
	}

	//testData.wrk.SaveGraph("utangle")
}

func TestConflictsNAttachersOneFork(t *testing.T) {
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
	//testData.printTxIDs()

	//testData.wrk.EnableTraceTags(attacher.TraceTagAttach)

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
	var ts ledger.Time
	for seqNr := range testData.seqChain {
		tx := testData.seqChain[seqNr][0]
		o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
		chainIn[seqNr] = o.MustAsChainOutput()
		ts = ledger.MaxTime(ts, o.Timestamp())
	}
	ts = ts.AddTicks(ledger.TransactionPace())
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
	vidSeq, err := attacher.AttachTransactionFromBytes(txBytesSeq, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
		wg.Done()
	}))
	require.NoError(t, err)
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()

	require.EqualValues(t, vertex.Bad.String(), vidSeq.GetTxStatus().String())
	util.RequireErrorWith(t, vidSeq.GetError(), "conflicts with existing consumers in the baseline state", "(double spend)", testData.forkOutput.IDShort())
	testData.wrk.SaveGraph("utangle")
}

func TestConflictsNAttachersOneForkBranches(t *testing.T) {
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
	var ts ledger.Time
	for seqNr := range testData.seqChain {
		tx := testData.seqChain[seqNr][0]
		o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
		chainIn[seqNr] = o.MustAsChainOutput()
		ts = ledger.MaxTime(ts, o.Timestamp())
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
		branches[i], err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		t.Logf("attaching branch %s", branches[i].IDShortString())
	}
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()
	testData.wrk.SaveGraph("utangle")
}

func TestConflictsNAttachersOneForkBranchesConflict(t *testing.T) {
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
	var ts ledger.Time
	for seqNr := range testData.seqChain {
		tx := testData.seqChain[seqNr][0]
		o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
		chainIn[seqNr] = o.MustAsChainOutput()
		ts = ledger.MaxTime(ts, o.Timestamp())
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

		_, err = testData.txStore.PersistTxBytesWithMetadata(txBytesBranch[i], nil)
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
		Timestamp:    ts.AddTicks(ledger.TransactionPace()),
		Endorsements: util.List(tx1.ID()),
		PrivateKey:   testData.privKeyAux,
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	vid, err := attacher.AttachTransactionFromBytes(txBytesConflicting, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
		wg.Done()
	}))
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()

	testData.wrk.SaveGraph("utangle")

	require.EqualValues(t, vid.GetTxStatus(), vertex.Bad)
	t.Logf("expected error: %v", vid.GetError())
	util.RequireErrorWith(t, vid.GetError(), "is incompatible with the baseline branch", tx1.IDShortString())
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
				vids[seqNr][i] = attacher.AttachTransaction(tx, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
					wg.Done()
				}))
			}
		}
		wg.Wait()

		testData.stopAndWait()
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
				vids[seqNr][i] = attacher.AttachTransaction(txSequence[i], testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
					wg.Done()
				}))
			}
		}
		wg.Wait()
		testData.stopAndWait()
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
				vids[seqNr][i] = attacher.AttachTransaction(txSequence[i], testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
					wg.Done()
				}))
			}
		}
		wg.Wait()
		testData.stopAndWait()
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
			howLongSeqChains      = 50 // 90 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		//testData.printTxIDs()

		testData.wrk.EnableTraceTags(workflow.TraceTagDelay)
		//testData.wrk.EnableTraceTags(attacher.TraceTagMarkDefUndef)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachEndorsements, attacher.TraceTagAttachVertex)

		var wg sync.WaitGroup

		testData.txBytesAttach()
		vids := make([]*vertex.WrappedTx, len(testData.seqChain))
		for seqNr, txSequence := range testData.seqChain {
			for i, tx := range txSequence {
				if i < len(txSequence)-1 {
					_, err := testData.wrk.PersistTxBytesWithMetadata(tx.Bytes(), nil)
					require.NoError(t, err)
				} else {
					wg.Add(1)
					vids[seqNr] = attacher.AttachTransaction(tx, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
						wg.Done()
					}))
				}
			}
		}
		wg.Wait()

		testData.stopAndWait()
		//testData.logDAGInfo()
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
			howLongSeqChains      = 10 // 95 fails
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)
		testData.makeSeqChains(howLongSeqChains)
		//testData.printTxIDs()

		var wg sync.WaitGroup

		testData.txBytesAttach()
		for _, txSequence := range testData.seqChain {
			for _, tx := range txSequence {
				_, err := testData.wrk.PersistTxBytesWithMetadata(tx.Bytes(), nil)
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

		//testData.wrk.EnableTraceTags(attacher.TraceTagAttach, attacher.TraceTagAttachVertex)
		//testData.wrk.EnableTraceTags(poker.TraceTag, pull_client.TraceTag, pull_server.TraceTag)

		wg.Add(1)
		vidBranch, err := attacher.AttachTransactionFromBytes(txBytesBranch, testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()

		testData.stopAndWait()
		testData.logDAGInfo()
		require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())
		//testData.wrk.SaveGraph("utangle")
		dag.SaveGraphPastCone(vidBranch, "utangle")
	})
	t.Run("with N branches pull", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5 // 5
			nChains               = 5
			howLongConflictChains = 5  // 97 fails when crosses slot boundary
			howLongSeqChains      = 10 // 10 // 95 fails
			nSlots                = 5
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
			lastInChainIdx := len(slotTransactions[branchNr][extendSeqIdx]) - 1
			extendOut := slotTransactions[branchNr][extendSeqIdx][lastInChainIdx].SequencerOutput().MustAsChainOutput()
			branches[branchNr] = testData.makeBranch(extendOut, prevBranch)
			prevBranch = branches[branchNr]
			t.Logf("makeBranch: %s", prevBranch.IDShortString())
			beginExtension := make([]*transaction.Transaction, len(slotTransactions[branchNr]))
			for i := range beginExtension {
				beginExtension[i] = util.MustLastElement(slotTransactions[branchNr][i])
			}
			extend = testData.extendToNextSlot(slotTransactions[branchNr], prevBranch)

			testData.storeTransactions(extend...)
		}

		testData.wrk.EnableTraceTags(workflow.TraceTagDelay)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachEndorsements)
		testData.storeTransactions(branches...)
		var wg sync.WaitGroup
		wg.Add(1)
		vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.OptionWithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()

		testData.stopAndWait()
		testData.logDAGInfo()
		testData.wrk.SaveGraph("utangle")
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
	})
	t.Run("with N branches and transfers", func(t *testing.T) {
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

		testData.wrk.EnableTraceTags("persist_txbytes")

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
	})
	t.Run("with N branches,transfers,inflation", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 3
			howLongConflictChains = 0 // 97 fails when crosses slot boundary
			nChains               = 3
			howLongSeqChains      = 3 // 95 fails
			nSlots                = 3
			inflateSeqMilestones  = true
		)

		testData := initLongConflictTestData(t, nConflicts, nChains, howLongConflictChains)
		testData.makeSeqBeginnings(false)

		testData.wrk.EnableTraceTags(sequencer.TraceTag + "_tx")

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
			slotTransactions[branchNr] = testData.makeSlotTransactionsWithTagAlong(howLongSeqChains, extend, inflateSeqMilestones)
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

		testData.wrk.EnableTraceTags("persist_txbytes")

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
	})
}
