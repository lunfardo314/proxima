package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// Library upgrade 1 end to end (kb/library_upgrade1.md): the upgrade is listed
// in ledger.Upgrades and applied by the same path a node takes at start
// (multistate.InitLedgerFromStore), then real transactions are validated on both
// sides of its slot.

// upgrade1Slot is where these tests activate upgrade 1: early enough for a few
// mine transits before it, since the test mine chain starts at slot 0 and moves
// one slot per transit.
const upgrade1Slot = 3

// withUpgrade1 lists upgrade 1 at upgrade1Slot, applies it to the test ledger's
// store as a node does at start, and restores the package ledger afterwards.
func withUpgrade1(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library}}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// restartNode stands for a node start on the test ledger's store: the library
// singleton is initialized once per process, so it is dropped first.
func restartNode(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.ResetForTesting()
	require.NoError(t, multistate.InitLedgerFromStore(u.Store()))
}

// constAt evaluates a named u64 constant of the library applying at slot.
func constAt(t *testing.T, slot uint32, name string) uint64 {
	t.Helper()
	res, err := ledger.L(slot).EvalFromSource(nil, name)
	require.NoError(t, err)
	v, err := easyfl_util.Uint64FromBytes(res)
	require.NoError(t, err)
	return v
}

// The upgraded library: the miner version is 5 from the upgrade slot on, the
// settlement window constant exists, _mineShape keeps its function code (an
// output carrying the mine lock parses the same in both libraries), the genesis
// identity is unchanged, and the upgrade index and slot reach the wallet
// constants.
func TestUpgrade1Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	before := ledger.L(0)
	shapeBefore, err := before.FunctionByName("_mineShape")
	require.NoError(t, err)

	withUpgrade1(t, u)

	require.EqualValues(t, 4, constAt(t, upgrade1Slot-1, "constMinerVersion"))
	require.EqualValues(t, 5, constAt(t, upgrade1Slot, "constMinerVersion"))
	require.EqualValues(t, 16, constAt(t, upgrade1Slot, "constMineSettlementWindowTicks"))
	_, err = ledger.L(upgrade1Slot - 1).FunctionByName("constMineSettlementWindowTicks")
	require.Error(t, err, "not in the library before the upgrade")

	after := ledger.L(upgrade1Slot)
	shapeAfter, err := after.FunctionByName("_mineShape")
	require.NoError(t, err)
	require.Equal(t, shapeBefore.FunCode, shapeAfter.FunCode)
	require.NotEqual(t, before.Library.LibraryHash(), after.Library.LibraryHash())

	require.Equal(t, before.Constants.GenesisTimeUnix, after.Constants.GenesisTimeUnix)
	require.Equal(t, before.Constants.Description, after.Constants.Description)
	require.EqualValues(t, 0, ledger.L(upgrade1Slot-1).UpgradeIndex())
	require.EqualValues(t, 1, after.UpgradeIndex())
	require.EqualValues(t, upgrade1Slot, after.Constants.UpgradeSlot)
	require.EqualValues(t, 1, after.Constants.UpgradeIndex)
	require.EqualValues(t, 5, after.MinerVersion)

	// stored once: a second start finds it and leaves the store alone
	restartNode(t, u)
	require.Equal(t, after.Library.LibraryHash(), ledger.L(upgrade1Slot).Library.LibraryHash())
}

// The mine rule across the upgrade. Before the slot, version 4 transits stamped
// at tick 1 are valid, as on the running network. From the slot on, a transit
// must carry version 5, be stamped no earlier than the settlement tick minus the
// sequencer pace (84 with the production constants), and carry transaction
// version 1.
func TestUpgrade1MineRule(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	miner := genesisPrivateKey
	withUpgrade1(t, u)

	v4, v5 := uint64(4), uint64(5)
	minTick := byte(constAt(t, upgrade1Slot, "ticksPerSlot64") -
		constAt(t, upgrade1Slot, "constPreBranchConsolidationTicks") -
		constAt(t, upgrade1Slot, "constMineSettlementWindowTicks") -
		constAt(t, upgrade1Slot, "constTransactionPaceSequencer"))
	require.EqualValues(t, 84, minTick)

	// slots 1 and 2: the old rules
	for slot := 1; slot < upgrade1Slot; slot++ {
		require.NoError(t, u.AddTransaction(buildMineTransition(t, u, miner, mineTxOpts{mine: true, version: &v4})), "slot %d", slot)
	}

	// slot 3, the upgrade slot: everything the old rules allowed is refused
	refused := []struct {
		opts mineTxOpts
		why  string
	}{
		{mineTxOpts{mine: true, version: &v4, tick: minTick}, "current miner version"},
		{mineTxOpts{mine: true, version: &v5}, "stamped before the settlement tick"},
		{mineTxOpts{mine: true, version: &v5, tick: minTick - 1}, "stamped before the settlement tick"},
		{mineTxOpts{mine: true, version: &v5, tick: minTick, txVersion: new(uint16)}, "TxVersion mismatch"},
	}
	for _, r := range refused {
		require.ErrorContains(t, u.AddTransaction(buildMineTransition(t, u, miner, r.opts)), r.why)
	}
	require.NoError(t, u.AddTransaction(buildMineTransition(t, u, miner, mineTxOpts{mine: true, version: &v5, tick: minTick})))

	// and after it
	require.NoError(t, u.AddTransaction(buildMineTransition(t, u, miner, mineTxOpts{mine: true, version: &v5, tick: 127})))
}

// Ordinary transactions follow the transaction version of their slot: an
// address funded before the upgrade under version 0 spends after it, the
// transfer built by the helpers that read the upgrade index off the slot's
// library and carrying version 1.
func TestUpgrade1Transfers(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade1(t, u)

	priv, _, addr := u.GenerateAddress(0)
	_, _, dest := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, 1_000_000_000))
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.Len(t, outs, 1)
	require.Less(t, outs[0].ID.Slot(), uint32(upgrade1Slot), "funded before the upgrade")

	td := utxodb.NewTransferData(priv, addr, base.T(upgrade1Slot+1, 10)).
		MustWithInputs(outs...).
		WithAmount(100_000_000).
		WithTargetLock(dest)
	txBytes, err := utxodb.MakeSimpleTransferTransaction(td)
	require.NoError(t, err)
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err, "parsed with the upgraded library, version 1")
	require.EqualValues(t, 1, ledger.L(tx.Slot()).UpgradeIndex())
	require.NoError(t, u.AddTransaction(txBytes))
}
