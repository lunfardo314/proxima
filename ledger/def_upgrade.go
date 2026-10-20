package ledger

// UpgradeDefinition defines a library upgrade.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	Slot uint32

	// Build takes the previous library JSON blob and returns the upgraded library JSON blob.
	Build func(prevJSON []byte) ([]byte, error)
}

// Upgrades lists every library upgrade after genesis, in increasing slot order. A node
// builds and stores, at start, each one its state does not hold yet, so the binary
// carries the whole history: a node synced from genesis long after an upgrade
// rebuilds every library. An upgrade is added here when its activation slot is
// decided; see ledger/upgrade.md.
var Upgrades = []*UpgradeDefinition{
	// mine transits stamped no earlier than the settlement tick minus the sequencer pace,
	// miner version 5 (kb/library_upgrade1.md); slot 29000 starts 2026-10-21 17:01:39 UTC
	{Slot: 29000, Build: BuildUpgrade1Library},
}
