package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/lunfardo314/proxima/util/vrf"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// Library upgrade 3 chains a branch's VRF message on its predecessor stem's VRF
// output instead of its proof (kb/library_upgrade3.md). Real branches are built
// by the sequencer's builder, one per slot, on both sides of the upgrade slot,
// and each must validate. Each proof is also checked to verify against the
// message of its own side only: since the ledger accepted it, the ledger checked
// that message.
func TestUpgrade3BranchVRFMessage(t *testing.T) {
	const upgradeSlot = multistate.MinSlotsBetweenUpgrades + 10

	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: upgradeSlot, Build: ledger.BuildUpgrade3Library}}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		reinitTestLedger()
	})
	store := common.NewInMemoryKVStore()
	seqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)
	// as a node does at start: build and store the listed upgrade
	ledger.ResetForTesting()
	require.NoError(t, multistate.InitLedgerFromStore(store))

	pk := genesisPrivateKey.Public().(ed25519.PublicKey)
	state := multistate.MustNewUpdatable(store, genesisRoot)

	// branch builds and validates a branch of the genesis sequencer at slot on the
	// current state, and applies it to the state when apply is set. Returns the VRF
	// proof of the stem it consumed and of the one it produced.
	branch := func(slot uint32, apply bool) (predProof, proof []byte) {
		rdr := multistate.MustNewSugaredReadableState(store, state.Root())
		predStem, ok := rdr.GetStemOutput().Output.StemLock()
		require.True(t, ok)
		txb, err := txbuilder_seq.NewWithSequencerID(base.T(slot, 0), seqID, genesisPrivateKey, rdr)
		require.NoError(t, err)
		txBytes, _, s, err := txbtest.BuildAndValidate(txb)
		require.NoError(t, err, "branch at slot %d must validate\n%s", slot, s)

		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)
		stem, ok := tx.StemOutput().Output.StemLock()
		require.True(t, ok)
		if apply {
			require.NoError(t, state.Update(tx.StateMutations(), nil))
		}
		return predStem.VRFProof, stem.VRFProof
	}
	oldMessage := func(predProof []byte, slot uint32) []byte {
		return common.Concat(predProof, base.Slot2Bytes(slot))
	}
	newMessage := func(predProof []byte, slot uint32) []byte {
		beta, err := vrf.ProofToHash(predProof)
		require.NoError(t, err)
		return common.Concat(beta, base.Slot2Bytes(slot))
	}
	verifies := func(msg, proof []byte) bool {
		_, err := vrf.Verify(pk, msg, proof)
		return err == nil
	}

	// The library defines the message on each side of the slot.
	someProof, err := vrf.Prove(genesisPrivateKey, []byte("some message"))
	require.NoError(t, err)
	require.Equal(t, oldMessage(someProof, upgradeSlot-1), ledger.L(upgradeSlot-1).BranchVRFMessage(someProof, upgradeSlot-1))
	require.Equal(t, newMessage(someProof, upgradeSlot), ledger.L(upgradeSlot).BranchVRFMessage(someProof, upgradeSlot))

	// Genesis: the genesis stem carries no proof, and the first branch's message
	// is the slot alone under both rules, so the chain from genesis stays valid.
	for _, slot := range []uint32{upgradeSlot - 1, upgradeSlot} {
		require.Equal(t, base.Slot2Bytes(slot), ledger.L(slot).BranchVRFMessage(nil, slot))
		predProof, proof := branch(slot, false)
		require.Empty(t, predProof)
		require.True(t, verifies(base.Slot2Bytes(slot), proof))
	}

	// Before the upgrade: the message is chained on the predecessor's proof.
	branch(upgradeSlot-2, true)
	predProof, proof := branch(upgradeSlot-1, true)
	require.NotEmpty(t, predProof)
	require.True(t, verifies(oldMessage(predProof, upgradeSlot-1), proof))
	require.False(t, verifies(newMessage(predProof, upgradeSlot-1), proof))

	// From the upgrade slot, over a predecessor made under the old rule, and then
	// over one made under the new: chained on the predecessor's output.
	for _, slot := range []uint32{upgradeSlot, upgradeSlot + 1} {
		predProof, proof = branch(slot, true)
		require.True(t, verifies(newMessage(predProof, slot), proof))
		require.False(t, verifies(oldMessage(predProof, slot), proof))
	}
}
