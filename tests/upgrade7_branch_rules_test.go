package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// The rules of library upgrade 7 on branches (.internal/library_upgrade7.md), checked on real branches
// of the genesis sequencer built by the sequencer's builder, one per slot, then altered and signed
// again. From the slot a branch mints the bonus its chain predecessor committed to and reveals the
// salt; a branch with another salt, with a bonus outside [1 mote, 5 PROX], or consuming a third input
// is refused. A branch whose chain predecessor was produced before the slot needs no commitment.
func TestUpgrade7BranchRules(t *testing.T) {
	const (
		upgrade1Slot = 10
		upgrade2Slot = upgrade1Slot + multistate.MinSlotsBetweenUpgrades
		upgrade3Slot = upgrade2Slot + multistate.MinSlotsBetweenUpgrades
		upgrade4Slot = upgrade3Slot + multistate.MinSlotsBetweenUpgrades
		upgrade5Slot = upgrade4Slot + multistate.MinSlotsBetweenUpgrades
		upgrade6Slot = upgrade5Slot + multistate.MinSlotsBetweenUpgrades
		upgradeSlot  = upgrade6Slot + multistate.MinSlotsBetweenUpgrades
		// what the altered branch before the slot sets aside for the third-input case
		setAside = 100 * base.PROX
	)
	withUnfoldedGenesis(t, []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: ledger.BuildUpgrade6LibraryAt(upgrade6Slot)},
		{Slot: upgradeSlot, Build: ledger.BuildUpgrade7LibraryAt(upgradeSlot)},
	})
	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)
	ledger.ResetForTesting()
	require.NoError(t, multistate.InitLedgerFromStore(store))
	require.True(t, ledger.L(upgradeSlot).HasUpgrade7())

	state := multistate.MustNewUpdatable(store, genesisRoot)
	addr := ledger.SigLockFromED25519PrivateKey(genesisPrivateKey)

	// branch builds the genesis sequencer's branch at slot on the current state, applies alter to the
	// finished transaction, signs it again and validates it; the branch is applied to the state when
	// apply is set and it is valid
	branch := func(slot uint32, alter func(txb *txbuilder_seq.SeqTxBuilder), apply bool) (*transaction.Transaction, error) {
		rdr := multistate.MustNewSugaredReadableState(store, state.Root())
		txb, err := txbuilder_seq.NewWithSequencerID(base.T(slot, 0), seqID, genesisPrivateKey, rdr)
		require.NoError(t, err)
		_, _, err = txb.BytesWithInputLoader()
		require.NoError(t, err)
		if alter != nil {
			alter(txb)
			txb.ComputeInputCommitment()
			txb.SignED25519(genesisPrivateKey)
		}
		tx, err := transaction.ParseAndValidate(txb.TxBuilder.Bytes(), txb.LoadInputBytes)
		if err == nil && apply {
			require.NoError(t, state.Update(tx.StateMutations(), nil))
		}
		return tx, err
	}
	// seqOutput is the branch's sequencer output and its index among the outputs
	seqOutput := func(txb *txbuilder_seq.SeqTxBuilder) (*ledger.Output, byte) {
		idx := txb.TxData.SequencerOutputIndex
		o, err := ledger.OutputFromBytes(txb.TxData.OutputBytes[idx])
		require.NoError(t, err)
		require.LessOrEqual(t, o.Amounts().NumElements(), 2, "the genesis sequencer holds no frozen coverage")
		return o, idx
	}

	_, err := branch(upgradeSlot-5, nil, true)
	require.NoError(t, err)
	// the last branch before the slot sets aside an output of its own key, the third input later
	tx, err := branch(upgradeSlot-1, func(txb *txbuilder_seq.SeqTxBuilder) {
		o, idx := seqOutput(txb)
		txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
			b.PutAmounts(int64(o.TokenBalance()-setAside), int64(o.Inflation()))
		}).Bytes()
		_, err := txb.ProduceOutput(ledger.NewOutput(func(b *ledger.OutputBuilder) {
			b.WithAmounts(setAside).WithLock(addr)
		}))
		require.NoError(t, err)
	}, true)
	require.NoError(t, err)
	asideID := base.MustNewOutputID(tx.ID(), byte(tx.NumProducedOutputs()-1))
	aside := tx.MustProducedOutputWithIDAt(byte(tx.NumProducedOutputs() - 1))

	// the first branch from the slot: its chain predecessor is from before it, no commitment needed
	tx, err = branch(upgradeSlot+1, nil, true)
	require.NoError(t, err)
	require.EqualValues(t, seqdata.DefaultBranchBonus, tx.SequencerOutput().Output.Inflation())

	t.Run("the honest branch reveals its predecessor's bid", func(t *testing.T) {
		tx, err := branch(upgradeSlot+2, nil, false)
		require.NoError(t, err)
		require.EqualValues(t, seqdata.DefaultBranchBonus, tx.SequencerOutput().Output.Inflation())
	})
	t.Run("another salt", func(t *testing.T) {
		_, err := branch(upgradeSlot+2, func(txb *txbuilder_seq.SeqTxBuilder) {
			txb.TxData.TxConstraints[ledger.BranchSaltTxConstraintIndex] = easyfl.InlineDataBytecode([]byte{1, 2, 3, 4, 5, 6, 7, 8})
		}, false)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "branch bonus does not match the sealed bid"))
	})
	t.Run("a bonus outside the interval", func(t *testing.T) {
		_, err := branch(upgradeSlot+2, func(txb *txbuilder_seq.SeqTxBuilder) {
			o, idx := seqOutput(txb)
			txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
				b.PutAmounts(int64(o.TokenBalance()-o.Inflation()), 0)
			}).Bytes()
		}, false)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "branch bonus must be between 1 mote and the bonus bound"))
	})
	t.Run("a third input", func(t *testing.T) {
		_, err := branch(upgradeSlot+2, func(txb *txbuilder_seq.SeqTxBuilder) {
			idx, err := txb.ConsumeOutput(aside.Output, asideID)
			require.NoError(t, err)
			require.NoError(t, txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0))
			_, err = txb.ProduceOutput(ledger.NewOutput(func(b *ledger.OutputBuilder) {
				b.WithAmounts(setAside).WithLock(addr)
			}))
			require.NoError(t, err)
		}, false)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "branch must consume only its stem and chain predecessor"))
	})
}
