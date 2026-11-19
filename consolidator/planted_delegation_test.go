package consolidator

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// Anyone may create a delegation naming any master, carrying any constraint between the chain
// constraint and the state, which every successor must keep. One planted under the wallet with a
// "time bomb" (consumable only before a slot) would take the consolidator's swept funds and lock
// them for good once the slot passes. The wallet's parse marks such a delegation as not of the
// standard layout, and listOwnDelegations leaves it out; a plain delegation planted the same way
// is of the standard layout and harmless: its tokens belong to the master.
func TestPlantedDelegationNotStandardLayout(t *testing.T) {
	ledger.ResetForTesting()
	// from library upgrade 7 a delegation with extra elements cannot be produced: the planted ones
	// are those made before, so the ledger here folds no upgrade
	genesisKey := ledger.InitWithTestingLedgerData(ledger.WithoutFoldedUpgrades())
	u := utxodb.NewUTXODB(genesisKey, true)

	jsonBytes := easyfl.ToJSON(ledger.L(base.MaxSlot).Library, true, false)
	desc, err := easyfl.ReadLibraryFromJSON(jsonBytes)
	require.NoError(t, err)
	tlib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)

	privs, _, addrs := u.GenerateAddresses(0, 2)
	victim, attackerPriv := addrs[0], privs[1]
	require.NoError(t, u.TokensFromFaucet(addrs[1], 1_000*prox))
	ts := ledger.TimeNow()
	if data, err := u.StateReader().GetUTXOsForController(addrs[1].ControllerID()); err == nil {
		for _, od := range data {
			ts = base.MaximumTime(ts, od.ID.Timestamp())
		}
	}
	ts = ts.AddSlots(1)

	bomb, err := tlib.CompileExpression(fmt.Sprintf("or(selfIsProducedOutput, lessThan(uint8Bytes(txSlot), u64/%d))", ts.Slot+100))
	require.NoError(t, err)
	var anyTarget base.ChainID
	anyTarget[0] = 1
	plant := func(ts base.LedgerTime, extra []byte) {
		par, err := u.MakeTransferInputData(attackerPriv, nil, ts)
		require.NoError(t, err)
		par = par.WithAmount(50 * prox).
			WithTargetLock(ledger.NewDelegateLock(anyTarget, base.HolderID(victim), 0)).
			WithConstraint(ledger.NewChainOrigin(ts.Slot))
		if extra != nil {
			par = par.WithConstraint(ledger.NewGeneralScript(extra))
		}
		txBytes, err := utxodb.MakeSimpleTransferTransaction(par.WithConstraint(ledger.DelegateLockState{}))
		require.NoError(t, err)
		require.NoError(t, u.AddTransaction(txBytes), "anyone may create a delegation under any master")
	}
	plant(ts, bomb)
	plant(ts.AddSlots(1), nil)

	data, err := u.StateReader().GetUTXOsForController(victim.ControllerID())
	require.NoError(t, err)
	standard := map[bool]int{}
	for _, od := range data {
		ow, err := od.Parse()
		require.NoError(t, err)
		if ow.Output.Lock().Name() != ledger.DelegateLockName {
			continue
		}
		o, err := txbuildercore.OutputFromBytes(ow.Output.Bytes())
		require.NoError(t, err)
		v, ok, err := tlib.ParseDelegationOutput(o, ow.ID)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, base.HolderID(victim), v.MasterID)
		standard[v.StandardLayout]++
	}
	require.Equal(t, map[bool]int{false: 1, true: 1}, standard, "the bombed delegation is not of the standard layout, the plain one is")
}
