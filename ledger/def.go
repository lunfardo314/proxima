package ledger

import (
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// TODO cleanup of the ledger definitions: remove unused function defs and optimize
// TODO revisit function naming convention

// This file contains all upgrade prescriptions to the base ledger provided by the EasyFL. It is "version 0" of the ledger.
// Ledger definition can be upgraded by adding new embedded and extended function with new binary codes.
// That will make ledger upgrades backwards compatible, because all past transactions and EasyFL constraint bytecodes
// outputs will be interpreted exactly the same way

func LibraryFromIdentityParameters(idParams *IdentityParameters, verbose ...bool) *Library {
	ret := newBaseLibrary(idParams)
	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Base EasyFL library:\n")
		ret.PrintLibraryStats()
	}

	upgrade0(ret.Library, idParams)

	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Extended EasyFL library:\n")
		ret.PrintLibraryStats()
	}
	ret.ID = idParams
	return ret
}

func LibraryYAMLFromIdentityParameters(id *IdentityParameters, compiled bool) []byte {
	return LibraryFromIdentityParameters(id).ToYAML(compiled, "# Proxima ledger definitions")
}

func ParseLedgerIdYAML(
	yamlData []byte,
	getResolver ...func(lib *easyfl.Library[*EvalContext],
	) func(sym string) easyfl.EmbeddedFunction[*EvalContext]) (*easyfl.Library[*EvalContext], *IdentityParameters, error) {
	lib, err := easyfl.NewLibraryFromYAML(yamlData, getResolver...)
	if err != nil {
		return nil, nil, err
	}
	idParams, err := IDParametersFromLibrary(lib)
	if err != nil {
		return nil, nil, err
	}
	return lib, idParams, nil
}

func _uint64FromConst(lib *easyfl.Library[*EvalContext], constName string) (uint64, error) {
	res, err := lib.EvalFromSource(nil, constName)
	if err != nil {
		return 0, err
	}
	return easyfl_util.Uint64FromBytes(res)
}

func IDParametersFromLibrary(lib *easyfl.Library[*EvalContext]) (*IdentityParameters, error) {
	ret := &IdentityParameters{}
	var err error
	var res []byte
	if ret.InitialSupply, err = _uint64FromConst(lib, "constInitialSupply"); err != nil {
		return nil, err
	}
	if res, err = lib.EvalFromSource(nil, "constGenesisControllerPublicKey"); err != nil {
		return nil, err
	}
	ret.GenesisControllerPublicKey = res
	if gt, err := _uint64FromConst(lib, "constGenesisTimeUnix"); err != nil {
		return nil, err
	} else {
		ret.GenesisTimeUnix = uint32(gt)
	}
	if td, err := _uint64FromConst(lib, "constTickDuration"); err != nil {
		return nil, err
	} else {
		ret.TickDuration = time.Duration(td)
	}
	if ret.SlotInflationBase, err = _uint64FromConst(lib, "constSlotInflationBase"); err != nil {
		return nil, err
	}
	if ret.LinearInflationSlots, err = _uint64FromConst(lib, "constLinearInflationSlots"); err != nil {
		return nil, err
	}
	if ret.BranchInflationBonusBase, err = _uint64FromConst(lib, "constBranchInflationBonusBase"); err != nil {
		return nil, err
	}
	if ret.MinimumAmountOnSequencer, err = _uint64FromConst(lib, "constMinimumAmountOnSequencer"); err != nil {
		return nil, err
	}
	if ret.MaxNumberOfEndorsements, err = _uint64FromConst(lib, "constMaxNumberOfEndorsements"); err != nil {
		return nil, err
	}
	if pb, err := _uint64FromConst(lib, "constPreBranchConsolidationTicks"); err != nil {
		return nil, err
	} else {
		if pb > 255 {
			return nil, fmt.Errorf("invalid pre branch consolidation ticks")
		}
		ret.PreBranchConsolidationTicks = byte(pb)
	}
	if pb, err := _uint64FromConst(lib, "constPostBranchConsolidationTicks"); err != nil {
		return nil, err
	} else {
		if pb > 255 {
			return nil, fmt.Errorf("invalid post branch consolidation ticks")
		}
		ret.PostBranchConsolidationTicks = byte(pb)
	}
	if tp, err := _uint64FromConst(lib, "constTransactionPace"); err != nil {
		return nil, err
	} else {
		if tp > 255 {
			return nil, fmt.Errorf("invalid transaction pace")
		}
		ret.TransactionPace = byte(tp)
	}
	if tp, err := _uint64FromConst(lib, "constTransactionPaceSequencer"); err != nil {
		return nil, err
	} else {
		if tp > 255 {
			return nil, fmt.Errorf("invalid sequencer transaction pace")
		}
		ret.TransactionPaceSequencer = byte(tp)
	}
	if ret.VBCost, err = _uint64FromConst(lib, "constVBCost16"); err != nil {
		return nil, err
	}
	if res, err = lib.EvalFromSource(nil, "constDescription"); err != nil {
		return nil, err
	}
	ret.Description = string(res)
	return ret, nil
}

