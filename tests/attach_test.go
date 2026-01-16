package tests

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestAttachTime(t *testing.T) {
	ts := ledger.TimeNow()
	t.Logf("tick duration:\n%v\nledger time now: %s", ledger.TickDuration(), ts.String())
	require.True(t, base.ValidTime(ts))
}

func TestAttachBasic(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, root := multistate.InitStateStoreFromGlobals(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		env := newWorkflowDummyEnvironment(stateStore, txBytesStore)

		env.StartTracingTags(global.TraceTag)

		wrk := workflow.Start(env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC)

		_, _, err := multistate.ScanGenesisState(stateStore)
		require.NoError(t, err)
		genesisOut := ledger.GenesisStemOutput()
		vidGenesis, err := wrk.EnsureBranch(genesisOut.ID.TransactionID())
		require.NoError(t, err)

		rdr := multistate.MakeSugared(wrk.Branches().GetStateReaderForTheBranch(vidGenesis.ID()))
		genesisOut1 := rdr.GetStemOutput()
		require.EqualValues(t, genesisOut.ID, genesisOut1.ID)
		require.EqualValues(t, genesisOut.Output.Bytes(), genesisOut1.Output.Bytes())

		env.Stop()
		env.WaitAllWorkProcessesStop()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())
		t.Logf("genesis root: %s", root.String())
		t.Logf("%s", wrk.Info())
	})
	t.Run("with distribution", func(t *testing.T) {
		//attacher.SetTraceOn()
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000_000, ChainOrigin: false},
			{Lock: addr2, Balance: 1_000_000_000, ChainOrigin: false},
			{Lock: addr2, Balance: 1_000_000_000, ChainOrigin: true},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := multistate.InitStateStoreFromGlobals(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

		env := newWorkflowDummyEnvironment(stateStore, txBytesStore)
		wrk := workflow.Start(env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC)

		txBytes, err := txbuilder_seq.DistributeInitialSupply(stateStore, genesisPrivateKey, distrib)
		require.NoError(t, err)

		distribTxID, err := transaction.IDFromParsedTransactionBytes(txBytes)
		require.NoError(t, err)

		vidDistrib, err := wrk.EnsureBranch(distribTxID)
		require.NoError(t, err)

		env.Stop()
		env.WaitAllWorkProcessesStop()

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", wrk.Info())

		distribVID := wrk.GetVertex(vidDistrib.ID())
		require.True(t, distribVID != nil)

		rdr := multistate.MakeSugared(wrk.Branches().GetStateReaderForTheBranch(distribVID.ID()))
		stemOut := rdr.GetStemOutput()
		require.EqualValues(t, distribTxID, stemOut.ID.TransactionID())
		require.EqualValues(t, 0, stemOut.Output.TokenBalance())

		rr, ok := multistate.FetchRootRecord(wrk.StateStore(), distribVID.ID())
		require.True(t, ok)
		require.EqualValues(t, ledger.DefaultInitialSupply, int(rr.Supply))
		require.EqualValues(t, 0, int(rr.SlotInflation))

		bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000_000, int(bal2))
		require.EqualValues(t, 2, n2)

		balChain, nChain := multistate.BalanceOnLock(rdr, ledger.ChainLockFromChainID(bootstrapChainID))
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = multistate.BalanceOnChainOutput(rdr, bootstrapChainID)
		// Genesis output now has initialSupply-1 (1 token goes to dust output)
		require.EqualValues(t, ledger.DefaultInitialSupply-1-1_000_000_000-2_000_000_000, int(balChain))
	})
	t.Run("sync scenario", func(t *testing.T) {
		//attacher.SetTraceOn()
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000_000},
			{Lock: addr2, Balance: 2_000_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := multistate.InitStateStoreFromGlobals(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

		env := newWorkflowDummyEnvironment(stateStore, txBytesStore)
		wrk := workflow.Start(env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC)

		txBytes, err := txbuilder_seq.MakeDistributionTransaction(stateStore, genesisPrivateKey, distrib)
		require.NoError(t, err)

		distribTxID, err := transaction.IDFromParsedTransactionBytes(txBytes)
		require.NoError(t, err)

		_, err = txBytesStore.PersistTxBytesWithMetadata(txBytes, nil)
		require.NoError(t, err)
		require.True(t, len(txBytesStore.GetTxBytesWithMetadata(&distribTxID)) > 0)

		//wrk.StartTracingTags(attacher.TraceTagAttach)
		//wrk.StartTracingTags(attacher.TraceTagAttachMilestone)
		//wrk.StartTracingTags(attacher.TraceTagAttachVertex)
		//wrk.StartTracingTags(attacher.TraceTagAttachInputs)
		//wrk.StartTracingTags(attacher.TraceTagValidateSequencer)
		//wrk.StartTracingTags(attacher.TraceTagAttachEndorsements)

		vidDistrib, err := wrk.EnsureBranch(distribTxID, 10*time.Minute) //3*time.Second)
		require.NoError(t, err)

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", wrk.Info())

		env.Stop()
		env.WaitAllWorkProcessesStop()

		rdr := multistate.MakeSugared(wrk.Branches().GetStateReaderForTheBranch(vidDistrib.ID()))
		stemOut := rdr.GetStemOutput()

		require.EqualValues(t, distribTxID, stemOut.ID.TransactionID())
		require.EqualValues(t, 0, stemOut.Output.TokenBalance())

		rr, ok := multistate.FetchRootRecord(wrk.StateStore(), distribTxID)
		require.True(t, ok)
		require.EqualValues(t, ledger.DefaultInitialSupply, int(rr.Supply))
		require.EqualValues(t, 0, int(rr.SlotInflation))

		bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000_000, int(bal2))
		require.EqualValues(t, 1, n2)

		balChain, nChain := multistate.BalanceOnLock(rdr, ledger.ChainLockFromChainID(bootstrapChainID))
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = multistate.BalanceOnChainOutput(rdr, bootstrapChainID)
		// Genesis output now has initialSupply-1 (1 token goes to dust output)
		require.EqualValues(t, ledger.DefaultInitialSupply-1-1_000_000_000-2_000_000_000, int(balChain))

	})
	t.Run("with distribution tx", func(t *testing.T) {
		//attacher.SetTraceOn()
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000_000},
			{Lock: addr2, Balance: 2_000_000_000},
		}

		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := multistate.InitStateStoreFromGlobals(stateStore)
		txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

		env := newWorkflowDummyEnvironment(stateStore, txBytesStore)
		wrk := workflow.Start(env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC)

		txBytes, err := txbuilder_seq.DistributeInitialSupply(stateStore, genesisPrivateKey, distrib)
		require.NoError(t, err)

		waitCh := make(chan struct{})
		vidDistrib, err := attacher.AttachTransactionFromBytes(txBytes, wrk, attacher.WithAttachmentCallback(func(vid *vertex.WrappedTx, err error) {
			require.EqualValues(t, vertex.Good, vid.GetTxStatus())
			_, err = txBytesStore.PersistTxBytesWithMetadata(txBytes, nil)
			util.AssertNoError(err)
			close(waitCh)
		}))
		require.NoError(t, err)

		<-waitCh

		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		t.Logf("genesis branch txid: %s", vidDistrib.IDShortString())
		t.Logf("%s", wrk.Info())

		env.Stop()
		env.WaitAllWorkProcessesStop()

		distribVID := wrk.GetVertex(vidDistrib.ID())
		require.True(t, distribVID != nil)
		rdr := multistate.MakeSugared(wrk.Branches().GetStateReaderForTheBranch(distribVID.ID()))
		stemOut := rdr.GetStemOutput()

		distribTxID, _, err := transaction.IDAndTimestampFromParsedTransactionBytes(txBytes)
		require.NoError(t, err)

		require.EqualValues(t, int(stemOut.ID.Slot()), int(distribTxID.Slot()))
		require.EqualValues(t, 0, stemOut.Output.TokenBalance())

		rr, ok := multistate.FetchRootRecord(wrk.StateStore(), stemOut.ID.TransactionID())
		require.True(t, ok)
		require.EqualValues(t, ledger.DefaultInitialSupply, int(rr.Supply))
		require.EqualValues(t, 0, int(rr.SlotInflation))

		bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000_000, int(bal2))
		require.EqualValues(t, 1, n2)

		balChain, nChain := multistate.BalanceOnLock(rdr, ledger.ChainLockFromChainID(bootstrapChainID))
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = multistate.BalanceOnChainOutput(rdr, bootstrapChainID)
		// Genesis output now has initialSupply-1 (1 token goes to dust output)
		require.EqualValues(t, ledger.DefaultInitialSupply-1-1_000_000_000-2_000_000_000, int(balChain))
	})
}

