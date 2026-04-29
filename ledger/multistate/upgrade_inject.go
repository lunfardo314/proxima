package multistate

// This file implements upgrade UTXO injection during branch production.
// When a branch is committed, we check if any upgrade UTXOs are missing
// from the baseline state and inject them into the mutations.

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// InjectedUpgrade contains information about an injected upgrade UTXO.
type InjectedUpgrade struct {
	Slot        uint32
	LibraryHash [32]byte
	LibraryYAML []byte
}

// InjectMissingUpgradeUTXOs checks for any upgrade slots up to branchSlot that
// don't have their upgrade UTXO in the baseline state, and injects them into mutations.
// This ensures upgrade UTXOs are committed when crossing upgrade boundaries.
//
// Each upgrade UTXO contains:
// - The library hash for its upgrade slot
// - The previous library hash (base library hash for slot 0)
// - The previous upgrade slot (MaxSlot for slot 0)
//
// Parameters:
// - muts: the mutations to inject into
// - stateReader: reader for the baseline state to check existing UTXOs
// - branchSlot: the slot of the branch being committed
//
// Returns information about the upgrade UTXOs injected.
func InjectMissingUpgradeUTXOs(muts *Mutations, stateReader IndexedStateReader, branchSlot uint32) []InjectedUpgrade {
	// Fast path: check if there's any pending upgrade that might need injection
	if !ledger.HasPendingUpgradeForSlot(branchSlot) {
		return nil
	}

	upgradeSlots := ledger.GetAllUpgradeSlots(branchSlot)
	if len(upgradeSlots) == 0 {
		return nil
	}

	var injected []InjectedUpgrade
	for i, upgradeSlot := range upgradeSlots {
		// Generate the synthetic OutputID for this upgrade
		oid := base.UpgradeOutputID(upgradeSlot)

		// Check if it already exists in the baseline state
		if stateReader.HasUTXO(oid) {
			continue
		}

		// Get the library hash for this upgrade slot
		lib := ledger.L(upgradeSlot)
		libraryHash := lib.LibraryHash()

		// Determine previous library hash and slot
		var prevLibraryHash [32]byte
		var prevUpgradeSlot uint32

		if upgradeSlot == 0 {
			// Slot 0: previous is the base EasyFL library
			prevLibraryHash = ledger.BaseLibraryHash()
			prevUpgradeSlot = base.MaxSlot
		} else {
			// Find the previous upgrade slot (must exist in the sorted list)
			// Since upgradeSlots is sorted in ascending order, the previous is at index i-1
			if i > 0 {
				prevUpgradeSlot = upgradeSlots[i-1]
			} else {
				// This upgrade slot is > 0 but it's the first in our list
				// This means slot 0 already exists in state, get its hash
				prevUpgradeSlot = 0
			}
			prevLib := ledger.L(prevUpgradeSlot)
			prevLibraryHash = prevLib.LibraryHash()
		}

		// Create the upgrade UTXO with chain link
		upgradeUTXO := ledger.UpgradeUTXO(upgradeSlot, libraryHash, prevLibraryHash, prevUpgradeSlot)

		// Inject into mutations using raw clone (upgrade UTXOs don't have standard locks)
		muts.InsertAddOutputMutationRaw(upgradeUTXO.ID, upgradeUTXO.Output)

		// Record the injected upgrade info
		injected = append(injected, InjectedUpgrade{
			Slot:        upgradeSlot,
			LibraryHash: libraryHash,
			LibraryYAML: lib.ToYAML(false),
		})
	}

	// If we injected any UTXOs, update the pending upgrade tracking
	if len(injected) > 0 {
		ledger.UpdateNextPendingUpgradeSlot(branchSlot)
	}

	return injected
}
