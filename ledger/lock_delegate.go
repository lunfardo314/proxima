package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"slices"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type (
	DelegateLock struct {
		Target                 ChainLock
		MasterID               base.SpenderID
		MaxFrozenEpochs        byte
		RequiredInflationShare uint16 // in promille, <= 1000
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		State           byte
	}
)

const (
	DelegateLockName       = "delegateLock"
	DelegateLockTemplate   = DelegateLockName + "(%s, 0x%s, %s, z16/%d)"
	DelegateLockTemplateHR = DelegateLockName + "(target=%s, master=%s, maxFreezeEpochs=%d, inflationShare=%d%%)"

	DelegateLockStateName       = "delegateLockState"
	DelegateLockStateTemplate   = DelegateLockStateName + "(z32/%d, %d)"
	DelegateLockStateTemplateHR = DelegateLockStateName + "(frozenUntilEpoch=%d, state=%s)"

	DelegateLockStateUndef  = byte(0)
	DelegateLockStateFrozen = byte(1)
	DelegateLockStateOnHold = byte(2)

	// 3rd unlock byte in the delegation output unlock parameters

	DelegationUnlockedByTarget = byte(0)
	DelegationUnlockedByMaster = byte(0xff)
)

//go:embed def/lock_delegate.easyfl
var delegateLockSource string

//------------ DelegateLock

// TODO take target ChainID as parameter, not ChainLock

func NewDelegateLock(target ChainLock, masterID base.SpenderID, maxFrozenEpochs byte, requiredInflationShare uint16) *DelegateLock {
	return &DelegateLock{
		Target:                 target,
		MasterID:               masterID,
		MaxFrozenEpochs:        maxFrozenEpochs,
		RequiredInflationShare: requiredInflationShare,
	}
}

func (d *DelegateLock) Source() string {
	m := "0x"
	if d.MaxFrozenEpochs != 0 && d.MaxFrozenEpochs != byte(L(base.MaxSlot).MaxFrozenEpochs) {
		m = fmt.Sprintf("%d", d.MaxFrozenEpochs)
	}
	return fmt.Sprintf(DelegateLockTemplate, d.Target.Source(), hex.EncodeToString(d.MasterID[:]), m, d.RequiredInflationShare)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), hex.EncodeToString(d.MasterID[:]), d.MaxFrozenEpochs, d.RequiredInflationShare)
}

func (d *DelegateLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateLock) Controllers() []Controller {
	if EqualControllers(d.Target, SigLock(d.MasterID)) {
		return []Controller{d.Target}
	}
	return []Controller{d.Target, SigLock(d.MasterID)}
}

func DelegateLockFromBytesWithLib(data []byte, lib *Library) (*DelegateLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: %w", err)
	}
	if sym != DelegateLockName {
		return nil, fmt.Errorf("DelegateLockFromBytes: not a DelegateLock")
	}
	ret := &DelegateLock{}

	// target lock
	ret.Target, err = ChainLockFromBytesWithLib(args[0], lib)
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: %w", err)
	}
	// master spender ID (raw 32 bytes)
	masterIDbin := easyfl.StripDataPrefix(args[1])
	if len(masterIDbin) != len(base.SpenderID{}) {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong master ID size")
	}
	copy(ret.MasterID[:], masterIDbin)

	// max frozen epochs
	a2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil || a2 >= 256 {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong max frozen epochs: %v", err)
	}
	ret.MaxFrozenEpochs = byte(a2)
	if ret.MaxFrozenEpochs == 0 {
		// set default from the library for this slot
		ret.MaxFrozenEpochs = byte(lib.MaxFrozenEpochs)
	}

	// required inflation share
	ret.RequiredInflationShare, err = easyfl_util.Uint16FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong required inflation share: %v", err)
	}

	return ret, nil
}

func (d *DelegateLock) Name() string {
	return DelegateLockName
}

func (d *DelegateLock) Master() Controller {
	return SigLock(d.MasterID)
}

func registerDelegateLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateLockName, 4, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return DelegateLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterLockSerde(DelegateLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := DelegateLockFromBytesWithLib(bytes, lib)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(DelegateLockStateName, 2, func(data []byte) (Constraint, error) {
		return DelegateLockStateFromBytesWithLib(data, lib)
	})
}

//--------------------------- delegationLockState

func DelegateLockStateFromBytesWithLib(data []byte, lib *Library) (DelegateLockState, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: %w", err)
	}
	if sym != DelegateLockStateName {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: not a DelegateLockState")
	}
	fr, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: wrong argument 0: %w", err)
	}
	state := easyfl.StripDataPrefix(args[1])
	if len(state) != 1 {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: argument 1 must be one byte")
	}
	return DelegateLockState{
		LastFrozenEpoch: fr,
		State:           state[0],
	}, nil
}

func (d DelegateLockState) Source() string {
	return fmt.Sprintf(DelegateLockStateTemplate, d.LastFrozenEpoch, d.State)
}

func (d DelegateLockState) String() string {
	s := "undef"
	switch d.State {
	case DelegateLockStateFrozen:
		s = "frozen"
	case DelegateLockStateOnHold:
		s = "on hold"
	}
	return fmt.Sprintf(DelegateLockStateTemplateHR, d.LastFrozenEpoch, s)
}

func (d DelegateLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLockState) Name() string {
	return DelegateLockStateName
}

