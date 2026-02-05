package tests

import (
	"crypto/ed25519"
	"runtime"
	"sync"
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
		addr1 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(2))
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
		addr1 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(2))
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
		addr1 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(2))
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
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
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
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
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
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
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
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)

		// no explicit baseline in order for the test to pass
		txBytes, loader, err = txbuilder_seq.MakeSimpleSequencerTransactionWithInputLoader(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: testData.terminalOutputs,
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
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
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
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
		if vid.IsSequencerTransaction() {
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
		SeqName:       "seq",
		Timestamp:     ts,
		ChainInput:    chainIn[0],
		Endorsements:  util.List(chainIn[1].ID.TransactionID()),
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    testData.privKeyAux,
		PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
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
			SeqName:       "seq",
			Timestamp:     ts,
			ChainInput:    chainIn[i],
			StemInput:     stem,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    testData.privKeyAux,
			PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
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
			SeqName:       "seq",
			StemInput:     stem,
			ChainInput:    chainIn[i],
			Timestamp:     ts,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    testData.privKeyAux,
			PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)

		_, err = testData.txStore.PersistTxBytesWithMetadata(txBytesBranch[i], nil)
		require.NoError(t, err)

		tx, err := transaction.ParseWithPartialValidation(txBytesBranch[i])
		require.NoError(t, err)
		t.Logf("branch #%d : %s", i, tx.IDShortString())
	}

	tx0, err := transaction.ParseWithPartialValidation(txBytesBranch[0])
	require.NoError(t, err)
	t.Logf("will be extending %s", tx0.IDShortString())

	tx1, err := transaction.ParseWithPartialValidation(txBytesBranch[1])
	require.NoError(t, err)
	t.Logf("will be endorsing %s", tx1.IDShortString())

	txBytesConflicting, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:       "dummy",
		ChainInput:    tx0.SequencerOutput().MustAsChainOutput(),
		Timestamp:     ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts.AddTicks(int(ledger.L(0).TransactionPaceSequencer))),
		Endorsements:  util.List(tx1.ID()),
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    testData.privKeyAux,
		PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
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
			SeqName:       "seq0",
			ChainInput:    chainIn,
			StemInput:     distribBD.Stem,
			Timestamp:     chainIn.Timestamp().NextSlotBoundary(),
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    testData.privKeyAux,
			PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
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