func upgrade0(lib *easyfl.Library[*EvalContext], id *IdentityParameters) {
	err := EmbedHardcoded(lib)
	util.AssertNoError(err)

	// add main ledger constants
	err = lib.UpgradeFromYAML(ConstantsYAMLFromIdentity(id))
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

	lib.MustExtendMany(_inflationFunctionsSource)

	lib.MustExtendMany(amountsAuxSource)
	lib.MustExtendMany(addressED25519ConstraintSource)
	lib.MustExtendMany(conditionalLockSource)
	lib.MustExtendMany(deadlineLockSource)
	lib.MustExtendMany(timelockSource)
	lib.MustExtendMany(stemLockSource)
	lib.MustExtendMany(chainConstraintSource)
	lib.MustExtendMany(sequencerConstraintSource)
	lib.MustExtendMany(inflationConstraintSource)
	lib.MustExtendMany(messageWithED25519SenderSource)
	lib.MustExtendMany(chainLockConstraintSource)
	lib.MustExtendMany(immutableDataConstraintSource)
	lib.MustExtendMany(commitToSiblingSource)
	lib.MustExtendMany(delegationLockSource)
	lib.MustExtendMany(totalAmountSource)
	lib.MustExtendMany(delegateLock2Source)
}

// registerConstraints mass-registers all wrappers of constraints
func (lib *Library) registerConstraints() {
	registerAmountsConstraint(lib)
	registerAddressED25519Constraint(lib)
	registerConditionalLock(lib)
	registerDeadlineLockConstraint(lib)
	registerTimeLockConstraint(lib)
	registerStemLockConstraint(lib)
	registerChainConstraint(lib)
	registerSequencerConstraint(lib)
	//registerInflationConstraint(lib)
	registerMessageWithSenderED25519Constraint(lib)
	registerChainLockConstraint(lib)
	registerImmutableConstraint(lib)
	registerCommitToSiblingConstraint(lib)
	registerDelegationLock(lib)
	registerTotalAmountConstraint(lib)
	registerDelegate2Lock(lib)

	lib.appendInlineTests(func() {
		// inline tests
		libraryGlobal.MustEqual("timestampBytes(u32/255, 21)", base.NewLedgerTime(255, 21).Hex())
		libraryGlobal.MustEqual("ticksBefore(timestampBytes(u32/100, 5), timestampBytes(u32/101, 10))", "u64/133")
		libraryGlobal.MustError("mustValidTimeSlot(255)", "wrong slot data")
		libraryGlobal.MustEqual("mustValidTimeSlot(u32/255)", base.Slot(255).Hex())
		libraryGlobal.MustEqual("mustValidTimeTick(88)", "88")
		libraryGlobal.MustError("mustValidTimeTick(200)", "'wrong ticks value'")
		libraryGlobal.MustEqual("div(constInitialSupply, constSlotInflationBase)", "u64/30303030")
	})

}
