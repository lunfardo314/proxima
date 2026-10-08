package ledger

// This file defines the upgrade commitment UTXO format.
// Upgrade UTXOs commit to library upgrades at specific slots.
//
// UTXO Format (6 elements, indices 0..5):
// - 0 Amount: 0 (no tokens)
// - 1 Index-values tuple (standard UTXO indexing slot; empty here)
// - 2 Lock: empty inline data (unspendable - evaluates to false)
// - 3: inline data containing the library hash (32 bytes)
// - 4: inline data containing the previous library hash (32 bytes)
// - 5: inline data containing the previous upgrade slot (4 bytes, BigEndian)
//
// Indices 4 and 5 create a chain of commitments - each upgrade UTXO commits
// to its library hash AND links to the previous upgrade, forming a hash chain
// that commits to the entire upgrade history.
//
// For slot 0 (genesis):
// - Previous library hash is the hash of the EasyFL base library (before upgrade0)
// - Previous upgrade slot is MaxSlot (sentinel indicating "base library")
//
// The synthetic OutputID is created using base.UpgradeOutputID(upgradeSlot).

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// BaseLibraryHash returns the hash of the EasyFL base library (before any upgrades).
// This is used as the previous library hash for slot 0 upgrade UTXO.
func BaseLibraryHash() [32]byte {
	baseLib := newBaseLibrary()
	return baseLib.LibraryHash()
}

// UpgradeUTXO creates an upgrade commitment UTXO.
// This is an unspendable output that commits to a library hash at a specific upgrade slot,
// along with the previous library hash and slot to create a chain of commitments.
//
// Parameters:
// - upgradeSlot: the slot number for this upgrade
// - libraryHash: 32-byte hash of the compiled library JSON
// - prevLibraryHash: 32-byte hash of the previous library (BaseLibraryHash() for slot 0)
// - prevUpgradeSlot: slot of the previous upgrade (base.MaxSlot for slot 0)
//
// Returns:
// - OutputWithID containing the upgrade commitment UTXO
func UpgradeUTXO(upgradeSlot uint32, libraryHash, prevLibraryHash [32]byte, prevUpgradeSlot uint32) *OutputWithID {
	output := NewOutput(func(o *OutputBuilder) {
		// Amount: 0 (no tokens)
		o.WithAmounts(0)

		// Lock: empty inline data (evaluates to false, making output unspendable)
		o.PutConstraint(easyfl.InlineDataBytecode(nil), ConstraintIndexLock)

		// Constraint 2: library hash as inline data
		hashData := easyfl.InlineDataBytecode(libraryHash[:])
		o.MustPushConstraint(hashData)

		// Constraint 3: previous library hash as inline data
		prevHashData := easyfl.InlineDataBytecode(prevLibraryHash[:])
		o.MustPushConstraint(prevHashData)

		// Constraint 4: previous upgrade slot as inline data (4 bytes BigEndian)
		prevSlotBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(prevSlotBytes, prevUpgradeSlot)
		prevSlotData := easyfl.InlineDataBytecode(prevSlotBytes)
		o.MustPushConstraint(prevSlotData)
	})

	return &OutputWithID{
		ID:     base.UpgradeOutputID(upgradeSlot),
		Output: output,
	}
}

// UpgradeUTXOData is the content of an upgrade UTXO. The parse is wallet-side
// (txbuildercore) because the library commitment proof needs it there.
type UpgradeUTXOData = txbuildercore.UpgradeUTXOView

// ParseUpgradeUTXO parses an output and verifies it's a valid upgrade UTXO.
func ParseUpgradeUTXO(o *Output) (*UpgradeUTXOData, error) {
	return txbuildercore.ParseUpgradeUTXO(o.Output)
}

// IsUpgradeUTXO checks if an output with ID is a valid upgrade UTXO.
func IsUpgradeUTXO(o *OutputWithID) bool {
	// Check if the OutputID is a synthetic upgrade OutputID
	if !base.IsUpgradeOutputID(o.ID) {
		return false
	}

	// Try to parse as upgrade UTXO
	_, err := ParseUpgradeUTXO(o.Output)
	return err == nil
}

// VerifyUpgradeUTXO verifies that an upgrade UTXO matches the expected values.
func VerifyUpgradeUTXO(o *OutputWithID, expectedHash, expectedPrevHash [32]byte, expectedPrevSlot uint32) error {
	if !base.IsUpgradeOutputID(o.ID) {
		return fmt.Errorf("not a valid upgrade OutputID")
	}

	data, err := ParseUpgradeUTXO(o.Output)
	if err != nil {
		return err
	}

	if data.LibraryHash != expectedHash {
		return fmt.Errorf("library hash mismatch")
	}

	if data.PrevLibraryHash != expectedPrevHash {
		return fmt.Errorf("previous library hash mismatch")
	}

	if data.PrevUpgradeSlot != expectedPrevSlot {
		return fmt.Errorf("previous upgrade slot mismatch: expected %d, got %d", expectedPrevSlot, data.PrevUpgradeSlot)
	}

	return nil
}

// VerifyUpgradeUTXOChain verifies that an upgrade UTXO correctly links to its predecessor.
// For slot 0, verifies that prevHash matches BaseLibraryHash() and prevSlot is MaxSlot.
// For other slots, the caller must verify the link to the previous upgrade UTXO.
func VerifyUpgradeUTXOChain(o *OutputWithID) (*UpgradeUTXOData, error) {
	upgradeSlot, ok := base.UpgradeSlotFromOutputID(o.ID)
	if !ok {
		return nil, fmt.Errorf("not a valid upgrade OutputID")
	}

	data, err := ParseUpgradeUTXO(o.Output)
	if err != nil {
		return nil, err
	}

	// For slot 0, verify against base library
	if upgradeSlot == 0 {
		if data.PrevUpgradeSlot != base.MaxSlot {
			return nil, fmt.Errorf("slot 0 upgrade UTXO must have prevSlot = MaxSlot, got %d", data.PrevUpgradeSlot)
		}
		expectedPrevHash := BaseLibraryHash()
		if data.PrevLibraryHash != expectedPrevHash {
			return nil, fmt.Errorf("slot 0 upgrade UTXO prevHash does not match base library hash")
		}
	}

	return data, nil
}
