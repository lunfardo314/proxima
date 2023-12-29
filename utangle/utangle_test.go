package utangle

import (
	"crypto/ed25519"
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle/attacher"
	"github.com/lunfardo314/proxima/utangle/dag"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
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
		attacher.SetTraceOn()
		const nConflicts = 10
		testData := initConflictTest(t, nConflicts, 0, false)
		for _, txBytes := range testData.txBytes {
			_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}
		testData.logDAGInfo()
	})
	t.Run("n double spends consumed", func(t *testing.T) {
		attacher.SetTraceOn()
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
			nConflicts = 5
			howLong    = 96 // 97 fails when crosses slot boundary
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

		//testData.logDAGInfo()

		require.True(t, vertex.Bad == vid.GetTxStatus())
		t.Logf("expected reason: %v", vid.GetReason())
		util.RequireErrorWith(t, vid.GetReason(), "conflicts with existing consumers in the baseline state", testData.forkOutput.IDShort())
	})
}

func TestConflictsNAttachers(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		//attacher.SetTraceOn()
		const (
			nConflicts = 2
			howLong    = 10 // 97 fails when crosses slot boundary
			nChains    = 2
		)
		initLongConflictTestData(t, nConflicts, nChains, howLong)
	})
}

type conflictTestData struct {
	t                  *testing.T
	wrk                *testingWorkflow
	txStore            global.TxBytesStore
	bootstrapChainID   core.ChainID
	privKey            ed25519.PrivateKey
	addr               core.AddressED25519
	privKeyAux         ed25519.PrivateKey
	addrAux            core.AddressED25519
	stateIdentity      genesis.LedgerIdentityData
	originBranchTxid   core.TransactionID
	forkOutput         *core.OutputWithID
	auxOutput          *core.OutputWithID
	txBytes            [][]byte
	conflictingOutputs []*core.OutputWithID
	chainOrigins       []*core.OutputWithChainID
	pkController       []ed25519.PrivateKey
	chainOriginsTx     *transaction.Transaction
}

func initConflictTest(t *testing.T, nConflicts int, nChains int, targetLockChain bool) *conflictTestData {

	const initBalance = 100_000
	genesisPrivKey := testutil.GetTestingPrivateKey()
	par := genesis.DefaultIdentityData(genesisPrivKey)
	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistribution(2, initBalance)
	if nChains > 0 {
		distrib[1] = core.LockBalance{
			Lock:    addrs[1],
			Balance: uint64(initBalance * nChains),
		}
	}
	ret := &conflictTestData{
		t:             t,
		stateIdentity: *par,
		privKey:       privKeys[0],
		addr:          addrs[0],
		privKeyAux:    privKeys[1],
		addrAux:       addrs[1],
	}
	require.True(t, core.AddressED25519MatchesPrivateKey(ret.addr, ret.privKey))

	ret.pkController = make([]ed25519.PrivateKey, nConflicts)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	stateStore := common.NewInMemoryKVStore()
	ret.txStore = txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

	ret.bootstrapChainID, _ = genesis.InitLedgerState(ret.stateIdentity, stateStore)
	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)
	err = ret.txStore.SaveTxBytes(txBytes)
	require.NoError(t, err)

	ret.wrk = newTestingWorkflow(ret.txStore, dag.New(stateStore))

	t.Logf("bootstrap chain id: %s", ret.bootstrapChainID.String())
	t.Logf("origing branch txid: %s", ret.originBranchTxid.StringShort())

	for i := range distrib {
		t.Logf("distributed %s -> %s", util.GoThousands(distrib[i].Balance), distrib[i].Lock.String())
	}
	t.Logf("%s", ret.wrk.Info())

	err = attacher.EnsureLatestBranches(ret.wrk)
	require.NoError(t, err)

	rdr := ret.wrk.HeaviestStateForLatestTimeSlot()
	bal, _ := multistate.BalanceOnLock(rdr, ret.addr)
	require.EqualValues(t, initBalance, int(bal))

	oDatas, err := rdr.GetUTXOsLockedInAccount(ret.addr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.forkOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.Amount()))
	t.Logf("forked output ID: %s", ret.forkOutput.IDShort())

	oDatas, err = rdr.GetUTXOsLockedInAccount(ret.addrAux.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.auxOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.Amount()))
	t.Logf("auxiliary output ID: %s", ret.forkOutput.IDShort())

	ret.txBytes = make([][]byte, nConflicts)

	td := txbuilder.NewTransferData(ret.privKey, ret.addr, core.LogicalTimeNow()).
		MustWithInputs(ret.forkOutput)

	for i := 0; i < nConflicts; i++ {
		td.WithAmount(uint64(100 + i))
		if targetLockChain {
			td.WithTargetLock(core.ChainLockFromChainID(ret.bootstrapChainID))
		} else {
			td.WithTargetLock(ret.addr)
		}
		ret.txBytes[i], err = txbuilder.MakeTransferTransaction(td)
		require.NoError(t, err)
	}
	require.EqualValues(t, nConflicts, len(ret.txBytes))

	ret.conflictingOutputs = make([]*core.OutputWithID, nConflicts)
	for i := range ret.conflictingOutputs {
		tx, err := transaction.FromBytesMainChecksWithOpt(ret.txBytes[i])
		require.NoError(t, err)
		ret.conflictingOutputs[i] = tx.MustProducedOutputWithIDAt(1)
		require.EqualValues(t, 100+i, int(ret.conflictingOutputs[i].Output.Amount()))
	}

	ret.makeChainOrigins(nChains)
	return ret
}

