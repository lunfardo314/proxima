package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// TODO cleanup of the ledger definitions: remove unused function defs and optimize
// TODO revisit function naming convention

// This file contains all upgrade prescriptions to the base ledger provided by the EasyFL. It is "version 0" of the ledger.
// Ledger definition can be upgraded by adding new embedded and extended function with new binary codes.
// That will make ledger upgrades backwards compatible, because all past transactions and EasyFL constraint bytecodes
// outputs will be interpreted exactly the same way

func LibraryFromParameters(idParams InitParameters, verbose ...bool) *Library {
	ret := newBaseLibrary()
	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Base EasyFL library:\n")
		ret.PrintLibraryStats()
	}

	upgrade0(ret.Library, idParams)

	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Extended EasyFL library:\n")
		ret.PrintLibraryStats()
	}
	return ret
}

func LibraryYAMLFromParameters(id InitParameters, compiled bool) []byte {
	return LibraryFromParameters(id).ToYAML(compiled, "# Proxima ledger definitions")
}

func ParseLibraryFromYAML(
	yamlData []byte,
	getResolver ...func(lib *easyfl.Library[*EvalContext],
	) func(sym string) easyfl.EmbeddedFunction[*EvalContext]) (*easyfl.Library[*EvalContext], error) {
	lib, err := easyfl.NewLibraryFromYAML(yamlData, getResolver...)
	if err != nil {
		return nil, err
	}
	return lib, nil
}

func upgrade0(lib *easyfl.Library[*EvalContext], par InitParameters) {
	err := EmbedHardcoded(lib)
	util.AssertNoError(err)

	// add main ledger constants
	err = lib.UpgradeFromYAML(ConstantsYAMLFromParams(par))
	util.AssertNoError(err)

	// add path constants
	err = lib.UpgradeFromYAML([]byte(pathConstants()))
	util.AssertNoError(err)

	// add base helpers
	err = lib.UpgradeFromYAML([]byte(_helperFunctionsYAML))
	util.AssertNoError(err)

	// add general functions
	err = lib.UpgradeFromYAML([]byte(_generalFunctionsYAML))
	util.AssertNoError(err)

	lib.MustExtendMany(amountsAuxSource)
	lib.MustExtendMany(addressED25519ConstraintSource)
	lib.MustExtendMany(conditionalLockSource)
	lib.MustExtendMany(deadlineLockSource)
	lib.MustExtendMany(timelockSource)
	lib.MustExtendMany(stemLockSource)
	lib.MustExtendMany(chainConstraintSource)
	lib.MustExtendMany(sequencerConstraintSource)
	lib.MustExtendMany(chainLockConstraintSource)
	lib.MustExtendMany(commitToSiblingSource)
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
