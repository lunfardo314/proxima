package server

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// stateEnv serves one state as the LRB state; any other environment call
// panics on the embedded nil interface.
type stateEnv struct {
	environment
	rdr multistate.SugaredStateReader
}

func (e *stateEnv) LatestReliableState() (multistate.SugaredStateReader, error) { return e.rdr, nil }

// TestConsumedInLRB: submit_tx runs full-context validation only on consumed
// outputs the node holds with the bytes the caller supplied. Made-up outputs
// would let anyone have the node evaluate arbitrary scripts at no cost.
func TestConsumedInLRB(t *testing.T) {
	u := utxodb.NewUTXODB(ledger.InitWithTestingLedgerData())
	priv, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, 100_000_000))
	_, _, dst := u.GenerateAddress(2)
	par, err := u.MakeTransferInputData(priv, addr, base.NilLedgerTime)
	require.NoError(t, err)
	txBytes, err := utxodb.MakeTransferTransaction(par.WithAmount(20_000_000).WithTargetLock(dst))
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)

	consumed := make([][]byte, tx.NumInputs())
	for i := range consumed {
		data, found := u.StateReader().GetUTXO(tx.MustInputAt(byte(i)))
		require.True(t, found)
		consumed[i] = data
	}

	// the node holds the outputs with these bytes: full validation runs
	srv := &server{environment: &stateEnv{rdr: u.SugaredStateReader()}}
	inState, err := srv.consumedInLRB(tx, consumed)
	require.NoError(t, err)
	require.True(t, inState)

	// caller's bytes differ from the node's output: refused
	forged := make([][]byte, len(consumed))
	copy(forged, consumed)
	forged[0] = append(append([]byte{}, consumed[0]...), 0x00)
	_, err = srv.consumedInLRB(tx, forged)
	require.ErrorContains(t, err, "differs")

	// outputs not in the node's state (another ledger here): stage skipped
	other := utxodb.NewUTXODB(ledger.InitWithTestingLedgerData())
	srv = &server{environment: &stateEnv{rdr: other.SugaredStateReader()}}
	inState, err = srv.consumedInLRB(tx, consumed)
	require.NoError(t, err)
	require.False(t, inState)
}
