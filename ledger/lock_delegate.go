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
		Target               base.ChainID
		MasterID             base.HolderID
		RequiredInflationCut uint16 // in promille, <= 1000
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		State           byte
		// AdvanceShare is the promille of the projected inflation the target
		// actually advanced when it froze this delegation. Pinned here at
		// freeze time because neither the freeze slot nor the pre-freeze
		// balance survives on the output, so an early stop cannot otherwise
		// work out how much of the advance is unearned.
		AdvanceShare uint16
	}
)

const (
	DelegateLockName = "delegateLock"
	// 1 arg at output element index 2: inflationCut. The epoch grid and the
	// freeze depth are ledger constants. Target chainID and master holder ID
	// live in the index-value tuple at output element index 1 (positions 1
	// and 0 respectively).
	DelegateLockTemplate   = DelegateLockName + "(z16/%d)"
	DelegateLockTemplateHR = DelegateLockName + "(targetChainID=%s, master=%s, inflationCut=%d%%)"

	DelegateLockStateName       = "delegateLockState"
	DelegateLockStateTemplate   = DelegateLockStateName + "(z32/%d, %d, z16/%d)"
	DelegateLockStateTemplateHR = DelegateLockStateName + "(frozenUntilEpoch=%d, state=%s, advanceShare=%d)"

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

func NewDelegateLock(targetChainID base.ChainID, masterID base.HolderID, requiredInflationCut uint16) *DelegateLock {
	return &DelegateLock{
		Target:               targetChainID,
		MasterID:             masterID,
		RequiredInflationCut: requiredInflationCut,
	}
}

// Source returns the EasyFL source representation of the 1-arg
// delegateLock constraint that goes at output element index 2. Only the
// inflation cut lives in the bytecode; target chain and master holder live
// in the index-value tuple at index 1.
func (d *DelegateLock) Source() string {
	return fmt.Sprintf(DelegateLockTemplate, d.RequiredInflationCut)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), hex.EncodeToString(d.MasterID[:]),
		d.RequiredInflationCut)
}

// Bytes returns the compiled bytecode of the 1-arg delegateLock
// constraint at output element index 2.
func (d *DelegateLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateLock) Name() string {
	return DelegateLockName
}

// IndexValues returns [masterID, targetChainID]. Master is at position 0
// per the §4.1 master-first convention.
func (d *DelegateLock) IndexValues() [][]byte {
	return [][]byte{d.MasterID[:], d.Target[:]}
}

// LockBytecode returns the compiled 4-arg delegateLock bytecode at
// output element index 2.
func (d *DelegateLock) LockBytecode() []byte {
	return d.Bytes()
}

// DelegateLockFromBytesWithLib parses the 4-arg delegateLock bytecode at
// output element index 2. Returns a partially-filled DelegateLock with
// MaxFrozenEpochs, RequiredInflationCut, EpochSlots and
// TargetMaxFrozenEpochs set; the caller must fill Target / MasterID from
// the output's index-value tuple at index 1.
func DelegateLockFromBytesWithLib(data []byte, lib *Library) (*DelegateLock, error) {
	sym, _, args, err := lib.Library.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: %w", err)
	}
	if sym != DelegateLockName {
		return nil, fmt.Errorf("DelegateLockFromBytes: not a DelegateLock")
	}
	ret := &DelegateLock{}

	// arg 0: required inflation cut
	ret.RequiredInflationCut, err = easyfl_util.Uint16FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong required inflation cut: %v", err)
	}

	return ret, nil
}

// DelegateLockFromOutputElements rebuilds a complete DelegateLock from
// both output elements:
//   - indexValuesBytes — bytes at output element index 1; expected
//     (master, target) pair.
//   - lockBytecode     — bytes at output element index 2; carries the
//     2-arg delegateLock (maxFrozenEpochs, inflationCut).
func DelegateLockFromOutputElements(indexValuesBytes, lockBytecode []byte, lib *Library) (*DelegateLock, error) {
	ret, err := DelegateLockFromBytesWithLib(lockBytecode, lib)
	if err != nil {
		return nil, err
	}
	values, err := IndexValuesFromBytes(indexValuesBytes)
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromOutputElements: %w", err)
	}
	if len(values) != 2 {
		return nil, fmt.Errorf("DelegateLockFromOutputElements: expected 2 index values, got %d", len(values))
	}
	if len(values[0]) != len(base.HolderID{}) {
		return nil, fmt.Errorf("DelegateLockFromOutputElements: wrong master ID size: %d", len(values[0]))
	}
	copy(ret.MasterID[:], values[0])
	if ret.Target, err = base.ChainIDFromBytes(values[1]); err != nil {
		return nil, fmt.Errorf("DelegateLockFromOutputElements: wrong target chain ID: %w", err)
	}
	return ret, nil
}

func registerDelegateLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateLockName, 1, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return DelegateLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterConstraint(DelegateLockStateName, 3, func(data []byte) (Constraint, error) {
		return DelegateLockStateFromBytesWithLib(data, lib)
	})
}

//--------------------------- delegationLockState

func DelegateLockStateFromBytesWithLib(data []byte, lib *Library) (DelegateLockState, error) {
	sym, _, args, err := lib.Library.ParseBytecodeOneLevel(data, 3)
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
	share, err := easyfl_util.Uint16FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: wrong argument 2: %w", err)
	}
	return DelegateLockState{
		LastFrozenEpoch: fr,
		State:           state[0],
		AdvanceShare:    share,
	}, nil
}

