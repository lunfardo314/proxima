package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// A branch must consume its predecessor stem: consuming it is what runs the stem's checks (the
// predecessor, the VRF proof, the supply and coverage recurrences). Branches of the genesis
// sequencer are built by the sequencer's builder; in the last one the stem input is replaced by an
// ordinary output of the same key. The ledger accepts it; the node check CheckStemAndTargetUnlocks
// refuses it, and passes the honest branches.
func TestBranchConsumesItsStem(t *testing.T) {
	const setAside = 100 * base.PROX
	saved := transaction.StemTargetCheckFromSlot
	transaction.StemTargetCheckFromSlot = 0
	t.Cleanup(func() { transaction.StemTargetCheckFromSlot = saved })

	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)
	state := multistate.MustNewUpdatable(store, genesisRoot)
	addr := ledger.SigLockFromED25519PrivateKey(genesisPrivateKey)

	// branch builds the genesis sequencer's branch at slot on the current state, applies alter to the
	// finished transaction, signs it again and validates it
	branch := func(slot uint32, alter func(txb *txbuilder_seq.SeqTxBuilder)) (*transaction.Transaction, error) {
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
		return transaction.ParseAndValidate(txb.TxBuilder.Bytes(), txb.LoadInputBytes)
	}
	apply := func(tx *transaction.Transaction) {
		require.NoError(t, state.Update(tx.StateMutations(), nil))
	}

	// the first branch sets aside an output of its own key
	tx, err := branch(1, func(txb *txbuilder_seq.SeqTxBuilder) {
		idx := txb.TxData.SequencerOutputIndex
		o, err := ledger.OutputFromBytes(txb.TxData.OutputBytes[idx])
		require.NoError(t, err)
		txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
			b.PutAmounts(int64(o.TokenBalance()-setAside), int64(o.Inflation()))
		}).Bytes()
		_, err = txb.ProduceOutput(ledger.NewOutput(func(b *ledger.OutputBuilder) {
			b.WithAmounts(setAside).WithLock(addr)
		}))
		require.NoError(t, err)
	})
	require.NoError(t, err)
	require.NoError(t, tx.CheckStemAndTargetUnlocks(), "an honest branch passes")
	apply(tx)
	aside := tx.MustProducedOutputWithIDAt(byte(tx.NumProducedOutputs() - 1))

	tx, err = branch(2, nil)
	require.NoError(t, err)
	require.NoError(t, tx.CheckStemAndTargetUnlocks(), "an honest branch passes")
	apply(tx)

	// the third branch with its stem input replaced by the output set aside
	forged, err := branch(3, func(txb *txbuilder_seq.SeqTxBuilder) {
		txb.ReplaceInput(1, aside.Output.Bytes(), aside.ID)
		txb.ConsumedOutputs[1] = aside.Output
		require.NoError(t, txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0))
		_, err := txb.ProduceOutput(ledger.NewOutput(func(b *ledger.OutputBuilder) {
			b.WithAmounts(setAside).WithLock(addr)
		}))
		require.NoError(t, err)
	})
	require.NoError(t, err, "the ledger accepts the branch without its stem")
	err = forged.CheckStemAndTargetUnlocks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not consume its predecessor stem")
}
