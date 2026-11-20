package tests

// Two branches of equal coverage are ordered by the tie-break of library upgrade 7 (sigma x u - B)
// whether the node holds a branch's transaction or only the stored branch (a virtual transaction
// made from the branch data). Before, a virtual branch fell back to the transaction ID, so nodes
// could order the same tied branches differently in the tip pool.

import (
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestBranchTieBreakHeldOrVirtual(t *testing.T) {
	require.True(t, ledger.L(1).HasUpgrade7())
	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)
	rdr := multistate.MustNewSugaredReadableState(store, genesisRoot)

	// a valid branch of the genesis chain in slot 1 with the given bonus (no sealed bid is checked
	// on the genesis output); the cumulative bonus and the balance follow the bonus
	branchWithBonus := func(bonus uint64) *transaction.Transaction {
		txb, err := txbuilder_seq.NewWithSequencerID(base.T(1, 0), seqID, genesisPrivateKey, rdr)
		require.NoError(t, err)
		_, _, err = txb.BytesWithInputLoader()
		require.NoError(t, err)
		idx := txb.TxData.SequencerOutputIndex
		o, err := ledger.OutputFromBytes(txb.TxData.OutputBytes[idx])
		require.NoError(t, err)
		cc := o.ChainConstraint()
		require.NotNil(t, cc)
		cc.CumulativeBranchBonus = cc.CumulativeBranchBonus - o.Inflation() + bonus
		txb.TxData.OutputBytes[idx] = o.Clone(func(b *ledger.OutputBuilder) {
			b.PutAmounts(int64(o.TokenBalance()-o.Inflation()+bonus), int64(bonus))
			b.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		}).Bytes()
		txb.ComputeInputCommitment()
		txb.SignED25519(genesisPrivateKey)
		tx, err := transaction.ParseAndValidate(txb.TxBuilder.Bytes(), txb.LoadInputBytes)
		require.NoError(t, err)
		return tx
	}
	key := func(tx *transaction.Transaction) uint64 {
		return ledger.TieBreakKey(ledger.L(1).TieBreakSigma, ledger.BranchTieBreakDraw(tx.StemOutput().Output), tx.SequencerOutput().Output.Inflation())
	}

	// pick two branches whose transaction IDs order them against their tie-break keys, so the old
	// fallback to the transaction ID gives the wrong answer and the test can tell the difference
	maxBonus := ledger.L(1).BranchInflationBonusBase(1)
	tx1 := branchWithBonus(1 * base.PROX)
	var tx2 *transaction.Transaction
	for bonus := maxBonus; bonus > 1*base.PROX; bonus-- {
		cand := branchWithBonus(bonus)
		k1, k2 := key(tx1), key(cand)
		if k1 != k2 && (k1 > k2) == base.LessTxID(tx1.ID(), cand.ID()) {
			tx2 = cand
			break
		}
	}
	require.NotNil(t, tx2)
	expected := key(tx1) > key(tx2) // tx1 preferred

	held := func(tx *transaction.Transaction) *vertex.WrappedTx {
		vid := vertex.WrapTxID(tx.ID())
		vid.ConvertVirtualTxToVertexNoLock(vertex.NewVertex(tx))
		vid.SetTxStatusGoodBranch(100)
		return vid
	}
	virtual := func(tx *transaction.Transaction) *vertex.WrappedTx {
		vid := vertex.WrapBranchDataAsVirtualTx(&multistate.BranchData{Stem: tx.StemOutput(), SequencerOutput: tx.SequencerOutput()})
		vid.SetTxStatusGoodBranch(100)
		return vid
	}
	for _, w1 := range []func(*transaction.Transaction) *vertex.WrappedTx{held, virtual} {
		for _, w2 := range []func(*transaction.Transaction) *vertex.WrappedTx{held, virtual} {
			v1, v2 := w1(tx1), w2(tx2)
			require.Equal(t, expected, vertex.IsPreferredMilestoneAgainstTheOther(v1, v2))
			require.Equal(t, !expected, vertex.IsPreferredMilestoneAgainstTheOther(v2, v1))
		}
	}
}
