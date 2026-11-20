package tests

// Safeguard test: the upper bound of the freely chosen branch bonus (5 PROX) is
// enforced by the ledger, also where no sealed bid is checked (branch on the genesis output,
// _sealedBidRevealed lets it pass without a commitment). The existing test covers only the
// lower bound. Built like TestUpgrade7BranchRules: a real branch of the genesis chain,
// altered and re-signed.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestUpgrade7BranchBonusUpperBound(t *testing.T) {
	require.True(t, ledger.L(0).HasUpgrade7())
	maxBonus := ledger.L(1).BranchInflationBonusBase(1)
	require.EqualValues(t, 5*base.PROX, maxBonus, "ledger bound: 5 PROX")

	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)
	state := multistate.MustNewUpdatable(store, genesisRoot)

	// branch of the genesis chain in slot 1 (predecessor = genesis output, so no commitment),
	// bonus on the sequencer output replaced, balance adjusted by the same difference
	branchWithBonus := func(bonus uint64) error {
		rdr := multistate.MustNewSugaredReadableState(store, state.Root())
		txb, err := txbuilder_seq.NewWithSequencerID(base.T(1, 0), seqID, genesisPrivateKey, rdr)
		require.NoError(t, err)
		_, _, err = txb.BytesWithInputLoader()
		require.NoError(t, err)
		idx := txb.TxData.SequencerOutputIndex
		o, err := ledger.OutputFromBytes(txb.TxData.OutputBytes[idx])
		require.NoError(t, err)
		// cumulative bonus adjusted to match, as an attacker would: only the range check remains
		cc := o.ChainConstraint()
		require.NotNil(t, cc)
		cc.CumulativeBranchBonus = cc.CumulativeBranchBonus - o.Inflation() + bonus
		txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
			b.PutAmounts(int64(o.TokenBalance()-o.Inflation()+bonus), int64(bonus))
			b.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		}).Bytes()
		txb.ComputeInputCommitment()
		txb.SignED25519(genesisPrivateKey)
		_, err = transaction.ParseAndValidate(txb.TxBuilder.Bytes(), txb.LoadInputBytes)
		return err
	}

	t.Run("exactly the bound is accepted", func(t *testing.T) {
		require.NoError(t, branchWithBonus(maxBonus))
	})
	for _, bonus := range []uint64{maxBonus + 1, 1000 * base.PROX} {
		err := branchWithBonus(bonus)
		require.Error(t, err, "bonus %d above the bound must be refused", bonus)
		require.NoError(t, util.MustErrorWith(err, "branch bonus must be between 1 mote and the bonus bound"),
			"refused, but not by the range check: %v", err)
	}
}

// (a) a branch cannot mint a bonus other than the sealed one, even with the correct salt and a
// bonus within range; (b) on a predecessor without a bid (empty commitment) no branch can be
// built, so nothing is minted.
func TestUpgrade7SealedBid(t *testing.T) {
	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)

	build := func(state *multistate.Updatable, slot uint32, alter func(txb *txbuilder_seq.SeqTxBuilder), apply bool) error {
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
		return err
	}
	seqOut := func(txb *txbuilder_seq.SeqTxBuilder) (*ledger.Output, byte) {
		idx := txb.TxData.SequencerOutputIndex
		o, err := ledger.OutputFromBytes(txb.TxData.OutputBytes[idx])
		require.NoError(t, err)
		return o, idx
	}

	t.Run("bonus other than sealed", func(t *testing.T) {
		state := multistate.MustNewUpdatable(store, genesisRoot)
		require.NoError(t, build(state, 1, nil, true)) // commits the default bonus
		err := build(state, 2, func(txb *txbuilder_seq.SeqTxBuilder) {
			o, idx := seqOut(txb)
			// the default bonus is the bound (5 PROX); one mote less is in range but not the sealed bid
			require.Greater(t, o.Inflation(), uint64(1))
			other := o.Inflation() - 1
			// cumulative bonus adjusted as well, so only the sealed bid stands in the way
			cc := o.ChainConstraint()
			require.NotNil(t, cc)
			cc.CumulativeBranchBonus--
			txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
				b.PutAmounts(int64(o.TokenBalance()-1), int64(other))
				b.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
			}).Bytes()
		}, false)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "branch bonus does not match the sealed bid"), "%v", err)
	})

	t.Run("predecessor without bid: no branch", func(t *testing.T) {
		state := multistate.MustNewUpdatable(store, genesisRoot)
		require.NoError(t, build(state, 1, func(txb *txbuilder_seq.SeqTxBuilder) {
			o, idx := seqOut(txb)
			txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
				b.PutConstraint(ledger.SeqExtensionBytecode(nil), ledger.SeqExtensionFixedIndex)
			}).Bytes()
		}, true), "a sequencer output without a bid is allowed")
		err := build(state, 2, nil, false)
		require.Error(t, err, "no branch may mint on a predecessor without a bid")
		require.NoError(t, util.MustErrorWith(err, "branch bonus does not match the sealed bid"), "%v", err)
	})
}
