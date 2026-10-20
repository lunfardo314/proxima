package ledger

// UpgradeDefinition defines a library upgrade.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	Slot uint32

	// Description is shown in the node log with the upgrade, nowhere else. May be empty.
	Description string

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
	{
		Slot:        29000,
		Description: "mining covenant upgrade: transit only from tick 83",
		Build:       BuildUpgrade1Library,
	},
}

// UpgradeDescription returns the description of the upgrade listed at slot, or "".
func UpgradeDescription(slot uint32) string {
	for _, upg := range Upgrades {
		if upg.Slot == slot {
			return upg.Description
		}
	}
	return ""
}