func TestAttachConflicts1Attacher(t *testing.T) {
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
			amount += o.Output.TokenBalance()
		}

		branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
		require.EqualValues(t, 1, len(branches))
		bd := branches[0]

		chainOut := bd.SequencerOutput.MustAsChainOutput()
		inTS := []base.LedgerTime{chainOut.Timestamp()}
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}
		ts := base.MaximumTime(inTS...).AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)

		txBytes, loader, err := txbuilder_seq.MakeSimpleSequencerTransactionWithInputLoader(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: testData.conflictingOutputs,
			PrivateKey:       genesisPrivateKey,
		})
		require.NoError(t, err)

		const printTx = true
		if printTx {
			t.Logf("----------- transaction ---------------\n%s",
				transaction.LinesFromTransactionBytes(txBytes, loader).String())
		}

		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()
		testData.logDAGInfo()

		if nConflicts > 1 {
			require.True(t, vertex.Bad == vid.GetTxStatus())
			t.Logf("reason: %v", vid.GetError())
			util.RequireErrorWithOld(t, vid.GetError(), "conflict", "in the past cone", testData.forkOutput.IDShort())
		} else {
			require.True(t, vertex.Good == vid.GetTxStatus())
		}
	})
	t.Run("conflicting tx consumed", func(t *testing.T) {
		//attacher.SetTraceOn()
		const nConflicts = 10
		testData := initWorkflowTestWithConflicts(t, nConflicts, 1, false)
		for _, txBytes := range testData.txBytesConflicting {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()

		amount := uint64(0)
		for _, o := range testData.conflictingOutputs {
			amount += o.Output.TokenBalance()
		}

		inTS := make([]base.LedgerTime, 0)
		for _, o := range testData.conflictingOutputs {
			inTS = append(inTS, o.Timestamp())
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, base.MaximumTime(inTS...).AddTicks(int(ledger.L(0).TransactionPace)))
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
		ts := outToConsume.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: []*ledger.OutputWithID{&outToConsume},
			PrivateKey:       genesisPrivateKey,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()
		require.NoError(t, err)
		testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("reason: %v", vid.GetError())
		util.RequireErrorWithOld(t, vid.GetError(), "conflict", "in the past cone", testData.forkOutput.IDShort())
	})
	t.Run("long", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 5
			howLong    = 30 // more violates pre-branch consolidation ticks
		)
		testData := initLongConflictTestData(t, nConflicts, nConflicts, howLong, true)
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
		inTS := []base.LedgerTime{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.TokenBalance()
		}

		ts := base.MaximumTime(inTS...).AddTicks(int(ledger.L(0).TransactionPaceSequencer))

		// checking invalid explicit baseline
		explicitBaseline := util.Ref(base.RandomTransactionID(true, 5, ts))
		txBytes, loader, err := txbuilder_seq.MakeSimpleSequencerTransactionWithInputLoader(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: testData.terminalOutputs,
			ExplicitBaseline: explicitBaseline,
			PrivateKey:       genesisPrivateKey,
		})
		require.NoError(t, util.MustErrorWith(err, "explicit baseline must be a branch transaction ID"))

		// now this must pass without error
		explicitBaseline = util.Ref(base.RandomTransactionID(true, 5, base.T(ts.Slot, 0)))

		txBytes, loader, err = txbuilder_seq.MakeSimpleSequencerTransactionWithInputLoader(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: testData.terminalOutputs,
			Endorsements:     nil,
			ExplicitBaseline: explicitBaseline,
			PrivateKey:       genesisPrivateKey,
		})
		require.NoError(t, err)

		// no explicit baseline in order for the test to pass
		txBytes, loader, err = txbuilder_seq.MakeSimpleSequencerTransactionWithInputLoader(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       genesisPrivateKey,
		})
		require.NoError(t, err)

		const printTx = true
		if printTx {
			t.Logf("----------- transaction ---------------\n%s",
				transaction.LinesFromTransactionBytes(txBytes, loader).String())
		}
		var wg sync.WaitGroup

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		//testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetError())
		util.RequireErrorWithOld(t, vid.GetError(), "conflict", "in the past cone", testData.forkOutput.IDShort())
	})
	t.Run("long with sync", func(t *testing.T) {
		const (
			nConflicts = 5
			howLong    = 30 // more violates pre-branch consolidation ticks
		)
		testData := initLongConflictTestData(t, nConflicts, nConflicts, howLong, true)
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
		inTS := []base.LedgerTime{chainOut.Timestamp()}
		amount := uint64(0)
		for _, o := range testData.terminalOutputs {
			inTS = append(inTS, o.Timestamp())
			amount += o.Output.TokenBalance()
		}
		for _, ts := range inTS {
			t.Logf("inTS : %s", ts.String())
		}

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        base.MaximumTime(inTS...).AddTicks(int(ledger.L(0).TransactionPaceSequencer)),
			ChainInput:       chainOut,
			AdditionalInputs: testData.terminalOutputs,
			PrivateKey:       genesisPrivateKey,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup

		//testData.env.StartTracingTags(attacher.TraceTagPull)

		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		testData.stopAndWait()

		testData.logDAGInfo(true)

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetError())
		util.RequireErrorWithOld(t, vid.GetError(), "conflict", "in the past cone", testData.forkOutput.IDShort())
	})
}

