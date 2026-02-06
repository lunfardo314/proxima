package ledger

import (
	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

var (
	//go:embed def/def_embed0.yaml
	_definitionsEmbeddedYAMLUpgrade0 string
	//go:embed def/def_general_func0.yaml
	_generalFunctionsYAMLUpgrade0 string
	//go:embed def/def_helper_func0.yaml
	_helperFunctionsYAMLUpgrade0 string
	//go:embed def/tx_integrity_validator.easyfl
	_txLayoutValidator0 string
)

// upgrade0 makes library at genesis by applying upgrade to the base EasyFL library
func upgrade0(lib *easyfl.Library[*EvalContext], par InitParameters) {
	err := upgradeLibrary(lib,
		[]byte(_definitionsEmbeddedYAMLUpgrade0),
		ConstantsYAMLFromParamsUpgrade0(par),
		[]byte(pathConstantsUpgrade0()),
		[]byte(_helperFunctionsYAMLUpgrade0),
		[]byte(_generalFunctionsYAMLUpgrade0),
	)
	util.AssertNoError(err)

	lib.MustExtendMany(addressED25519ConstraintSource)
	lib.MustExtendMany(timelockSource)
	lib.MustExtendMany(amountsSource)
	lib.MustExtendMany(stemLockSource)
	lib.MustExtendMany(chainConstraintSource)
	lib.MustExtendMany(sequencerConstraintSource)
	lib.MustExtendMany(chainLockConstraintSource)
	lib.MustExtendMany(delegateLock2Source)
	lib.MustExtendMany(tagAlongLockConstraintSource)
	lib.MustExtendMany(ensureStopFreezeDelegationConstraintSource)
	lib.MustExtendMany(_txLayoutValidator0)

}

// registerConstraints0 mass-registers all serde wrappers of constraints at genesis
// This function must be used in upgrades along new constraint registration
func registerConstraints0(lib *Library) {
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
