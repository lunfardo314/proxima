package conewalk

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// mapStore is the smallest source of transaction bytes: a map from txid to bytes.
type mapStore map[base.TransactionID][]byte

func (m mapStore) GetTxBytes(id *base.TransactionID) []byte { return m[*id] }
func (m mapStore) HasTxBytes(id *base.TransactionID) bool   { _, ok := m[*id]; return ok }

// chain builds three real transactions, each consuming an output of the one before: a faucet
// transfer to A, A to B, B to C. The faucet transfer consumes outputs of transactions that are not
// put in the store, so the walk must report them missing.
func chain(t *testing.T) (store mapStore, txs []*transaction.Transaction) {
	ledger.ResetForTesting()
	u := utxodb.NewUTXODB(ledger.InitWithTestingLedgerData())
	privA, _, addrA := u.GenerateAddress(1)
	privB, _, addrB := u.GenerateAddress(2)
	_, _, addrC := u.GenerateAddress(3)

	faucetBytes, err := u.MakeTransactionFromFaucet(addrA, 1_000_000_000)
	require.NoError(t, err)
	require.NoError(t, u.AddTransaction(faucetBytes))
	faucetTx, err := transaction.Parse(faucetBytes)
	require.NoError(t, err)
	txAB, err := u.TransferTokensReturnTx(privA, addrB, 500_000_000)
	require.NoError(t, err)
	txBC, err := u.TransferTokensReturnTx(privB, addrC, 200_000_000)
	require.NoError(t, err)

	store = mapStore{}
	txs = []*transaction.Transaction{faucetTx, txAB, txBC}
	for _, tx := range txs {
		store[tx.ID()] = tx.Bytes()
	}
	return store, txs
}

// The walk from the newest transaction reaches the whole chain, newest slot first, and reports
// every dependency the store does not hold as missing, each referenced from the faucet transfer.
func TestWalkWholeCone(t *testing.T) {
	store, txs := chain(t)
	w := New(store, 0, transaction.ParseLibraryAgnostic)
	var visited []base.TransactionID
	missing := map[base.TransactionID]base.TransactionID{}
	w.Visit = func(tx *transaction.Transaction) { visited = append(visited, tx.ID()) }
	w.Missing = func(dep, referrer base.TransactionID) { missing[dep] = referrer }

	require.True(t, w.Add(txs[2].ID()))
	w.Run()

	require.ElementsMatch(t, []base.TransactionID{txs[0].ID(), txs[1].ID(), txs[2].ID()}, visited)
	for i := 1; i < len(visited); i++ {
		require.LessOrEqual(t, visited[i].Slot(), visited[i-1].Slot(), "visited out of slot order")
	}
	require.NotEmpty(t, missing)
	for dep, referrer := range missing {
		require.False(t, store.HasTxBytes(&dep))
		require.Equal(t, txs[0].ID(), referrer)
	}
	require.Zero(t, w.ParseErrors)
}

// A dependency the caller skips is neither loaded nor reported, and the walk does not go past it:
// this is how the dagstore stops a branch's walk at what it already holds.
func TestWalkSkip(t *testing.T) {
	store, txs := chain(t)
	w := New(store, 0, transaction.ParseLibraryAgnostic)
	var visited []base.TransactionID
	w.Visit = func(tx *transaction.Transaction) { visited = append(visited, tx.ID()) }
	w.Missing = func(dep, _ base.TransactionID) {
		t.Fatalf("reported missing past a skipped dependency: %s", dep.StringShort())
	}
	w.Skip = func(dep, _ base.TransactionID) bool { return dep == txs[1].ID() }

	require.True(t, w.Add(txs[2].ID()))
	w.Run()
	require.Equal(t, []base.TransactionID{txs[2].ID()}, visited)
}

// Dependencies older than the floor are reported to BelowFloor and not followed.
func TestWalkFloor(t *testing.T) {
	store, txs := chain(t)
	floor := txs[2].Slot() + 1 // above every dependency of the root
	w := New(store, floor, transaction.ParseLibraryAgnostic)
	var visited, below []base.TransactionID
	w.Visit = func(tx *transaction.Transaction) { visited = append(visited, tx.ID()) }
	w.BelowFloor = func(dep, _ base.TransactionID) { below = append(below, dep) }

	require.True(t, w.Add(txs[2].ID()))
	w.Run()
	// the root itself is below the floor too, so nothing is visited and nothing followed
	require.Empty(t, visited)
	require.Empty(t, below)

	w = New(store, txs[2].Slot(), transaction.ParseLibraryAgnostic)
	w.Visit = func(tx *transaction.Transaction) { visited = append(visited, tx.ID()) }
	w.BelowFloor = func(dep, _ base.TransactionID) { below = append(below, dep) }
	require.True(t, w.Add(txs[2].ID()))
	w.Run()
	require.Contains(t, visited, txs[2].ID())
	for _, dep := range below {
		require.Less(t, dep.Slot(), txs[2].Slot())
	}
}