// makes chain origins transaction from auh output
func (td *conflictTestData) makeChainOrigins(n int) {
	if n == 0 {
		return
	}
	txb := txbuilder.NewTransactionBuilder()
	_, _ = txb.ConsumeOutputWithID(td.auxOutput)
	txb.PutSignatureUnlock(0)
	amount := td.auxOutput.Output.Amount() / uint64(n)
	for i := 0; i < n; i++ {
		o := core.NewOutput(func(o *core.Output) {
			o.WithAmount(amount)
			o.WithLock(td.addrAux)
			_, _ = o.PushConstraint(core.NewChainOrigin().Bytes())
		})
		_, _ = txb.ProduceOutput(o)
	}
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.TransactionData.Timestamp = td.auxOutput.Timestamp().AddTicks(core.TransactionPaceInTicks)
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(td.privKeyAux)

	var err error
	txBytes := txb.TransactionData.Bytes()
	td.chainOriginsTx, err = transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	require.NoError(td.t, err)
	td.chainOrigins = make([]*core.OutputWithChainID, n)
	td.chainOriginsTx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		td.chainOrigins[idx] = &core.OutputWithChainID{
			OutputWithID: core.OutputWithID{
				ID:     *oid,
				Output: o,
			},
			ChainID: blake2b.Sum256(oid[:]),
		}
		td.t.Logf("chain origin %s : %s", oid.StringShort(), td.chainOrigins[idx].String())
		return true
	})
}

func (td *conflictTestData) logDAGInfo() {
	td.t.Logf("DAG INFO:\n%s", td.wrk.Info())
	slot := td.wrk.LatestBranchSlot()
	td.t.Logf("VERTICES in the latest slot %d\n%s", slot, td.wrk.LinesVerticesInSlotAndAfter(slot).String())
}

type longConflictTestData struct {
	conflictTestData
	txSequences     [][][]byte
	terminalOutputs []*core.OutputWithID
}

func initLongConflictTestData(t *testing.T, nConflicts int, nChains int, howLong int) *longConflictTestData {
	ret := &longConflictTestData{
		conflictTestData: *initConflictTest(t, nConflicts, nChains, false),
		txSequences:      make([][][]byte, nConflicts),
		terminalOutputs:  make([]*core.OutputWithID, nConflicts),
	}

	var prev *core.OutputWithID
	var err error

	td := &ret.conflictTestData

	for seqNr, originOut := range ret.conflictingOutputs {
		ret.txSequences[seqNr] = make([][]byte, howLong)
		for i := 0; i < howLong; i++ {
			if i == 0 {
				prev = originOut
			}
			prev = originOut
			trd := txbuilder.NewTransferData(td.privKey, td.addr, originOut.Timestamp().AddTicks(core.TransactionPaceInTicks*(i+1)))
			trd.WithAmount(originOut.Output.Amount())
			trd.MustWithInputs(prev)
			if i < howLong-1 {
				trd.WithTargetLock(td.addr)
			} else {
				trd.WithTargetLock(core.ChainLockFromChainID(td.bootstrapChainID))
			}
			ret.txSequences[seqNr][i], err = txbuilder.MakeSimpleTransferTransaction(trd)
			require.NoError(t, err)

			tx, err := transaction.FromBytesMainChecksWithOpt(ret.txSequences[seqNr][i])
			require.NoError(t, err)

			prev = tx.MustProducedOutputWithIDAt(0)
			if i == howLong-1 {
				ret.terminalOutputs[seqNr] = prev
			}
		}
	}
	return ret
}
