package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
)

// UpgradeDefinition defines a library upgrade.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	Slot uint32

	// Description is shown in the node log with the upgrade, nowhere else. May be empty.
	Description string

	// Build takes the previous library JSON blob and returns the upgraded library JSON blob.
	Build func(prevJSON []byte) ([]byte, error)

	// Fold, when set, replaces Build when the upgrade is folded into a genesis library: for an
	// upgrade whose definitions depend on the slot it is listed at.
	Fold func(prevJSON []byte) ([]byte, error)
}

// Upgrades lists every library upgrade of the network, in increasing slot order, and is never
// shortened: the last one is either still to be activated or the latest active one. Its order
// is the upgrade's number, so a genesis can fold the first n of them (FoldedUpgrades). A node
// builds and stores, at start, each upgrade its state does not hold yet, and checks the stored
// libraries against what this release builds (multistate.InitLedgerFromStore). See
// ledger/upgrade.md.
var Upgrades = []*UpgradeDefinition{
	{
		Slot:        Upgrade1Slot,
		Description: "mining covenant upgrade: transit only from tick 83",
		Build:       BuildUpgrade1Library,
	},
	{
		Slot:        Upgrade2Slot,
		Description: "post-branch consolidation: no sequencer transaction other than a branch before tick 9",
		Build:       BuildUpgrade2Library,
	},
	{
		Slot:        Upgrade3Slot,
		Description: "branch VRF message chained on the predecessor's VRF output; other fixes",
		Build:       BuildUpgrade3Library,
	},
	{
		Slot:        Upgrade4Slot,
		Description: "consensus improvements",
		Build:       BuildUpgrade4Library,
	},
	{
		Slot:        Upgrade5Slot,
		Description: "mine successor and sequencer key rules",
		Build:       BuildUpgrade5Library,
	},
	{
		Slot:        Upgrade6Slot,
		Description: "transaction-hash mining, the signer-bound transaction ID, sequencer pace 6 and minimum balances",
		Build:       BuildUpgrade6LibraryAt(Upgrade6Slot),
		Fold:        BuildUpgrade6LibraryAt(0),
	},
}

// Upgrade6Slot is the activation slot of library upgrade 6: 2026-11-10 12:02:59 UTC. Mining
// resumes Upgrade6MiningPauseSlots later, at slot 197000.
const Upgrade6Slot = 196000

// Upgrade5Slot is the activation slot of library upgrade 5: 2026-11-05 09:44:19 UTC.
const Upgrade5Slot = 153000

// Upgrade4Slot is the activation slot of library upgrade 4: 2026-11-03 12:13:39 UTC.
const Upgrade4Slot = 137000

// Upgrade3Slot is the activation slot of library upgrade 3: 2026-10-25 09:12:19 UTC.
const Upgrade3Slot = 60000

// Upgrade2Slot is the activation slot of library upgrade 2: 2026-10-23 14:32:19 UTC.
const Upgrade2Slot = 45000

// Upgrade1Slot is the activation slot of library upgrade 1: 2026-10-21 17:01:39 UTC.
const Upgrade1Slot = 29000

// foldedUpgradesConstant names the constant of a genesis library that folds upgrades: how many
// of the first upgrades of Upgrades it already carries. Absent from a genesis that folds none,
// such as the one of the take 1 network, whose upgrades all apply at their slots.
const foldedUpgradesConstant = "constFoldedUpgrades"

// FoldUpgrades builds the upgrades, in order, on a genesis library and records their number in
// the constant foldedUpgradesConstant. A new ledger so starts with the current definitions, and
// its nodes skip those upgrades. With no upgrades the library is returned unchanged.
func FoldUpgrades(genesisJSON []byte, upgrades []*UpgradeDefinition) ([]byte, error) {
	if len(upgrades) == 0 {
		return genesisJSON, nil
	}
	ret := genesisJSON
	var err error
	for i, upg := range upgrades {
		build := upg.Build
		if upg.Fold != nil {
			build = upg.Fold
		}
		if ret, err = build(ret); err != nil {
			return nil, fmt.Errorf("fold upgrade %d into the genesis library: %w", i+1, err)
		}
	}
	return buildUpgrade(ret, "folded upgrades", fmt.Sprintf("func %s : u64/%d", foldedUpgradesConstant, len(upgrades)))
}

// FoldedUpgrades is the number of the first upgrades of Upgrades a library carries from genesis.
func FoldedUpgrades(lib *easyfl.Library[*EvalContext]) int {
	n, err := _uint64FromConst(lib, foldedUpgradesConstant)
	if err != nil {
		return 0
	}
	return int(n)
}

// FoldedUpgrades is the number of the first upgrades of Upgrades the ledger carries from genesis.
func (lib *Library) FoldedUpgrades() int {
	return FoldedUpgrades(lib.Library)
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