func (d DelegateLockState) Source() string {
	return fmt.Sprintf(DelegateLockStateTemplate, d.LastFrozenEpoch, d.State, d.AdvanceShare)
}

func (d DelegateLockState) String() string {
	s := "undef"
	switch d.State {
	case DelegateLockStateFrozen:
		s = "frozen"
	case DelegateLockStateOnHold:
		s = "on hold"
	}
	return fmt.Sprintf(DelegateLockStateTemplateHR, d.LastFrozenEpoch, s, d.AdvanceShare)
}

func (d DelegateLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLockState) Name() string {
	return DelegateLockStateName
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip the 1-arg delegateLock bytecode at output element
		// index 2. Target and master are not in the bytecode; they live
		// in the index-value tuple at index 1 and are exercised elsewhere
		// via DelegateLockFromOutputElements.
		targetChainID := base.RandomChainID()
		masterID := base.HolderID(SigLockRandom())
		example := NewDelegateLock(targetChainID, masterID, 10)

		exampleBack, err := DelegateLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(exampleBack.RequiredInflationCut == example.RequiredInflationCut, "DelegateLockFromBytes: wrong back 1")
		util.Assertf(example.RequiredInflationCut == 10, "DelegateLockFromBytes: wrong back 2")

		util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateLockName)

		pref1, err := lib.Library.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)

		pref2, err := lib.Library.EvalFromSource(nil, "#"+DelegateLockName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
		util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
	})

	registerInlineTest(func(lib *Library) {
		dlz := DelegateLockState{LastFrozenEpoch: 3001, State: DelegateLockStateFrozen, AdvanceShare: 900}

		dlzBack, err := DelegateLockStateFromBytesWithLib(dlz.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(dlzBack.LastFrozenEpoch == 3001, "DelegateLockState: inconsistency 1")
		util.Assertf(dlzBack.State == DelegateLockStateFrozen, "DelegateLockState: inconsistency 2")
		util.Assertf(dlzBack.AdvanceShare == 900, "DelegateLockState: inconsistency 3")
		util.Assertf(dlz == dlzBack, "DelegateLockState: inconsistency 4")
	})
}

// evalEnforceFrozenCoverageOnDelegateOutput is embedded EasyFL function that enforces correct frozen coverage values.
// Per Phase 3 of delegation_epoch_params, the vector size used for
// produced-side checks is sourced from the delegation's own
// TargetMaxFrozenEpochs (inlined into the lock at origin), not from
// the library default.
func evalEnforceFrozenCoverageOnDelegateOutput(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	ctx := par.DataContext()
	par.Require(ctx.SelfIsProducedOutput(), "evalEnforceFrozenCoverageOnDelegateOutput: produced output expected")
	o := ctx.SelfOutput()

	amounts := o.Amounts()
	cc := o.ChainConstraint()
	par.Require(cc != nil, "evalEnforceFrozenCoverageOnDelegateOutput: chain constraint is expected at index 2")

	succID := ctx.OutputID(path[len(path)-2])

	// Parse this output as a delegation so we can pull EpochSlots /
	// TargetMaxFrozenEpochs from its own lock body (works both at origin
	// and at transit; the lock is always parseable).
	dOut, ok := AsDelegationOutput(o, succID)
	par.Require(ok, "evalEnforceFrozenCoverageOnDelegateOutput: not a delegation output")
	mfe := dOut.TargetMaxFrozenEpochs()

	// produced output
	if cc.IsOrigin() {
		par.Require(o.Inflation() == 0 && amounts.IsFrozenCoverageZero(),
			"evalEnforceFrozenCoverageOnDelegateOutput: inflation and frozen coverage must be 0 on a non-chain output and on chain origin")
		return par.AllocData(0xff)
	}
	// it is a non-origin chained output

	pred, err := ctx.ConsumedOutput(dOut.PredecessorInputIndex)
	par.RequireNoError(err)

	if pred.Lock().Name() != DelegateLockName {
		// predecessor is not delegation -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(),
			"evalEnforceFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to the reason: chain predecessor is not a delegation")
		return []byte{0xff}
	}
	// predecessor is delegation
	// unlock parameters of predecessor delegation lock must be 2 bytes
	unlock, err := ctx.UnlockParameters(dOut.PredecessorInputIndex, ConstraintIndexLock)
	par.RequireNoError(err)
	par.Require(len(unlock) >= 2, "evalEnforceFrozenCoverageOnDelegateOutput: unlock parameters of predecessor delegation lock at (%d, %d) must be 2 bytes",
		dOut.PredecessorInputIndex, ConstraintIndexLock)

	if unlock[1] == DelegationUnlockedByMaster {
		// predecessor is delegation unlocked by master  -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(),
			"evalEnforceFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to the reason: predecessor is unlocked by the master")
		return par.AllocData(0xff)
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

	vectorToCheck := o.Amounts().FrozenCoverageVector(mfe)
	par.Require(len(expectedVector) == len(vectorToCheck), "len(expectedVector) == len(vectorToCheck)")
	par.Require(slices.Equal(expectedVector, vectorToCheck), "evalEnforceFrozenCoverageOnDelegateOutput: wrong frozen coverage value in delegation output: %s", dOut.ChainID.String)

	return par.AllocData(0xff)
}