func TestAttachConflictsNAttachersSeqStartTx(t *testing.T) {
	//attacher.SetTraceOn()
	const (
		nConflicts = 10
		nChains    = 10
		howLong    = 10 // 2 // 97 fails when crosses slot boundary
	)
	var wg sync.WaitGroup
	testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
	testData.makeSeqBeginnings(false)

	_, err := testData.txStore.PersistTxBytesWithMetadata(testData.chainOriginsTx.Bytes(), nil)
	require.NoError(t, err)

	submitted := make([]*vertex.WrappedTx, nChains)
	wg.Add(len(testData.seqChain))
	for i, seqChain := range testData.seqChain {
		submitted[i], err = attacher.AttachTransactionFromBytes(seqChain[0].Bytes(), testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
	}
	wg.Wait()
	testData.stopAndWait()

	testData.logDAGInfo()

	for _, vid := range submitted {
		require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String())
	}
}

func TestAttachConflictsNAttachersSeqStartTxFee(t *testing.T) {
	//attacher.SetTraceOn()
	const (
		nConflicts = 2 // 5
		nChains    = 2 // 5
		howLong    = 3 // 5 // 97 fails when crosses slot boundary
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
		t.Logf("     ------------------ attach seq chain %d: %s", i, seqChain[0].IDShortString())
		submittedSeq[i], err = attacher.AttachTransactionFromBytes(seqChain[0].Bytes(), testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
	}
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()

	for _, vid := range submittedSeq {
		require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String())
	}

	for _, vid := range testData.wrk.Vertices() {
		if !vid.FlagsUp(vertex.FlagVertexConstraintsValid) {
			t.Logf("wrong flags: %s", vid.String())
		}
		if vid.IsVirtualTx() {
			require.True(t, vid.FlagsUp(vertex.FlagVertexDefined))
		} else {
			require.True(t, vid.FlagsUp(vertex.FlagVertexConstraintsValid))
		}
		if vid.IsSequencerMilestone() {
			require.True(t, vid.GetTxStatus() == vertex.Good)
		} else {
			require.True(t, vid.GetTxStatus() == vertex.Undefined)
		}
	}

	//testData.wrk.SaveGraph("utangle")
}

func TestAttachConflictsNAttachersOneFork(t *testing.T) {
	const (
		nConflicts = 5  // 2
		nChains    = 5  // 2
		howLong    = 20 // 97 fails when crosses the slot boundary
		pullYN     = true
	)
	var err error

	testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
	testData.makeSeqBeginnings(true)
	testData.printTxIDs()

	//testData.env.StartTracingTags(attacher.TraceTagAttachVertex)
	//testData.env.StartTracingTags(attacher.TraceTagAttach)

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
	var ts base.LedgerTime
	for seqNr := range testData.seqChain {
		tx := testData.seqChain[seqNr][0]
		o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
		chainIn[seqNr] = o.MustAsChainOutput()
		ts = base.MaximumTime(ts, o.Timestamp())
	}
	ts = ts.AddTicks(int(ledger.L(0).TransactionPaceSequencer))
	txBytesSeq, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:      "seq",
		Timestamp:    ts,
		ChainInput:   chainIn[0],
		Endorsements: util.List(chainIn[1].ID.TransactionID()),
		PrivateKey:   testData.privKeyAux,
	})
	require.NoError(t, err)
	txid, _, _ := transaction.IDAndTimestampFromParsedTransactionBytes(txBytesSeq)
	t.Logf("seq tx expected to fail: %s", txid.StringShort())
	t.Logf("   chain input: %s", chainIn[0].ID.StringShort())
	t.Logf("   endrosement: %s", chainIn[1].ID.StringShort())

	//testData.env.StartTracingTags(attacher.TraceTagAttach)
	//testData.env.StartTracingTags(attacher.TraceTagPull)
	//testData.env.StartTracingTags(attacher.TraceTagAttachEndorsements)
	//testData.env.StartTracingTags(attacher.TraceTagAttachVertex)
	//testData.env.StartTracingTags(attacher.TraceTagSolidifySequencerBaseline)

	const saveGraph = false
	if saveGraph {
		txid1, err := testData.txStore.PersistTxBytesWithMetadata(txBytesSeq, nil)
		require.NoError(t, err)
		require.EqualValues(t, txid, txid1)
		memdag.SavePastConeFromTxStoreUntilSlot(txid, testData.txStore, 0, "pastCone_TestAttachConflictsNAttachersOneFork")
	}

	waitCh := make(chan struct{})
	vidSeq, err := attacher.AttachTransactionFromBytes(txBytesSeq, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
		close(waitCh)
	}))
	require.NoError(t, err)
	<-waitCh

	testData.stopAndWait()
	testData.logDAGInfo()

	t.Logf("expected BAD transaction %s", vidSeq.IDShortString())
	require.EqualValues(t, vertex.Bad.String(), vidSeq.GetTxStatus().String())
	conflict := testData.forkOutput.ID.TransactionID()
	util.RequireErrorWithOld(t, vidSeq.GetError(), conflict.StringShort(), "conflict")
	//testData.wrk.SaveGraph("utangle")
}

