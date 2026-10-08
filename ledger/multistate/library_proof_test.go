package multistate

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// The library commitment proof (kb/library_proof.md), both halves: the node's
// UTXOProof over a real genesis state and the wallet's VerifyLibraryCommitment,
// round-tripped through the JSON wire form. The branch is assembled with the
// wallet's raw serializer, not the ledger's builder: the verifier never runs a
// constraint, so a structurally correct branch that would fail validation still
// exercises every step of the check (ID, stem, root, proof, UTXO, hash).

// fakeBranchBytes serialises a branch transaction at the slot whose stem output
// names root as its baseline.
func fakeBranchBytes(root []byte, slot uint32) []byte {
	stem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(0).WithLock(&ledger.StemLock{})
		o.PutConstraint((&ledger.OracleData{BaselineRoot: root}).Bytes(), ledger.ConstraintIndexChain)
	})
	return txbuildercore.SerializeRawTxBytes(&txbuildercore.TxRawData{
		Timestamp:            base.LedgerTime{Slot: slot}, // tick 0: a branch
		SequencerOutputIndex: 0,
		SequencerData:        []byte{0, 1}, // sequencer output 0, stem output 1
		OutputBytes:          [][]byte{stem.Bytes(), stem.Bytes()},
	})
}

func TestLibraryProof(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MustNewReadable(store, root)
	lib := ledger.L(0)

	oid := base.UpgradeOutputID(0)
	utxoBytes, found := rdr.GetUTXO(oid)
	require.True(t, found)
	proof := rdr.UTXOProof(oid)

	branchBytes := fakeBranchBytes(root.Bytes(), 1)
	branchID, err := txbuildercore.TxIDFromBytes(branchBytes)
	require.NoError(t, err)
	require.True(t, branchID.IsBranchTransaction())

	commitment := &txbuildercore.LibraryCommitment{
		BranchID:         branchID,
		BranchTxBytes:    branchBytes,
		UpgradeUTXOBytes: utxoBytes,
		Proof:            proof.Bytes(),
	}
	prevHash := ledger.BaseLibraryHash()
	definition := func(libraryJSON []byte, c *txbuildercore.LibraryCommitment) *txbuildercore.LedgerDefinitionJSON {
		def := &txbuildercore.LedgerDefinitionJSON{
			UpgradeSlot:     0,
			LibraryJSON:     string(libraryJSON),
			PrevLibraryHash: hex.EncodeToString(prevHash[:]),
			PrevUpgradeSlot: base.MaxSlot,
		}
		if c != nil {
			def.Commitment = c.JSONAble()
		}
		return def
	}

	t.Run("wire round trip", func(t *testing.T) {
		data, err := json.Marshal(commitment.JSONAble())
		require.NoError(t, err)
		var back txbuildercore.LibraryCommitmentJSON
		require.NoError(t, json.Unmarshal(data, &back))
		parsed, err := back.Parse()
		require.NoError(t, err)
		require.Equal(t, commitment, parsed)
	})

	t.Run("honest node", func(t *testing.T) {
		walletLib, c, err := txbuildercore.LibraryFromLedgerDefinition(definition(lib.DefinitionsJSON(), commitment))
		require.NoError(t, err)
		require.Equal(t, branchID, c.BranchID)
		require.Equal(t, lib.LibraryHash(), walletLib.LibraryHash())
	})

	t.Run("no commitment", func(t *testing.T) {
		_, _, err := txbuildercore.LibraryFromLedgerDefinition(definition(lib.DefinitionsJSON(), nil))
		require.ErrorContains(t, err, "does not provide")
	})

	t.Run("node cannot prove", func(t *testing.T) {
		def := definition(lib.DefinitionsJSON(), nil)
		def.Commitment = &txbuildercore.LibraryCommitmentJSON{Error: "retry in a slot"}
		_, _, err := txbuildercore.LibraryFromLedgerDefinition(def)
		require.ErrorContains(t, err, "retry in a slot")
	})

	// A tampered library: one function renamed is the smallest remap, and the
	// hash covers names. The JSON's own hash field is left as it was, so a
	// verifier that trusted that field would pass.
	t.Run("tampered library", func(t *testing.T) {
		desc, err := easyfl.ReadLibraryFromJSON(lib.DefinitionsJSON())
		require.NoError(t, err)
		desc.Functions[len(desc.Functions)-1].Sym += "x"
		tampered, err := json.Marshal(desc)
		require.NoError(t, err)
		_, _, err = txbuildercore.LibraryFromLedgerDefinition(definition(tampered, commitment))
		require.ErrorContains(t, err, "the ledger commits to library")
	})

	expected := txbuildercore.UpgradeUTXOView{LibraryHash: lib.LibraryHash(), PrevLibraryHash: prevHash, PrevUpgradeSlot: base.MaxSlot}
	tamper := func(mod func(c *txbuildercore.LibraryCommitment)) error {
		c := *commitment
		mod(&c)
		return txbuildercore.VerifyLibraryCommitment(&c, 0, expected)
	}

	t.Run("branch bytes of another branch", func(t *testing.T) {
		err := tamper(func(c *txbuildercore.LibraryCommitment) { c.BranchTxBytes = fakeBranchBytes(root.Bytes(), 2) })
		require.ErrorContains(t, err, "not to the named branch")
	})

	t.Run("forged baseline root", func(t *testing.T) {
		flipped := root.Bytes()
		flipped[0] ^= 0x01
		forged := fakeBranchBytes(flipped, 1)
		forgedID, err := txbuildercore.TxIDFromBytes(forged)
		require.NoError(t, err)
		err = tamper(func(c *txbuildercore.LibraryCommitment) { c.BranchID, c.BranchTxBytes = forgedID, forged })
		require.ErrorContains(t, err, "does not bind the upgrade UTXO")
	})

	t.Run("proof of another key", func(t *testing.T) {
		other := rdr.UTXOProof(base.GenesisStemOutputID())
		err := tamper(func(c *txbuildercore.LibraryCommitment) { c.Proof = other.Bytes() })
		require.ErrorContains(t, err, "another key")
	})

	t.Run("proof of absence", func(t *testing.T) {
		absent := rdr.UTXOProof(base.UpgradeOutputID(7))
		c := *commitment
		c.Proof = absent.Bytes()
		err := txbuildercore.VerifyLibraryCommitment(&c, 7, expected)
		require.ErrorContains(t, err, "does not bind the upgrade UTXO")
	})

	t.Run("not a branch", func(t *testing.T) {
		nonBranch := txbuildercore.SerializeRawTxBytes(&txbuildercore.TxRawData{
			Timestamp:            base.LedgerTime{Slot: 1, Tick: 3},
			SequencerOutputIndex: 0,
			SequencerData:        []byte{0, 1},
			OutputBytes:          [][]byte{utxoBytes, utxoBytes},
		})
		id, err := txbuildercore.TxIDFromBytes(nonBranch)
		require.NoError(t, err)
		err = tamper(func(c *txbuildercore.LibraryCommitment) { c.BranchID, c.BranchTxBytes = id, nonBranch })
		require.True(t, strings.Contains(err.Error(), "not a branch transaction"), err.Error())
	})
}
