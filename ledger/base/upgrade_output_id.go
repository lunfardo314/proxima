package base

// This file defines synthetic OutputID format for upgrade commitment UTXOs.
// These special UTXOs commit to library upgrades at specific slots.
//
// Synthetic OutputID Format (33 bytes):
// - Bytes 0-3: upgrade slot (big-endian)
// - Byte 4: tick = 0, sequencer flag = 0
// - Byte 5: max output index = 0xff (255)
// - Bytes 6-31: upgrade slot as big-endian 26-byte number (zero-padded)
// - Byte 32: output index = 0xff (255)
//
// No-collision guarantee:
// 1. At slot 0 (genesis), only 2 outputs exist (indices 0 and 1), so index 255 is impossible
// 2. For non-genesis slots, the hash portion being the slot number (zero-padded) is
//    computationally infeasible to match with a real blake2b hash

import (
	"encoding/binary"
)

// SyntheticUpgradeOutputIndex is the output index used for synthetic upgrade UTXOs.
// This value (255) is reserved for synthetic outputs to avoid collision with real outputs.
const SyntheticUpgradeOutputIndex = byte(0xff)

// UpgradeOutputID creates a synthetic OutputID for an upgrade commitment UTXO.
// The OutputID uniquely identifies the upgrade at the given slot.
func UpgradeOutputID(upgradeSlot uint32) OutputID {
	var ret OutputID

	// Bytes 0-3: upgrade slot (big-endian)
	binary.BigEndian.PutUint32(ret[0:4], upgradeSlot)

	// Byte 4: tick = 0, sequencer flag = 0
	ret[4] = 0

	// Byte 5: max output index = 0xff (marks this as synthetic)
	ret[5] = SyntheticUpgradeOutputIndex

	// Bytes 6-31: upgrade slot as 26-byte big-endian number (zero-padded)
	// The slot occupies the last 4 bytes of this 26-byte region
	binary.BigEndian.PutUint32(ret[28:32], upgradeSlot)

	// Byte 32: output index = 0xff
	ret[32] = SyntheticUpgradeOutputIndex

	return ret
}

// IsUpgradeOutputID checks if an OutputID is a synthetic upgrade OutputID.
// It verifies:
// 1. Output index is 0xff
// 2. Max output index in txid is 0xff
// 3. Tick is 0 and sequencer flag is 0
// 4. Hash portion matches the expected pattern (slot number zero-padded)
func IsUpgradeOutputID(oid OutputID) bool {
	// Check output index
	if oid[32] != SyntheticUpgradeOutputIndex {
		return false
	}

	// Check max output index
	if oid[5] != SyntheticUpgradeOutputIndex {
		return false
	}

	// Check tick byte (should be 0 - no tick, no sequencer flag)
	if oid[4] != 0 {
		return false
	}

	// Check hash portion: bytes 6-27 should be zero (zero-padding)
	for i := 6; i < 28; i++ {
		if oid[i] != 0 {
			return false
		}
	}

	// Check that bytes 28-31 (slot in hash) match bytes 0-3 (slot in timestamp)
	if oid[0] != oid[28] || oid[1] != oid[29] || oid[2] != oid[30] || oid[3] != oid[31] {
		return false
	}

	return true
}

// UpgradeSlotFromOutputID extracts the upgrade slot from a synthetic upgrade OutputID.
// Returns the slot and true if valid, or 0 and false if not a valid upgrade OutputID.
func UpgradeSlotFromOutputID(oid OutputID) (uint32, bool) {
	if !IsUpgradeOutputID(oid) {
		return 0, false
	}
	return binary.BigEndian.Uint32(oid[0:4]), true
}
