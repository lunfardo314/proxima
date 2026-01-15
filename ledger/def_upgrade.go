package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// UpgradeResolvers is the static list of all embedded function resolver factories
// for each upgrade slot. This map grows with each upgrade and entries are never removed.
//
// When an upgrade is added, its resolver factory is added here statically.
// At genesis, only upgrade0 is present. New entries are added with new upgrades.
//
// Note: Minimum slot distance between upgrades is enforced in multistate/upgrades.go
// (see multistate.MinSlotsBetweenUpgrades constant there).
var UpgradeResolvers map[uint32]ResolverFactory

func init() {
	UpgradeResolvers = map[uint32]ResolverFactory{
		0: GetEmbeddedFunctionResolverUpgrade0,
		// Future upgrades will be added here
	}
}

// UpgradeDefinition defines a pending library upgrade.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	Slot uint32

	// Build takes the previous library YAML and returns the upgraded library YAML.
	Build func(prevYAML []byte) ([]byte, error)
}

// PendingUpgrade is the current pending upgrade, or nil if no upgrade is pending.
// At most one pending upgrade can exist at a time.
var PendingUpgrade *UpgradeDefinition = nil

func upgradeLibrary(lib *easyfl.Library[*EvalContext], slot uint32, yamlList ...[]byte) error {
	resolverFactory := UpgradeResolvers[slot]
	util.Assertf(resolverFactory != nil, "no resolver in UpgradeResolvers for slot %d", slot)
	resolver := resolverFactory(lib)

	for _, yaml := range yamlList {
		if err := lib.UpgradeFromYAML(yaml, resolver); err != nil {
			return err
		}
	}
	return nil
}

func upgrade0(lib *easyfl.Library[*EvalContext], par InitParameters) {
	err := upgradeLibrary(lib, 0,
		[]byte(_definitionsEmbeddedYAMLUpgrade0),
		ConstantsYAMLFromParamsUpgrade0(par),
		[]byte(pathConstantsUpgrade0()),
		[]byte(_helperFunctionsYAMLUpgrade0),
		[]byte(_generalFunctionsYAMLUpgrade0),
	)
	util.AssertNoError(err)

	lib.MustExtendMany(amountsAuxSource)
	lib.MustExtendMany(addressED25519ConstraintSource)
	lib.MustExtendMany(conditionalLockSource) // TODO not very necessary
	lib.MustExtendMany(deadlineLockSource)    // TODO not very necessary
	lib.MustExtendMany(timelockSource)
	lib.MustExtendMany(stemLockSource)
	lib.MustExtendMany(chainConstraintSource)
	lib.MustExtendMany(sequencerConstraintSource)
	lib.MustExtendMany(chainLockConstraintSource)
	lib.MustExtendMany(commitToSiblingSource) // TODO not very necessary
	lib.MustExtendMany(delegateLock2Source)
	lib.MustExtendMany(tagAlongLockConstraintSource)
	lib.MustExtendMany(ensureStopFreezeDelegationConstraintSource)
}

// registerConstraints mass-registers all wrappers of constraints
func (lib *Library) registerConstraints() {
	registerAmountsConstraint(lib)
	registerAddressED25519Constraint(lib)
	registerTimeLockConstraint(lib)
	registerStemLockConstraint(lib)
	registerChainConstraint(lib)
	registerSequencerConstraint(lib)
	registerChainLockConstraint(lib)
	registerDelegateLock(lib)
	registerTagAlongLockConstraint(lib)
	registerEnsureConstraints(lib)

	lib.appendInlineTests(func() {
		// inline tests - use L(base.MaxSlot) to get the current library
		currentLib := L(base.MaxSlot)
		currentLib.MustEqual("timestampBytes(u32/255, 21)", base.T(255, 21).Hex())
		currentLib.MustEqual("ticksBefore(timestampBytes(u32/100, 5), timestampBytes(u32/101, 10))", "u64/133")
		currentLib.MustError("mustValidTimeSlot(255)", "wrong slot data")
		currentLib.MustTrue("mustValidTimeSlot(u32/255)")
		currentLib.MustEqual("mustValidTimeTick(88)", "88")
		currentLib.MustError("mustValidTimeTick(200)", "'wrong ticks value'")
		currentLib.MustEqual("div(constInitialSupply, constSlotInflationBase)", "u64/30303030")
	})

}