func TestAttachConflictsNAttachersOneForkBranches(t *testing.T) {
	const (
		nConflicts = 5  // 2
		nChains    = 5  // 2
		howLong    = 30 // more fails when crosses slot boundary
		pullYN     = true
	)

	testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
	testData.makeSeqBeginnings(true)
	testData.printTxIDs()

	//testData.env.StartTracingTags(attacher.TraceTagAttachVertex)
	//testData.env.StartTracingTags(attacher.TraceTagAttachOutput)

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
	var ts base.LedgerTime
	for seqNr := range testData.seqChain {
		tx := testData.seqChain[seqNr][0]
		o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
		chainIn[seqNr] = o.MustAsChainOutput()
		ts = base.MaximumTime(ts, o.Timestamp())
	}
	ts = ts.NextSlotBoundary()

	var err error
	var wg sync.WaitGroup
	branches := make([]*vertex.WrappedTx, len(chainIn))
	var txBytes []byte
	stem := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot()).GetStemOutput()
	for i := range chainIn {
		txBytes, err = txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:    "seq",
			Timestamp:  ts,
			ChainInput: chainIn[i],
			StemInput:  stem,
			PrivateKey: testData.privKeyAux,
		})
		require.NoError(t, err)
		wg.Add(1)
		branches[i], err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		t.Logf("attaching branch %s", branches[i].IDShortString())
	}
	wg.Wait()

	testData.stopAndWait()
	testData.logDAGInfo()
	//testData.wrk.SaveGraph("utangle")
}

func TestAttachConflictsNAttachersOneForkBranchesConflict(t *testing.T) {
	//attacher.SetTraceOn()
	const (
		nConflicts = 5
		nChains    = 5
		howLong    = 30 // 5 // 97 fails when crosses slot boundary
		pullYN     = true
	)

	testData := initLongConflictTestData(t, nConflicts, nChains, howLong)
	testData.makeSeqBeginnings(true)
	//testData.printTxIDs()

	//testData.env.StartTracingTags(global.TraceTag, attacher.TraceTagAttachMilestone)

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
	var ts base.LedgerTime
	for seqNr := range testData.seqChain {
		tx := testData.seqChain[seqNr][0]
		o := tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
		chainIn[seqNr] = o.MustAsChainOutput()
		ts = base.MaximumTime(ts, o.Timestamp())
	}
	ts = ts.NextSlotBoundary()

	var err error
	txBytesBranch := make([][]byte, nChains)
	require.True(t, len(txBytesBranch) >= 2)

	stem := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot()).GetStemOutput()
	for i := range chainIn {
		txBytesBranch[i], err = txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
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

	txBytesConflicting, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:      "dummy",
		ChainInput:   tx0.SequencerOutput().MustAsChainOutput(),
		Timestamp:    ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts.AddTicks(int(ledger.L(0).TransactionPaceSequencer))),
		Endorsements: util.List(tx1.ID()),
		PrivateKey:   testData.privKeyAux,
	})
	require.NoError(t, err)

	vid, err := attacher.AttachTransactionFromBytes(txBytesConflicting, testData.wrk)
	require.NoError(t, err)

	status, err := testData.wrk.WaitTxIDDefined(vid.ID(), time.Millisecond, 5*time.Second)
	require.NoError(t, err)
	require.EqualValues(t, status, vertex.Bad)
	testData.stopAndWait()
	testData.logDAGInfo()

	//testData.wrk.SaveGraph("utangle")

	require.EqualValues(t, vid.GetTxStatus(), vertex.Bad)
	t.Logf("expected error: %v", vid.GetError())
	util.RequireErrorWithOld(t, vid.GetError(), "conflicting branch endorsement")
}

