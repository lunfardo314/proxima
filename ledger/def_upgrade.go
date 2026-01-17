package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

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

// upgradeLibrary applies YAML definitions to a library using the unified resolver.
func upgradeLibrary(lib *easyfl.Library[*EvalContext], yamlList ...[]byte) error {
	resolver := GetEmbeddedFunctionResolver(lib)

	for _, yaml := range yamlList {
		if err := lib.UpgradeFromYAML(yaml, resolver); err != nil {
			return err
		}
	}
	return nil
}

func upgrade0(lib *easyfl.Library[*EvalContext], par InitParameters) {
	err := upgradeLibrary(lib,
		[]byte(_definitionsEmbeddedYAMLUpgrade0),
		ConstantsYAMLFromParamsUpgrade0(par),
		[]byte(pathConstantsUpgrade0()),
		[]byte(_helperFunctionsYAMLUpgrade0),
		[]byte(_generalFunctionsYAMLUpgrade0),
	)
	util.AssertNoError(err)

	lib.MustExtendMany(amountsAuxSource)
	lib.MustExtendMany(addressED25519ConstraintSource)
	//lib.MustExtendMany(conditionalLockSource) // not very necessary
	//lib.MustExtendMany(deadlineLockSource)    // not very necessary
	lib.MustExtendMany(timelockSource)
	lib.MustExtendMany(stemLockSource)
	lib.MustExtendMany(chainConstraintSource)
	lib.MustExtendMany(sequencerConstraintSource)
	lib.MustExtendMany(chainLockConstraintSource)
	//lib.MustExtendMany(commitToSiblingSource) // not very necessary
	lib.MustExtendMany(delegateLock2Source)
	lib.MustExtendMany(tagAlongLockConstraintSource)
	lib.MustExtendMany(ensureStopFreezeDelegationConstraintSource)
}

// registerConstraints mass-registers all wrappers of constraints
func (lib *Library) registerConstraints() {
	registerAmountsConstraint(lib)
	registerAddressED25519Serde(lib)
	registerTimeLockConstraint(lib)
	registerStemLockConstraint(lib)
	registerChainConstraint(lib)
	registerSequencerConstraint(lib)
	registerChainLockConstraint(lib)
	registerDelegateLock(lib)
	registerTagAlongLockConstraint(lib)
	registerEnsureConstraints(lib)

	registerInlineTest(func(lib *Library) {
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
