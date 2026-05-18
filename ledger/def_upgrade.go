package ledger

// UpgradeDefinition defines a pending library upgrade.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	Slot uint32

	// Build takes the previous library JSON blob and returns the upgraded library JSON blob.
	Build func(prevJSON []byte) ([]byte, error)
}

// PendingUpgrade is the current pending upgrade, or nil if no upgrade is pending.
// At most one pending upgrade can exist at a time.
var PendingUpgrade *UpgradeDefinition = nil