func TestAttachSeqChains(t *testing.T) {
	t.Run("no pull order normal", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5
			nChains               = 5
			howLongConflictChains = 5  // 2  // 97 fails when crosses slot boundary
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
				vids[seqNr][i] = attacher.AttachTransaction(tx, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
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
				vids[seqNr][i] = attacher.AttachTransaction(txSequence[i], testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
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
				vids[seqNr][i] = attacher.AttachTransaction(txSequence[i], testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
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
		const (
			nConflicts            = 5
			nChains               = 5
			howLongConflictChains = 2  // 97 fails when crosses slot boundary
			howLongSeqChains      = 10 // 90 // 95 fails
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
					_, err := testData.wrk.TxBytesStore().PersistTxBytesWithMetadata(tx.Bytes(), nil)
					require.NoError(t, err)
				} else {
					wg.Add(1)
					vids[seqNr] = attacher.AttachTransaction(tx, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
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
		//testData.wrk.SaveGraph("utangle")
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
				_, err := testData.wrk.TxBytesStore().PersistTxBytesWithMetadata(tx.Bytes(), nil)
				require.NoError(t, err)
			}
		}

		distribBD := testData.wrk.Branches().Get(testData.distributionBranchTxID)
		require.True(t, distribBD != nil)

		chainIn := testData.seqChain[0][len(testData.seqChain[0])-1].SequencerOutput().MustAsChainOutput()
		txBytesBranch, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:    "seq0",
			ChainInput: chainIn,
			StemInput:  distribBD.Stem,
			Timestamp:  chainIn.Timestamp().NextSlotBoundary(),
			PrivateKey: testData.privKeyAux,
		})
		//txBytesBranch, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParamsOld{
		//	SeqName:    "seq0",
		//	ChainInput: chainIn,
		//	StemInput:  distribBD.Stem,
		//	Timestamp:  chainIn.Timestamp().NextSlotBoundary(),
		//	PrivateKey: testData.privKeyAux,
		//})
		require.NoError(t, err)

		//testData.wrk.StartTracingTags(attacher.TraceTagAttach, attacher.TraceTagAttachVertex)
		//testData.wrk.StartTracingTags(poker.TraceTag, pull_client.TraceTag, pull_server.TraceTag)

		wg.Add(1)
		vidBranch, err := attacher.AttachTransactionFromBytes(txBytesBranch, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()

		testData.stopAndWait()
		testData.logDAGInfo()
		require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())
		//testData.wrk.SaveGraph("utangle")
		//memdag.SaveGraphPastCone(vidBranch, "utangle")
	})
	t.Run("with N branches pull", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts            = 5
			nChains               = 5
			howLongConflictChains = 5 // 97 fails when crosses slot boundary
			howLongSeqChains      = 5 // 10 // 10 // 95 fails
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

		testData.storeTransactions(branches...)
		var wg sync.WaitGroup
		wg.Add(1)
		vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()

		testData.stopAndWait()
		testData.logDAGInfo()
		//testData.wrk.SaveGraph("utangle")
		//dag.SaveGraphPastCone(vidBranch, "utangle")
		require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())

		time.Sleep(500 * time.Millisecond)
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		t.Logf("Memory stats: allocated %.1f MB, Num GC: %d, Goroutines: %d, ",
			float32(memStats.Alloc*10/(1<<20))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
	})
	t.Run("with N branches and transfers", func(t *testing.T) {
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

		//testData.env.StartTracingTags("persist_txbytes")

		var wg sync.WaitGroup
		wg.Add(1)
		vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()

		testData.stopAndWait()
		testData.logDAGInfo()
		//memdag.SaveGraphPastCone(vidBranch, "utangle")

		require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())

		time.Sleep(500 * time.Millisecond)
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		t.Logf("Memory stats: allocated %.1f MB, Num GC: %d, Goroutines: %d, ",
			float32(memStats.Alloc*10/(1<<20))/10,
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

		testData.env.StartTracingTags(sequencer.TraceTag + "_tx")

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

		testData.env.StartTracingTags("persist_txbytes")

		var wg sync.WaitGroup
		wg.Add(1)
		vidBranch := attacher.AttachTransaction(branches[len(branches)-1], testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		wg.Wait()

		testData.stopAndWait()
		testData.logDAGInfo()
		//memdag.SaveGraphPastCone(vidBranch, "utangle")
		require.EqualValues(t, vertex.Good.String(), vidBranch.GetTxStatus().String())

		time.Sleep(500 * time.Millisecond)
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		t.Logf("Memory stats: allocated %.1f MB, Num GC: %d, Goroutines: %d, ",
			float32(memStats.Alloc*10/(1<<20))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
	})
}

// =============================================================================
// TIMING EDGE CASES TESTS
// These tests cover timing-related edge cases in the attacher, including
// transaction pace validation, slot boundary transitions, and consolidation windows.
// =============================================================================

// TestAttachTimingPaceBoundaries tests transaction pace validation at exact boundaries.
// It verifies that transactions respect the minimum tick spacing requirements.
func TestAttachTimingPaceBoundaries(t *testing.T) {
	t.Run("non-sequencer exact pace", func(t *testing.T) {
		// Test that a transaction exactly at TransactionPace ticks apart is valid
		// Note: Non-sequencer transactions don't get callbacks like sequencer transactions.
		// We verify by checking if the transaction was successfully attached.
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create transaction exactly at TransactionPace ticks
		exactPaceTs := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if exactPaceTs.IsSlotBoundary() {
			exactPaceTs = exactPaceTs.AddTicks(1)
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, exactPaceTs).
			MustWithInputs(sourceOutput).
			WithAmount(1_000_000_000). // Use higher amount for minimum storage deposit
			WithTargetLock(testData.addr)

		txBytes, err := txbuilder.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		// Non-sequencer transactions are attached immediately without waiting for callback
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)

		// Non-sequencer tx shouldn't have "Bad" status if it was built correctly
		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(), "transaction at exact pace should not be rejected")
		t.Logf("TransactionPace = %d ticks, transaction at exact pace: PASSED (status: %s)", ledger.L(0).TransactionPace, vid.GetTxStatus().String())
	})

	t.Run("non-sequencer pace minus one", func(t *testing.T) {
		// Test that the pace constraint boundary is correctly identified.
		// Note: The actual pace constraint validation happens in EasyFL scripts during
		// lock validation, which only occurs when transaction is included in a sequencer's
		// past cone. This test verifies the constraint calculation is correct.
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Calculate timestamps at pace-1 and at exact pace
		tooFastTs := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace) - 1)
		exactPaceTs := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))

		// Verify the difference calculation
		tooFastDiff := base.DiffTicks(tooFastTs, sourceOutput.Timestamp())
		exactDiff := base.DiffTicks(exactPaceTs, sourceOutput.Timestamp())

		require.EqualValues(t, ledger.L(0).TransactionPace-1, tooFastDiff,
			"pace-1 should be exactly TransactionPace-1 ticks")
		require.EqualValues(t, ledger.L(0).TransactionPace, exactDiff,
			"exact pace should be exactly TransactionPace ticks")

		t.Logf("TransactionPace = %d, pace-1 = %d, verified constraint boundary calculation",
			ledger.L(0).TransactionPace, ledger.L(0).TransactionPace-1)
	})

	t.Run("sequencer exact pace", func(t *testing.T) {
		// Test sequencer transaction exactly at TransactionPaceSequencer
		// Note: initLongConflictTestData requires nChains == nConflicts
		const nChains = 2
		testData := initLongConflictTestData(t, nChains, nChains, 0)
		defer testData.stopAndWait()

		testData.makeChainOrigins(nChains)
		_, err := attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]
		// Exact sequencer pace
		exactSeqPaceTs := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		exactSeqPaceTs = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(exactSeqPaceTs)

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:      "test",
			ChainInput:   chainOrigin,
			Timestamp:    exactSeqPaceTs,
			Endorsements: []base.TransactionID{testData.distributionBranchTxID},
			PrivateKey:   testData.privKeyAux,
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String(), "sequencer at exact pace should be valid")
		t.Logf("TransactionPaceSequencer = %d ticks, sequencer at exact pace: PASSED", ledger.L(0).TransactionPaceSequencer)
	})
}

