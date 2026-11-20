package txbuilder_seq

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// The sealed branch bonus at its edges, from library upgrade 7: the bonus a branch commits to is the
// one the next branch mints, and both use the bonus of the sequencer data clamped to [1 mote, 5 PROX].
// A sequencer asking 1 mote commits to it and its next branch mints 1 mote; a stored 0 commits and
// mints 1 mote, a stored MaxUint64 5 PROX. Each branch is validated by the ledger, the sealed-bid and
// interval checks included.
func TestBranchBonusEdges(t *testing.T) {
	key := ledger.InitWithTestingLedgerData()
	t.Cleanup(ledger.ResetForTesting)
	require.True(t, ledger.L(0).HasUpgrade7())

	store := common.NewInMemoryKVStore()
	seqID, root := multistate.InitStateStoreFromGlobals(store)
	state := multistate.MustNewUpdatable(store, root)

	// branch builds the genesis sequencer's branch at slot, asking bonus for its next branch, validates
	// it and applies it to the state; returns the bonus the branch minted
	branch := func(slot uint32, bonus uint64) uint64 {
		rdr := multistate.MustNewSugaredReadableState(store, state.Root())
		txb, err := NewWithSequencerID(base.T(slot, 0), seqID, key, rdr)
		require.NoError(t, err)
		txb.nextSeqData.SetBranchBonus(bonus)
		txBytes, loader, err := txb.BytesWithInputLoader()
		require.NoError(t, err)
		tx, err := transaction.ParseAndValidate(txBytes, loader)
		require.NoError(t, err)
		require.NoError(t, state.Update(tx.StateMutations(), nil))
		return tx.SequencerOutput().Output.Inflation()
	}

	// the first branch, on the genesis output, mints the default and commits to 1 mote
	require.EqualValues(t, seqdata.DefaultBranchBonus, branch(1, 1))
	// its successor reveals and mints 1 mote, and commits to a stored 0
	require.EqualValues(t, 1, branch(2, 0))
	// 0 is read as 1 mote
	require.EqualValues(t, 1, branch(3, math.MaxUint64))
	// MaxUint64 is read as 5 PROX
	require.EqualValues(t, seqdata.DefaultBranchBonus, branch(4, seqdata.DefaultBranchBonus))
}
