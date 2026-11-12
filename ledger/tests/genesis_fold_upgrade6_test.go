package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// A genesis folding upgrades 1 to 6, as `proxi init genesis` makes: the transaction version
// counts the folded upgrades, so upgrade 6's rules, keyed on version 6, hold from genesis.

// upgrade7Slot is where TestFoldedGenesisLaterUpgrade lists an upgrade after the folded six.
const upgrade7Slot = 600

// foldedSix are upgrades 1 to 6, upgrade 6 folded at the test's mine difficulty floor (the
// real floor, 24, is above the test band [6, 10]), and the upgrades listed after them.
func foldedSix(later ...*ledger.UpgradeDefinition) []*ledger.UpgradeDefinition {
	upgrade6 := func(prev []byte) ([]byte, error) {
		lib, err := ledger.BuildUpgrade6LibraryAt(0)(prev)
		if err != nil {
			return nil, err
		}
		return ledger.BuildUpgradeFromSource(lib, "test floor", "func constMineFloorDifficulty : u64/6")
	}
	return append([]*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: ledger.BuildUpgrade6LibraryAt(upgrade6Slot), Fold: upgrade6},
	}, later...)
}

// withFoldedSix starts the test ledger on a genesis folding upgrades 1 to 6, with the release
// listing them and the later ones, through the path a node takes at start.
func withFoldedSix(t *testing.T, later ...*ledger.UpgradeDefinition) *utxodb.UTXODB {
	t.Helper()
	ledger.Upgrades = foldedSix(later...)
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	key := initTestLedger(func(par *ledger.InitParameters) { par.FoldUpgrades = ledger.Upgrades[:6] })
	require.Equal(t, genesisPrivateKey, key)
	u := utxodb.NewUTXODB(key, true)
	restartNode(t, u)
	return u
}

// requireSignerBoundID checks that a transfer at slot carries version and is valid signed by
// the builder, and that one signed over the transaction ID of the old rule is refused.
func requireSignerBoundID(t *testing.T, u *utxodb.UTXODB, addr ledger.SigLock, sign func(*exhelp.Builder), oldRule func(*exhelp.Builder), slot uint32, version uint16) {
	t.Helper()
	txBytes := transferAt6(t, u, addr, slot, sign)
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	require.EqualValues(t, version, tx.Version())
	require.NoError(t, u.AddTransaction(txBytes))

	err = u.AddTransaction(transferAt6(t, u, addr, slot+1, oldRule))
	require.Error(t, err, "a signature over the old ID is refused")
	require.NoError(t, util.MustErrorWith(err, "invalid signature of the transaction"))
}

// signers returns the builder's signing and the signing over the old rule's ID, as a wallet
// not upgraded would sign, for the key of address index i.
func signers(t *testing.T, u *utxodb.UTXODB, i int) (ledger.SigLock, func(*exhelp.Builder), func(*exhelp.Builder)) {
	priv, _, addr := u.GenerateAddress(i)
	require.NoError(t, u.TokensFromFaucet(addr, 1_000_000_000))
	builderSigned := func(txb *exhelp.Builder) { txb.SignED25519(priv) }
	oldRuleSigned := func(txb *exhelp.Builder) {
		txb.SignED25519(priv)
		tree := txb.ToTuple().AsTree()
		id, err := txbuildercore.TxIDFromTree(tree)
		require.NoError(t, err)
		ess, err := txbuildercore.HashEssence(tree)
		require.NoError(t, err)
		copy(id[base.LedgerTimeByteLength+1:], ess[base.LedgerTimeByteLength+1:])
		txb.TxData.SignatureData = base.SignatureDataED25519(priv, id[:])
	}
	return addr, builderSigned, oldRuleSigned
}

// The folded genesis carries upgrade 6's definitions built at slot 0: version 6 from genesis,
// in the library and in the wallet constants, sequencer pace 6, the sequencer origin minimum
// balance binding every chain, and the genesis's own start slot of mining, not a pause after
// the live activation slot. Transfers carry the signer-bound ID and a transaction-hash transit
// is valid, all from the first slots.
func TestFoldedGenesisUpgrade6(t *testing.T) {
	u := withFoldedSix(t)

	require.Equal(t, 6, ledger.L(0).FoldedUpgrades())
	require.EqualValues(t, 6, ledger.L(0).UpgradeIndex())
	require.EqualValues(t, 6, ledger.L(0).Constants.UpgradeIndex)
	require.True(t, ledger.L(0).Constants.MineWorkIsTxID())
	require.EqualValues(t, 6, constAt(t, 0, "constTransactionPaceSequencer"))
	require.EqualValues(t, 6, constAt(t, 0, "constMinerVersion"))
	require.Zero(t, constAt(t, 0, "constMinSequencerOriginBalanceSlot"))
	require.Zero(t, constAt(t, 0, "constDisableMiningUntilSlot"), "the test genesis mines from slot 0")

	addr, sign, oldRule := signers(t, u, 1)
	requireSignerBoundID(t, u, addr, sign, oldRule, 10, 6)

	priv, _, minerAddr := u.GenerateAddress(3)
	require.NoError(t, u.TokensFromFaucet(minerAddr, 100*base.PROX))
	txBytes := buildHashTransit(t, u, priv, 20, hashTransitOpts{})
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	require.True(t, tx.MineWorkIsTxID())
	require.NoError(t, u.AddTransaction(txBytes), "a transaction-hash transit from genesis")
}

// An upgrade listed at a slot after the folded six is upgrade 7: the version moves from 6 to 7
// at its slot, and the signer-bound ID holds on both sides.
func TestFoldedGenesisLaterUpgrade(t *testing.T) {
	u := withFoldedSix(t, &ledger.UpgradeDefinition{
		Slot: upgrade7Slot,
		Build: func(prev []byte) ([]byte, error) {
			return ledger.BuildUpgradeFromSource(prev, "upgrade 7", "func constMinerVersion : u64/7")
		},
	})
	require.EqualValues(t, 6, ledger.L(upgrade7Slot-1).UpgradeIndex())
	require.EqualValues(t, 7, ledger.L(upgrade7Slot).UpgradeIndex())
	require.EqualValues(t, 7, ledger.L(upgrade7Slot).Constants.UpgradeIndex)
	require.EqualValues(t, 7, constAt(t, upgrade7Slot, "constMinerVersion"))

	addr, sign, oldRule := signers(t, u, 1)
	requireSignerBoundID(t, u, addr, sign, oldRule, upgrade7Slot-10, 6)
	requireSignerBoundID(t, u, addr, sign, oldRule, upgrade7Slot+10, 7)
}