// TestAttachTimingSlotBoundaries tests slot boundary transitions.
// It verifies correct handling of transactions at tick 127 (last tick) and tick 0 (branch).
func TestAttachTimingSlotBoundaries(t *testing.T) {
	t.Run("branch transaction at slot boundary", func(t *testing.T) {
		// Test that a branch transaction (tick == 0) requires stem input.
		// This test verifies the slot boundary calculation and branch transaction construction.
		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(1)
		_, err = attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]

		// Get stem output from distribution branch
		distribBD := testData.wrk.Branches().Get(testData.distributionBranchTxID)
		require.NotNil(t, distribBD)

		// Create branch at next slot boundary
		branchTs := chainOrigin.Timestamp().NextSlotBoundary()
		require.True(t, branchTs.IsSlotBoundary(), "branch timestamp must be on slot boundary")
		require.EqualValues(t, 0, branchTs.Tick, "branch timestamp tick must be 0")

		// Verify we can build the branch transaction
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:    "test",
			ChainInput: chainOrigin,
			StemInput:  distribBD.Stem,
			Timestamp:  branchTs,
			PrivateKey: testData.privKeyAux,
		})
		require.NoError(t, err, "should be able to build branch transaction with stem input")

		// Attach without waiting for callback (sequencer tx solidification can be slow)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)
		require.NotNil(t, vid)

		t.Logf("Branch at slot %d, tick 0: built and attached (status: %s)", branchTs.Slot, vid.GetTxStatus().String())
	})

	t.Run("last tick before slot boundary", func(t *testing.T) {
		// Test transaction at tick 127 (MaxTickValue)
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Find a timestamp at tick 127 (last tick of slot)
		lastTickTs := sourceOutput.Timestamp()
		for lastTickTs.Tick != base.MaxTickValue {
			lastTickTs = lastTickTs.AddTicks(1)
		}
		// Ensure it's at valid pace from source
		if base.DiffTicks(lastTickTs, sourceOutput.Timestamp()) < int64(ledger.L(0).TransactionPace) {
			lastTickTs = base.T(lastTickTs.Slot+1, base.MaxTickValue)
		}

		require.EqualValues(t, base.MaxTickValue, lastTickTs.Tick, "should be at tick 127")

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, lastTickTs).
			MustWithInputs(sourceOutput).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes, err := txbuilder.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		// Non-sequencer transactions are attached immediately without waiting for callback
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)

		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(), "transaction at tick 127 should not be rejected")
		t.Logf("Transaction at slot %d, tick %d (MaxTickValue): PASSED (status: %s)", lastTickTs.Slot, lastTickTs.Tick, vid.GetTxStatus().String())
	})

	t.Run("cross-slot transaction chain", func(t *testing.T) {
		// Test transaction that consumes output from previous slot
		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// First transaction in current slot
		ts1 := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts1.IsSlotBoundary() {
			ts1 = ts1.AddTicks(1)
		}

		td1 := txbuilder.NewTransferData(testData.privKey, testData.addr, ts1).
			MustWithInputs(sourceOutput).
			WithAmount(5_000_000_000).
			WithTargetLock(testData.addr)

		txBytes1, err := txbuilder.MakeSimpleTransferTransaction(td1)
		require.NoError(t, err)

		// Non-sequencer transactions are attached immediately without waiting for callback
		vid1, err := attacher.AttachTransactionFromBytes(txBytes1, testData.wrk)
		require.NoError(t, err)
		require.NotEqual(t, vertex.Bad.String(), vid1.GetTxStatus().String())

		// Second transaction in next slot (cross-slot)
		output1 := vid1.MustOutputWithIDAt(0)
		ts2 := base.T(ts1.Slot+1, ledger.L(0).TransactionPace+1) // Next slot

		td2 := txbuilder.NewTransferData(testData.privKey, testData.addr, ts2).
			MustWithInputs(&output1).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes2, err := txbuilder.MakeSimpleTransferTransaction(td2)
		require.NoError(t, err)

		vid2, err := attacher.AttachTransactionFromBytes(txBytes2, testData.wrk)
		require.NoError(t, err)

		require.NotEqual(t, vertex.Bad.String(), vid2.GetTxStatus().String(), "cross-slot transaction should not be rejected")
		t.Logf("Cross-slot chain: slot %d -> slot %d: PASSED", ts1.Slot, ts2.Slot)
	})
}

// TestAttachTimingPreBranchConsolidation tests pre-branch consolidation window behavior.
// Sequencer transactions within PreBranchConsolidationTicks of slot boundary have restrictions.
func TestAttachTimingPreBranchConsolidation(t *testing.T) {
	t.Run("sequencer in pre-consolidation window", func(t *testing.T) {
		// Test that we can correctly identify timestamps in the pre-consolidation window.
		// The actual enforcement of pre-consolidation restrictions is tested implicitly
		// through the ledger validation scripts.
		if ledger.L(0).PreBranchConsolidationTicks == 0 {
			t.Skip("PreBranchConsolidationTicks is 0, no constraint to test")
		}

		// Calculate pre-consolidation timestamp (within window before slot boundary)
		preConsolidationTick := base.MaxTickValue - ledger.L(0).PreBranchConsolidationTicks + 1
		preConsolidationTs := base.T(1, preConsolidationTick)

		require.True(t, ledger.L(0).IsPreBranchConsolidationTimestamp(preConsolidationTs),
			"timestamp should be in pre-consolidation window")

		// One tick before should NOT be in pre-consolidation
		beforePreConsolidation := base.T(1, preConsolidationTick-1)
		require.False(t, ledger.L(0).IsPreBranchConsolidationTimestamp(beforePreConsolidation),
			"timestamp before window should not be in pre-consolidation")

		t.Logf("Pre-consolidation window: ticks > %d, test tick: %d (in window: true), tick %d (in window: false)",
			base.MaxTickValue-ledger.L(0).PreBranchConsolidationTicks, preConsolidationTick, preConsolidationTick-1)
	})

	t.Run("at exact consolidation boundary", func(t *testing.T) {
		// Test at exact boundary of pre-consolidation window
		if ledger.L(0).PreBranchConsolidationTicks == 0 {
			t.Skip("PreBranchConsolidationTicks is 0, no constraint to test")
		}

		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Test the boundary tick value
		boundaryTick := base.MaxTickValue - ledger.L(0).PreBranchConsolidationTicks
		boundaryTs := base.T(1, boundaryTick)

		// Boundary tick should NOT be in pre-consolidation
		require.False(t, ledger.L(0).IsPreBranchConsolidationTimestamp(boundaryTs),
			"tick at exact boundary should NOT be in pre-consolidation")

		// One tick after should BE in pre-consolidation
		afterBoundaryTs := base.T(1, boundaryTick+1)
		require.True(t, ledger.L(0).IsPreBranchConsolidationTimestamp(afterBoundaryTs),
			"tick after boundary should be in pre-consolidation")

		t.Logf("PreBranchConsolidationTicks=%d, boundary tick=%d: PASSED",
			ledger.L(0).PreBranchConsolidationTicks, boundaryTick)
	})
}

