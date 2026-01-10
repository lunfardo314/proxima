package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type upgradeData struct {
	upgradeYAML              []byte
	embeddedFunctionResolver func(sym string) easyfl.EmbeddedFunction[*EvalContext]
}

func upgradeLibrary(lib *easyfl.Library[*EvalContext], upgradeData []upgradeData) error {
	var err error
	for _, upg := range upgradeData {
		if upg.embeddedFunctionResolver != nil {
			err = lib.UpgradeFromYAML(upg.upgradeYAML, upg.embeddedFunctionResolver)
		} else {
			err = lib.UpgradeFromYAML(upg.upgradeYAML)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func upgrade0(lib *easyfl.Library[*EvalContext], par InitParameters) {
	err := upgradeLibrary(lib, []upgradeData{
		{[]byte(_definitionsEmbeddedYAMLUpgrade0), GetEmbeddedFunctionResolverUpgrade0(lib)},
		{ConstantsYAMLFromParamsUpgrade0(par), nil},
		{[]byte(pathConstantsUpgrade0()), nil},
		{[]byte(_helperFunctionsYAMLUpgrade0), nil},
		{[]byte(_generalFunctionsYAMLUpgrade0), nil},
	})
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
		// inline tests
		libraryGlobal.MustEqual("timestampBytes(u32/255, 21)", base.T(255, 21).Hex())
		libraryGlobal.MustEqual("ticksBefore(timestampBytes(u32/100, 5), timestampBytes(u32/101, 10))", "u64/133")
		libraryGlobal.MustError("mustValidTimeSlot(255)", "wrong slot data")
		libraryGlobal.MustTrue("mustValidTimeSlot(u32/255)")
		libraryGlobal.MustEqual("mustValidTimeTick(88)", "88")
		libraryGlobal.MustError("mustValidTimeTick(200)", "'wrong ticks value'")
		libraryGlobal.MustEqual("div(constInitialSupply, constSlotInflationBase)", "u64/30303030")
	})

}
