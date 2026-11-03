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

// Upgrades lists the library upgrades still to be activated, in increasing slot order. A node
// builds and stores, at start, each one its state does not hold yet. An upgrade is added here
// when its activation slot is decided, and removed in a release after it is active: from then
// on the library comes with the state database and with snapshots, and the state's upgrade
// UTXO commits to it (multistate.InitLedgerFromStore). The builder of a removed upgrade stays
// in the code. See ledger/upgrade.md.
//
// Activated: upgrade 1 at slot 29000 (2026-10-21 17:01:39 UTC), BuildUpgrade1Library;
// upgrade 2 at slot 45000 (2026-10-23 14:32:19 UTC), BuildUpgrade2Library;
// upgrade 3 at slot Upgrade3Slot (2026-10-25 09:12:19 UTC), BuildUpgrade3Library;
// upgrade 4 at slot Upgrade4Slot (2026-11-03 12:13:39 UTC), BuildUpgrade4Library.
var Upgrades = []*UpgradeDefinition{
	{
		Slot:        Upgrade5Slot,
		Description: "mine successor and sequencer key rules",
		Build:       BuildUpgrade5Library,
	},
}

// Upgrade5Slot is the activation slot of library upgrade 5: 2026-11-05 09:44:19 UTC.
const Upgrade5Slot = 153000

// Upgrade4Slot is the activation slot of library upgrade 4: 2026-11-03 12:13:39 UTC.
const Upgrade4Slot = 137000

// Upgrade3Slot is the activation slot of library upgrade 3: 2026-10-25 09:12:19 UTC.
const Upgrade3Slot = 60000

// UpgradeDescription returns the description of the upgrade listed at slot, or "".
func UpgradeDescription(slot uint32) string {
	for _, upg := range Upgrades {
		if upg.Slot == slot {
			return upg.Description
		}
	}
	return ""
}