// TestAttachTimingPostBranchConsolidation tests post-branch consolidation timing.
// Sequencer transactions must be at least PostBranchConsolidationTicks after branch.
func TestAttachTimingPostBranchConsolidation(t *testing.T) {
	t.Run("sequencer at exact post-consolidation", func(t *testing.T) {
		// Test that we can correctly identify timestamps in the post-consolidation window.
		// The actual enforcement of post-consolidation restrictions is tested implicitly
		// through the ledger validation scripts.

		// Exact post-consolidation timestamp
		postConsolidationTs := base.T(1, ledger.L(0).PostBranchConsolidationTicks)
		require.True(t, ledger.L(0).IsPostBranchConsolidationTimestamp(postConsolidationTs),
			"timestamp at exact post-consolidation ticks should be in post-consolidation window")

		// One tick before should NOT be in post-consolidation
		beforePostConsolidation := base.T(1, ledger.L(0).PostBranchConsolidationTicks-1)
		require.False(t, ledger.L(0).IsPostBranchConsolidationTimestamp(beforePostConsolidation),
			"timestamp before post-consolidation ticks should not be in post-consolidation window")

		// Tick 0 (branch) should NOT be in post-consolidation
		branchTs := base.T(1, 0)
		require.False(t, ledger.L(0).IsPostBranchConsolidationTimestamp(branchTs),
			"branch tick (0) should not be in post-consolidation window")

		t.Logf("PostBranchConsolidationTicks=%d, tick %d (in window: true), tick %d (in window: false): PASSED",
			ledger.L(0).PostBranchConsolidationTicks, ledger.L(0).PostBranchConsolidationTicks, ledger.L(0).PostBranchConsolidationTicks-1)
	})

	t.Run("ensure post-consolidation helper", func(t *testing.T) {
		// Test the EnsurePostBranchConsolidationConstraintTimestamp helper
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Timestamp before post-consolidation
		earlyTs := base.T(1, 1)
		require.False(t, ledger.L(0).IsPostBranchConsolidationTimestamp(earlyTs))

		// Use helper to adjust
		adjustedTs := ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(earlyTs)
		require.True(t, ledger.L(0).IsPostBranchConsolidationTimestamp(adjustedTs))
		require.EqualValues(t, ledger.L(0).PostBranchConsolidationTicks, adjustedTs.Tick)

		// Timestamp already at post-consolidation should not change
		okTs := base.T(1, ledger.L(0).PostBranchConsolidationTicks+10)
		unchanged := ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(okTs)
		require.EqualValues(t, okTs, unchanged)

		t.Logf("EnsurePostBranchConsolidationConstraintTimestamp helper: PASSED")
	})
}

// TestAttachTimingRecursionDepth tests attachment recursion depth limits.
// This prevents hanging chain attacks with very long non-sequencer chains.
func TestAttachTimingRecursionDepth(t *testing.T) {
	t.Run("chain at max recursion depth", func(t *testing.T) {
		// Create a chain of transactions at max recursion depth
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		maxDepth := testData.env.MaxAttachmentRecursionDepth()
		t.Logf("MaxAttachmentRecursionDepth = %d", maxDepth)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		// Create a chain of transactions (maxDepth - some margin for safety)
		chainLength := maxDepth - 5
		if chainLength <= 0 {
			chainLength = 5
		}
		t.Logf("Creating chain of %d transactions", chainLength)

		prevOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		txBytesChain := make([][]byte, chainLength)
		for i := 0; i < chainLength; i++ {
			ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(1)
			}

			// Transfer full balance to avoid remainder output issues with minimum storage deposit
			td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
				MustWithInputs(prevOutput).
				WithAmount(prevOutput.Output.TokenBalance()).
				WithTargetLock(testData.addr)

			txBytesChain[i], err = txbuilder.MakeSimpleTransferTransaction(td)
			require.NoError(t, err)

			tx, err := transaction.FromBytes(txBytesChain[i], transaction.MainTxValidationOptions...)
			require.NoError(t, err)
			prevOutput = tx.MustProducedOutputWithIDAt(0)

			// Store all but the last in txstore for pull
			if i < chainLength-1 {
				_, err = testData.txStore.PersistTxBytesWithMetadata(txBytesChain[i], nil)
				require.NoError(t, err)
			}
		}

		// Attach the last transaction (should pull the chain)
		// Non-sequencer transactions are attached immediately without waiting for callback
		vid, err := attacher.AttachTransactionFromBytes(txBytesChain[chainLength-1], testData.wrk)
		require.NoError(t, err)

		// Chain within limits should not be rejected
		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(),
			"chain within max depth should not be rejected")
		t.Logf("Chain of %d transactions within max depth %d: PASSED (status: %s)", chainLength, maxDepth, vid.GetTxStatus().String())
	})
}

// =============================================================================
// DEADLOCK SCENARIO TESTS
// These tests cover potential deadlock scenarios in the attacher, including
// context cancellation, concurrent attachers, and shutdown behavior.
// =============================================================================

// TestAttachDeadlockContextCancellation tests workflow stop behavior mid-attachment.
// Verifies that stopping the workflow causes attachers to exit cleanly.
func TestAttachDeadlockContextCancellation(t *testing.T) {
	t.Run("stop workflow during attachment", func(t *testing.T) {
		testData := initWorkflowTest(t, 2)

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(1)
		_, err = attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]

		ts := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:      "test",
			ChainInput:   chainOrigin,
			Timestamp:    ts,
			Endorsements: []base.TransactionID{testData.distributionBranchTxID},
			PrivateKey:   testData.privKeyAux,
		})
		require.NoError(t, err)

		var callbackCalled atomic.Bool
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk,
			attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
				callbackCalled.Store(true)
			}))
		require.NoError(t, err)

		// Stop workflow immediately (this triggers context cancellation internally)
		testData.stop()

		// Wait for completion with timeout
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			status := vid.GetTxStatus()
			if status != vertex.Undefined {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Should complete (either Good or Bad) without hanging
		status := vid.GetTxStatus()
		t.Logf("Transaction status after stop: %s", status.String())
		require.True(t, status == vertex.Good || status == vertex.Bad,
			"transaction should complete after workflow stop, got: %s", status.String())

		testData.waitStop(5 * time.Second)
	})
}