func init() {
	registerInlineTest(func(lib *Library) {
		target := ChainLockFromChainID(base.RandomChainID())
		masterID := base.SpenderID(SigLockRandom())
		example := NewDelegateLock(target, masterID, 3, 10)

		exampleBack, err := DelegateLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(example.MaxFrozenEpochs == 3, "DelegateLockFromBytes: wrong back 1")
		util.Assertf(exampleBack.MaxFrozenEpochs == example.MaxFrozenEpochs, "DelegateLockFromBytes: wrong back 2")
		util.Assertf(exampleBack.RequiredInflationShare == example.RequiredInflationShare, "DelegateLockFromBytes: wrong back 3")
		util.Assertf(example.RequiredInflationShare == 10, "DelegateLockFromBytes: wrong back 4")

		util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateLockName)
		exampleBack2, err := LockFromBytes(example.Bytes())
		util.AssertNoError(err)
		util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+DelegateLockName)

		pref1, err := lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)

		pref2, err := lib.EvalFromSource(nil, "#"+DelegateLockName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
		util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
	})

	registerInlineTest(func(lib *Library) {
		dlz := DelegateLockState{3001, DelegateLockStateFrozen}

		dlzBack, err := DelegateLockStateFromBytesWithLib(dlz.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(dlzBack.LastFrozenEpoch == 3001, "DelegateLockState: inconsistency 1")
		util.Assertf(dlzBack.State == DelegateLockStateFrozen, "DelegateLockState: inconsistency 2")
		util.Assertf(dlz == dlzBack, "DelegateLockState: inconsistency 3")
	})
}

// evalEnforceFrozenCoverageOnDelegateOutput is embedded EasyFL function that enforces correct frozen coverage values
func evalEnforceFrozenCoverageOnDelegateOutput(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	ctx := par.DataContext()
	par.Require(ctx.SelfIsProducedOutput(), "evalEnforceFrozenCoverageOnDelegateOutput: produced output expected")
	o := ctx.SelfOutput()

	amounts := o.Amounts()
	cc, idx := o.ChainConstraint()
	par.Require(idx == 2, "evalEnforceFrozenCoverageOnDelegateOutput: chain constraint is expected at index 2")

	lib := ctx.GetLibrary()
	// produced output
	if cc.IsOrigin() {
		par.Require(o.Inflation() == 0 && amounts.IsFrozenCoverageZero(byte(lib.MaxFrozenEpochs)),
			"evalEnforceFrozenCoverageOnDelegateOutput: inflation and frozen coverage must be 0 on a non-chain output and on chain origin")
		return []byte{0xff}
	}
	// it is a non-origin chained output

	succID := ctx.OutputID(path[len(path)-2])

	dOut, ok := AsDelegationOutput(o, succID)
	par.Require(ok, "evalEnforceFrozenCoverageOnDelegateOutput: inconsistency, delegation output expectedVector 1")

	pred, err := ctx.ConsumedOutput(dOut.PredecessorInputIndex)
	par.RequireNoError(err)

	if pred.Lock().Name() != DelegateLockName {
		// predecessor is not delegation -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(byte(lib.MaxFrozenEpochs)),
			"evalEnforceFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to the reason: chain predecessor is not a delegation")
		return []byte{0xff}
	}
	// predecessor is delegation
	// unlock parameters of predecessor delegation lock must be 3 bytes
	unlock, err := ctx.UnlockParameters(dOut.PredecessorInputIndex, ConstraintIndexLock)
	par.RequireNoError(err)
	par.Require(len(unlock) >= 3, "evalEnforceFrozenCoverageOnDelegateOutput: unlock parameters of predecessor delegation lock at (%d, %d) must be 3 bytes",
		dOut.PredecessorInputIndex, ConstraintIndexLock)

	if unlock[2] == DelegationUnlockedByMaster {
		// predecessor is delegation unlocked by master  -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(byte(lib.MaxFrozenEpochs)),
			"evalEnforceFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to the reason: predecessor is unlocked by the master")
		return []byte{0xff}
	}

	// unlocked by the target as enforced by the delegation lock
	var expectedVector []int64
	// the expected vector is different for frozen and revoked delegation outputs
	if dOut.State == DelegateLockStateOnHold {
		dOutPred, ok := AsDelegationOutput(pred, ctx.MustInputAt(dOut.PredecessorInputIndex))
		par.Require(ok, "evalEnforceFrozenCoverageOnDelegateOutput: delegation output expectedVector at predecessor")

		// the expected vector contains negative deltas of revoked frozen coverage in the current transaction (adjusted to the epoch difference)
		expectedVector = dOutPred.MakeFrozenCoverageAmountDeltasForRevoking(ctx.Timestamp())
	} else {
		_, _, frozenEpochs := dOut.FrozenEpochs(ctx.Timestamp())
		par.Require(frozenEpochs <= 256, "inconsistency: frozenEpochs <= 256")
		// the expected vector contains frozen coverages for the span of the frozen epochs
		expectedVector, err = dOut.MakeFrozenCoverageAmounts(ctx.Timestamp(), byte(frozenEpochs), dOut.Output.TokenBalance())
		par.RequireNoError(err)
	}

	vectorToCheck := o.Amounts().FrozenCoverageVector(byte(lib.MaxFrozenEpochs))
	par.Require(len(expectedVector) == len(vectorToCheck), "len(expectedVector) == len(vectorToCheck)")
	par.Require(slices.Equal(expectedVector, vectorToCheck), "evalEnforceFrozenCoverageOnDelegateOutput: wrong frozen coverage value in delegation output: %s", dOut.ChainID.String)

	return []byte{0xff}
}
