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
// predecessor, the VRF proof, the supply and coverage recurrences). A branch of the genesis
// sequencer, built by the sequencer's builder, has its stem input replaced by an ordinary output of
// the same key. Under library upgrade 7 the ledger refuses it; on a ledger without upgrade 7 the
// ledger accepts it and the node check CheckStemAndTargetUnlocks refuses it.
func TestBranchConsumesItsStem(t *testing.T) {
	t.Run("upgrade 7: the ledger refuses", func(t *testing.T) {
		_, err := forgedBranchWithoutStem(t)
		require.Error(t, err)
		require.Contains(t, err.Error(), "branch must consume its predecessor stem")
	})
	t.Run("before upgrade 7: the node check refuses", func(t *testing.T) {
		// a genesis folding upgrades 1 to 6 only: upgrade 7 is listed at its live slot, far ahead
		extraLedgerOptions = []ledger.ParametersOption{ledger.WithFoldedUpgrades(6)}
		reinitTestLedger()
		saved := transaction.StemTargetCheckFromSlot
		transaction.StemTargetCheckFromSlot = 0
		t.Cleanup(func() {
			transaction.StemTargetCheckFromSlot = saved
			extraLedgerOptions = nil
			reinitTestLedger()
		})
		require.False(t, ledger.L(10).HasUpgrade7())

		forged, err := forgedBranchWithoutStem(t)
		require.NoError(t, err, "the ledger without upgrade 7 accepts the branch")
		err = forged.CheckStemAndTargetUnlocks()
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not consume its predecessor stem")
	})
}

// forgedBranchWithoutStem builds two honest branches of the genesis sequencer, the first setting
// aside an output of its key, then a third branch whose stem input is replaced by that output, and
// validates the third. It returns the parsed transaction when the ledger accepts it.
func forgedBranchWithoutStem(t *testing.T) (*transaction.Transaction, error) {
	const setAside = 100 * base.PROX
	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)
	state := multistate.MustNewUpdatable(store, genesisRoot)
	addr := ledger.SigLockFromED25519PrivateKey(genesisPrivateKey)

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
	apply(tx)
	asideIdx := byte(tx.NumProducedOutputs() - 1)
	aside := tx.MustProducedOutputWithIDAt(asideIdx)

	tx, err = branch(2, nil)
	require.NoError(t, err)
	apply(tx)

	return branch(3, func(txb *txbuilder_seq.SeqTxBuilder) {
		// input 1 is the predecessor stem: replace it by the output set aside
		txb.ReplaceInput(1, aside.Output.Bytes(), aside.ID)
		txb.ConsumedOutputs[1] = aside.Output
		require.NoError(t, txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0))
		_, err := txb.ProduceOutput(ledger.NewOutput(func(b *ledger.OutputBuilder) {
			b.WithAmounts(setAside).WithLock(addr)
		}))
		require.NoError(t, err)
	})
}
