package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/freezeburn"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// TestBurnRemovesAllRecords burns a plain output and a chain output of one key through the
// path the crossing branch takes (freezeburn.DeleteBurned into the mutations, the trie update
// with the burned amount) and checks that the state keeps no record of either: the outputs,
// their account index entries under the key, and the chain record are gone, the rest of the
// state is untouched, and the token invariant holds with the burned amount.
func TestBurnRemovesAllRecords(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	priv, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, 3000*base.PROX))
	// timestamp from an actual output, not the wall clock
	ts := getSourceOutputs(t, u, addr)[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	chainOut, err := u.CreateChainOrigin(priv, ts, 1000*base.PROX)
	require.NoError(t, err)
	chainID := chainOut.ChainID

	before := u.StateReader()
	held, err := before.GetUTXOIDsForController(addr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, held)
	burnSet := make(map[base.OutputID]uint64)
	var total uint64
	for _, oid := range held {
		data, ok := before.GetUTXO(oid)
		require.True(t, ok)
		o, err := ledger.OutputFromBytes(data)
		require.NoError(t, err)
		burnSet[oid] = o.TokenBalance()
		total += o.TokenBalance()
	}
	// the burn set holds the chain output and at least one plain output (the origin's remainder)
	_, hasChain := burnSet[chainOut.ID]
	require.True(t, hasChain)
	require.GreaterOrEqual(t, len(burnSet), 2)
	_, err = before.GetUTXOForChainID(chainID)
	require.NoError(t, err, "the chain record exists before the burn")
	supplyBefore := u.Supply()
	t.Cleanup(freezeburn.SetBurnedForTesting(burnSet))

	muts := multistate.NewMutations()
	burned, err := freezeburn.DeleteBurned(muts, before.GetUTXO)
	require.NoError(t, err)
	require.Equal(t, total, burned)

	// the invariant without the burned amount refuses the deletion, with it accepts it
	upd := multistate.MustNewUpdatable(u.Store(), u.Root())
	require.Error(t, upd.Update(muts, &multistate.RootRecordParams{}))
	upd = multistate.MustNewUpdatable(u.Store(), u.Root())
	require.NoError(t, upd.Update(muts, &multistate.RootRecordParams{Burned: burned}))

	after := multistate.MustNewReadable(u.Store(), upd.Root())
	for oid := range burnSet {
		require.False(t, after.HasUTXO(oid), "output %s still in the state", oid.StringShort())
	}
	left, err := after.GetUTXOIDsForController(addr.ControllerID())
	require.NoError(t, err)
	require.Empty(t, left, "account index entries left under the key")
	_, err = after.GetUTXOForChainID(chainID)
	require.Error(t, err, "the chain record is left after the burn")

	// nothing else moved: the faucet's outputs are where they were
	faucetBefore, err := before.GetUTXOIDsForController(u.FaucetAddress().ControllerID())
	require.NoError(t, err)
	faucetAfter, err := after.GetUTXOIDsForController(u.FaucetAddress().ControllerID())
	require.NoError(t, err)
	require.ElementsMatch(t, faucetBefore, faucetAfter)
	require.NotZero(t, supplyBefore)
}

// TestBurnDelegationRemovesAllRecords burns a delegation, as upgrade 8 burns the one whose
// master key is frozen, and checks that its output, its chain record and both of its account
// index entries are gone: the one under the master and the one under the target, which the
// target's delegation pool reads. The target's own sequencer chain is untouched.
func TestBurnDelegationRemovesAllRecords(t *testing.T) {
	env := setupDelegEnv(t, 0, 1000)
	deleg := env.delegatedOutput
	before := env.u.StateReader()
	delegatedBefore, err := env.u.SugaredStateReader().GetOutputsDelegatedToAccount2(env.target[:])
	require.NoError(t, err)
	require.Len(t, delegatedBefore, 1)
	masterBefore, err := before.GetUTXOIDsForController(env.masterAddr.ControllerID())
	require.NoError(t, err)
	require.Contains(t, masterBefore, deleg.ID)
	t.Cleanup(freezeburn.SetBurnedForTesting(map[base.OutputID]uint64{deleg.ID: deleg.Output.TokenBalance()}))

	muts := multistate.NewMutations()
	burned, err := freezeburn.DeleteBurned(muts, before.GetUTXO)
	require.NoError(t, err)
	require.Equal(t, deleg.Output.TokenBalance(), burned)
	upd := multistate.MustNewUpdatable(env.u.Store(), env.u.Root())
	require.NoError(t, upd.Update(muts, &multistate.RootRecordParams{Burned: burned}))

	after := multistate.MustNewReadable(env.u.Store(), upd.Root())
	require.False(t, after.HasUTXO(deleg.ID))
	_, err = after.GetUTXOForChainID(deleg.ChainID)
	require.Error(t, err, "the delegation's chain record is left after the burn")
	delegatedAfter, err := multistate.MakeSugared(after).GetOutputsDelegatedToAccount2(env.target[:])
	require.NoError(t, err)
	require.Empty(t, delegatedAfter, "the target's index entry is left after the burn")
	masterAfter, err := after.GetUTXOIDsForController(env.masterAddr.ControllerID())
	require.NoError(t, err)
	require.NotContains(t, masterAfter, deleg.ID, "the master's index entry is left after the burn")
	require.Len(t, masterAfter, len(masterBefore)-1, "the master's other outputs stay")
	_, err = after.GetUTXOForChainID(env.target)
	require.NoError(t, err, "the target's sequencer chain stays")
}
