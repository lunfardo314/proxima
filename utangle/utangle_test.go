package utangle

import (
	"sync"
	"testing"

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

func TestOriginTangle(t *testing.T) {
	t.Run("genesis", func(t *testing.T) {
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
	t.Run("genesis with distribution", func(t *testing.T) {
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
	t.Run("genesis sync scenario", func(t *testing.T) {
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

	t.Run("genesis with distribution tx", func(t *testing.T) {
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
		vidDistrib, err := attacher.AttachTransactionFromBytes(txBytes, wrk, func(vid *vertex.WrappedTx) {
			status := vid.GetTxStatus()
			if status == vertex.Good {
				err := txBytesStore.SaveTxBytes(txBytes)
				util.AssertNoError(err)
			}
			t.Logf("distribution tx finalized. Status: %s", vid.StatusString())
			wg.Done()
		})
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

	//t.Run("origin with distribution", func(t *testing.T) {
	//	privKey := testutil.GetTestingPrivateKey()
	//	par := genesis.DefaultIdentityData(privKey)
	//	addr1 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
	//	addr2 := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
	//	distrib := []core.LockBalance{
	//		{Lock: addr1, Balance: 1_000_000},
	//		{Lock: addr2, Balance: 2_000_000},
	//	}
	//
	//	stateStore := common.NewInMemoryKVStore()
	//	bootstrapChainID, root := genesis.InitLedgerState(*par, stateStore)
	//	dagAccess := dag.New(stateStore)
	//	txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	//	wrk := newTestingWorkflow(txBytesStore, dagAccess)
	//
	//	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
	//	require.NoError(t, err)
	//	err = txBytesStore.SaveTxBytes(txBytes)
	//	require.NoError(t, err)
	//
	//	t.Logf("bootstrap chain id: %s", bootstrapChainID.String())
	//
	//	distribTxID, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
	//	require.NoError(t, err)
	//
	//	t.Logf("genesis branch txid: %s", distribTxID.StringShort())
	//	t.Logf("%s", dagAccess.Info())
	//
	//	distribVID := dagAccess.GetVertex(&distribTxID)
	//	require.True(t, distribVID != nil)
	//
	//	stemOut := ut.HeaviestStemOutput()
	//	require.EqualValues(t, int(stemOut.ID.TimeSlot()), int(distribTxID.TimeSlot()))
	//	require.EqualValues(t, 0, stemOut.Output.Amount())
	//	stemLock, ok := stemOut.Output.StemLock()
	//	require.True(t, ok)
	//	require.EqualValues(t, genesis.DefaultSupply, int(stemLock.Supply))
	//
	//	rdr := ut.HeaviestStateForLatestTimeSlot()
	//	bal1, n1 := multistate.BalanceOnLock(rdr, addr1)
	//	require.EqualValues(t, 1_000_000, int(bal1))
	//	require.EqualValues(t, 1, n1)
	//
	//	bal2, n2 := multistate.BalanceOnLock(rdr, addr2)
	//	require.EqualValues(t, 2_000_000, int(bal2))
	//	require.EqualValues(t, 1, n2)
	//
	//	balChain, nChain := multistate.BalanceOnLock(rdr, bootstrapChainID.AsChainLock())
	//	require.EqualValues(t, 0, balChain)
	//	require.EqualValues(t, 0, nChain)
	//
	//	balChain = multistate.BalanceOnChainOutput(rdr, &bootstrapChainID)
	//	require.EqualValues(t, genesis.DefaultSupply-1_000_000-2_000_000, int(balChain))
	//})
}