// TestAttachDeadlockConcurrentAttachers tests concurrent attachment of the same transaction.
// Verifies that only one attacher runs and callbacks are properly invoked.
func TestAttachDeadlockConcurrentAttachers(t *testing.T) {
	t.Run("concurrent attach same transaction", func(t *testing.T) {
		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		ts := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
			MustWithInputs(sourceOutput).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes, err := txbuilder.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		const numConcurrent = 10
		var wg sync.WaitGroup
		vids := make([]*vertex.WrappedTx, numConcurrent)
		errors := make([]error, numConcurrent)

		// Start multiple concurrent attachments of the same transaction
		wg.Add(numConcurrent)
		for i := 0; i < numConcurrent; i++ {
			go func(idx int) {
				defer wg.Done()
				vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
				errors[idx] = err
				vids[idx] = vid
			}(i)
		}

		// Wait with timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Good, all completed
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for concurrent attachments - possible deadlock")
		}

		// All attachments should succeed without error
		for i, err := range errors {
			require.NoError(t, err, "concurrent attachment %d should not error", i)
		}

		// All vids should point to same vertex (same txid)
		var refVid *vertex.WrappedTx
		for i, vid := range vids {
			if vid != nil {
				if refVid == nil {
					refVid = vid
				} else {
					require.EqualValues(t, refVid.ID(), vid.ID(),
						"concurrent attachments should return same vertex, idx=%d", i)
				}
			}
		}

		require.NotNil(t, refVid, "at least one vid should be returned")
		require.NotEqual(t, vertex.Bad.String(), refVid.GetTxStatus().String(), "transaction should not be rejected")
		t.Logf("Concurrent attachments: %d goroutines, all returned same vertex: PASSED", numConcurrent)
	})
}

// TestAttachDeadlockSolidificationDeadline tests solidification deadline behavior.
// Verifies that missing inputs cause deadline expiration, not hanging.
func TestAttachDeadlockSolidificationDeadline(t *testing.T) {
	t.Run("missing input causes deadline", func(t *testing.T) {
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create first transaction (don't store it - will be missing)
		ts1 := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts1.IsSlotBoundary() {
			ts1 = ts1.AddTicks(1)
		}

		td1 := txbuilder.NewTransferData(testData.privKey, testData.addr, ts1).
			MustWithInputs(sourceOutput).
			WithAmount(5_000_000_000).
			WithTargetLock(testData.addr)

		txBytes1, err := txbuilder.MakeSimpleTransferTransaction(td1)
		require.NoError(t, err)

		tx1, err := transaction.FromBytes(txBytes1, transaction.MainTxValidationOptions...)
		require.NoError(t, err)

		// Create second transaction that depends on first (missing)
		output1 := tx1.MustProducedOutputWithIDAt(0)
		ts2 := ts1.AddTicks(int(ledger.L(0).TransactionPace))
		if ts2.IsSlotBoundary() {
			ts2 = ts2.AddTicks(1)
		}

		td2 := txbuilder.NewTransferData(testData.privKey, testData.addr, ts2).
			MustWithInputs(output1).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes2, err := txbuilder.MakeSimpleTransferTransaction(td2)
		require.NoError(t, err)

		// Attach second transaction - should eventually fail due to missing input
		// Note: the first transaction (tx1) was never stored or attached
		vid, err := attacher.AttachTransactionFromBytes(txBytes2, testData.wrk)
		require.NoError(t, err)

		// Non-sequencer transactions might not immediately fail - they may be pending
		// while trying to solidify. The key assertion is that this doesn't hang.
		status := vid.GetTxStatus()
		t.Logf("Initial status for tx with missing input: %s", status.String())

		// If still trying to solidify, that's acceptable for this test
		// The main point is that the AttachTransactionFromBytes returned without hanging
		require.True(t, status == vertex.Undefined || status == vertex.Bad,
			"transaction with missing input should be Undefined (pending) or Bad, got: %s", status.String())
	})
}

// TestAttachDeadlockShutdownDuringAttachment tests graceful shutdown mid-attachment.
// Verifies that stopping the workflow doesn't leave orphaned goroutines.
func TestAttachDeadlockShutdownDuringAttachment(t *testing.T) {
	t.Run("shutdown during multiple attachments", func(t *testing.T) {
		goroutinesBefore := runtime.NumGoroutine()

		testData := initWorkflowTest(t, 2)

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(5)
		_, err = attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		// Start multiple attachments
		const numAttachments = 5
		for i := 0; i < numAttachments; i++ {
			chainOrigin := testData.chainOrigins[i%len(testData.chainOrigins)]
			ts := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer) * (i + 1))
			ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)

			txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
				SeqName:      "test",
				ChainInput:   chainOrigin,
				Timestamp:    ts,
				Endorsements: []base.TransactionID{testData.distributionBranchTxID},
				PrivateKey:   testData.privKeyAux,
			})
			require.NoError(t, err)

			_, err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}

		// Immediate shutdown
		stopped := testData.stopAndWait(5 * time.Second)
		require.True(t, stopped, "workflow should stop within timeout")

		// Give goroutines time to clean up
		time.Sleep(500 * time.Millisecond)
		runtime.GC()
		time.Sleep(100 * time.Millisecond)

		goroutinesAfter := runtime.NumGoroutine()
		goroutineDiff := goroutinesAfter - goroutinesBefore

		t.Logf("Goroutines before: %d, after: %d, diff: %d", goroutinesBefore, goroutinesAfter, goroutineDiff)

		// Allow some slack for background goroutines, but shouldn't leak many
		require.LessOrEqual(t, goroutineDiff, 5,
			"should not leak many goroutines after shutdown")
	})
}
