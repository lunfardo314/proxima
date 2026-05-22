package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"slices"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type (
	DelegateLock struct {
		Target                 base.ChainID
		MasterID               base.HolderID
		MaxFrozenEpochs        byte
		RequiredInflationShare uint16 // in promille, <= 1000
		// EpochSlots and TargetMaxFrozenEpochs are copies of the target
		// chain's delegationParams, inlined at delegation origin and
		// pinned byte-equal across every transit. See
		// claude/delegation_epoch_params.md.
		EpochSlots            uint32
		TargetMaxFrozenEpochs byte
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		State           byte
	}
)

const (
	DelegateLockName = "delegateLock"
	// 4 args at output element index 2: maxFrozenEpochs, inflationShare,
	// epochSlots, targetMaxFrozenEpochs. Target chainID and master holder
	// ID live in the index-value tuple at output element index 1
	// (positions 1 and 0 respectively).
	DelegateLockTemplate   = DelegateLockName + "(%s, z16/%d, z32/%d, %d)"
	DelegateLockTemplateHR = DelegateLockName + "(targetChainID=%s, master=%s, maxFreezeEpochs=%d, inflationShare=%d%%, epochSlots=%d, targetMaxFrozenEpochs=%d)"

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

func NewDelegateLock(targetChainID base.ChainID, masterID base.HolderID, maxFrozenEpochs byte, requiredInflationShare uint16, epochSlots uint32, targetMaxFrozenEpochs byte) *DelegateLock {
	return &DelegateLock{
		Target:                 targetChainID,
		MasterID:               masterID,
		MaxFrozenEpochs:        maxFrozenEpochs,
		RequiredInflationShare: requiredInflationShare,
		EpochSlots:             epochSlots,
		TargetMaxFrozenEpochs:  targetMaxFrozenEpochs,
	}
}

// Source returns the EasyFL source representation of the 4-arg
// delegateLock constraint that goes at output element index 2. Only
// (maxFrozenEpochs, inflationShare, epochSlots, targetMaxFrozenEpochs)
// live in the bytecode; target chain and master holder live in the
// index-value tuple at index 1.
func (d *DelegateLock) Source() string {
	m := "0x"
	if d.MaxFrozenEpochs != 0 && d.MaxFrozenEpochs != d.TargetMaxFrozenEpochs {
		m = fmt.Sprintf("%d", d.MaxFrozenEpochs)
	}
	return fmt.Sprintf(DelegateLockTemplate, m, d.RequiredInflationShare, d.EpochSlots, d.TargetMaxFrozenEpochs)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), hex.EncodeToString(d.MasterID[:]),
		d.MaxFrozenEpochs, d.RequiredInflationShare, d.EpochSlots, d.TargetMaxFrozenEpochs)
}

// Bytes returns the compiled bytecode of the 4-arg delegateLock
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
// MaxFrozenEpochs, RequiredInflationShare, EpochSlots and
// TargetMaxFrozenEpochs set; the caller must fill Target / MasterID from
// the output's index-value tuple at index 1.
func DelegateLockFromBytesWithLib(data []byte, lib *Library) (*DelegateLock, error) {
	sym, _, args, err := lib.Library.ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: %w", err)
	}
	if sym != DelegateLockName {
		return nil, fmt.Errorf("DelegateLockFromBytes: not a DelegateLock")
	}
	ret := &DelegateLock{}

	// arg 0: max frozen epochs (delegator's chosen depth, 0 = use target's)
	a0, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil || a0 >= 256 {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong max frozen epochs: %v", err)
	}
	ret.MaxFrozenEpochs = byte(a0)

	// arg 1: required inflation share
	ret.RequiredInflationShare, err = easyfl_util.Uint16FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong required inflation share: %v", err)
	}

	// arg 2: epochSlots (copy of target's delegationParams.epochSlots)
	a2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil || a2 > math.MaxUint32 {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong epochSlots: %v", err)
	}
	ret.EpochSlots = uint32(a2)

	// arg 3: targetMaxFrozenEpochs (copy of target's delegationParams.maxFrozenEpochs)
	a3, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil || a3 >= 256 {
		return nil, fmt.Errorf("DelegateLockFromBytes: wrong targetMaxFrozenEpochs: %v", err)
	}
	ret.TargetMaxFrozenEpochs = byte(a3)

	// default delegator's chosen max to target's if delegator picked 0
	if ret.MaxFrozenEpochs == 0 {
		ret.MaxFrozenEpochs = ret.TargetMaxFrozenEpochs
	}

	return ret, nil
}

// DelegateLockFromOutputElements rebuilds a complete DelegateLock from
// both output elements:
//   - indexValuesBytes — bytes at output element index 1; expected
//     (master, target) pair.
//   - lockBytecode     — bytes at output element index 2; carries the
//     2-arg delegateLock (maxFrozenEpochs, inflationShare).
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
	lib.mustRegisterConstraint(DelegateLockName, 4, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return DelegateLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterConstraint(DelegateLockStateName, 2, func(data []byte) (Constraint, error) {
		return DelegateLockStateFromBytesWithLib(data, lib)
	})
}

//--------------------------- delegationLockState

func DelegateLockStateFromBytesWithLib(data []byte, lib *Library) (DelegateLockState, error) {
	sym, _, args, err := lib.Library.ParseBytecodeOneLevel(data, 2)
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
		// Round-trip the 4-arg delegateLock bytecode at output element
		// index 2. Target and master are not in the bytecode; they live
		// in the index-value tuple at index 1 and are exercised elsewhere
		// via DelegateLockFromOutputElements.
		targetChainID := base.RandomChainID()
		masterID := base.HolderID(SigLockRandom())
		example := NewDelegateLock(targetChainID, masterID, 3, 10, 600, 20)

		exampleBack, err := DelegateLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(example.MaxFrozenEpochs == 3, "DelegateLockFromBytes: wrong back 1")
		util.Assertf(exampleBack.MaxFrozenEpochs == example.MaxFrozenEpochs, "DelegateLockFromBytes: wrong back 2")
		util.Assertf(exampleBack.RequiredInflationShare == example.RequiredInflationShare, "DelegateLockFromBytes: wrong back 3")
		util.Assertf(example.RequiredInflationShare == 10, "DelegateLockFromBytes: wrong back 4")
		util.Assertf(exampleBack.EpochSlots == 600, "DelegateLockFromBytes: epochSlots round-trip")
		util.Assertf(exampleBack.TargetMaxFrozenEpochs == 20, "DelegateLockFromBytes: targetMaxFrozenEpochs round-trip")

		util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateLockName)

		// Also exercise the maxFrozenEpochs == 0 (delegator picks target's
		// max) byte-saving path: the produced bytecode encodes $0 = 0x and
		// the parser fills in the target's value.
		example2 := NewDelegateLock(targetChainID, masterID, 0, 10, 600, 20)
		back2, err := DelegateLockFromBytesWithLib(example2.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(back2.MaxFrozenEpochs == 20, "DelegateLockFromBytes: defaulted maxFrozenEpochs from target")

		pref1, err := lib.Library.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)

		pref2, err := lib.Library.EvalFromSource(nil, "#"+DelegateLockName)
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
	mfe := dOut.TargetMaxFrozenEpochs

	// produced output
	if cc.IsOrigin() {
		par.Require(o.Inflation() == 0 && amounts.IsFrozenCoverageZero(mfe),
			"evalEnforceFrozenCoverageOnDelegateOutput: inflation and frozen coverage must be 0 on a non-chain output and on chain origin")
		return par.AllocData(0xff)
	}
	// it is a non-origin chained output

	pred, err := ctx.ConsumedOutput(dOut.PredecessorInputIndex)
	par.RequireNoError(err)

	if pred.Lock().Name() != DelegateLockName {
		// predecessor is not delegation -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(mfe),
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
		par.Require(amounts.IsFrozenCoverageZero(mfe),
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

// evalDelegationOriginCrossCheck is the embedded EasyFL function that, at
// delegation origin, looks for the target chain output among consumed
// inputs and verifies the lock's inline (epochSlots,
// targetMaxFrozenEpochs) match the target chain's delegationParams.
//
// Best-effort: if the target chain output is not present among consumed
// inputs, this returns pass without verifying. Rationale: the typical
// delegator-initiates flow has no way to include the target chain output
// (the delegator cannot unlock it). Wrong inline values only break the
// delegation for the delegator (master-revoke still works because that
// path doesn't depend on the inline params being correct relative to
// the target) — no protocol-level harm.
//
// When the target IS present (a coordinated tx where the target sequencer
// transits its chain output and the delegator's origin output is created
// in the same tx), this enforces equality strictly.
func evalDelegationOriginCrossCheck(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()
	par.Require(ctx.SelfIsProducedOutput(), "evalDelegationOriginCrossCheck: produced output expected")
	o := ctx.SelfOutput()

	// Inline lock values (target chainID from index-values, epochSlots /
	// targetMaxFrozenEpochs from the lock body).
	lockBytes, err := o.At(int(ConstraintIndexLock))
	par.RequireNoError(err)
	ivBytes, err := o.At(int(ConstraintIndexIndexValues))
	par.RequireNoError(err)

	lib := ctx.GetLibrary()
	dLock, err := DelegateLockFromOutputElements(ivBytes, lockBytes, lib)
	par.RequireNoError(err)

	// Scan consumed inputs for a chain output whose chainID equals the
	// delegation's target. Break on first match.
	for i := 0; i < ctx.NumInputs(); i++ {
		consumed, err := ctx.ConsumedOutput(byte(i))
		if err != nil {
			continue
		}
		cc := consumed.ChainConstraint()
		if cc == nil {
			continue
		}
		oid := ctx.MustInputAt(byte(i))
		withChainID, ok := AsOutputWithChainID(consumed, oid)
		if !ok || withChainID.ChainID != dLock.Target {
			continue
		}
		// Target chain output found. Verify it carries delegationParams
		// at index 6 and that the values match the inline copy.
		dpBytes, err := consumed.At(int(ConstraintIndexDelegationParams))
		par.Require(err == nil && len(dpBytes) > 0,
			"evalDelegationOriginCrossCheck: target chain output %s does not carry delegationParams; chain is not a valid delegation target",
			withChainID.ChainID.String)
		dp, err := DelegationParamsFromBytesWithLib(dpBytes, lib)
		par.RequireNoError(err)
		par.Require(dp.EpochSlots == dLock.EpochSlots,
			"evalDelegationOriginCrossCheck: inline epochSlots (%d) != target's delegationParams.epochSlots (%d)",
			dLock.EpochSlots, dp.EpochSlots)
		par.Require(dp.MaxFrozenEpochs == dLock.TargetMaxFrozenEpochs,
			"evalDelegationOriginCrossCheck: inline targetMaxFrozenEpochs (%d) != target's delegationParams.maxFrozenEpochs (%d)",
			dLock.TargetMaxFrozenEpochs, dp.MaxFrozenEpochs)
		return par.AllocData(0xff)
	}
	// Target not in consumed; best-effort permits.
	return par.AllocData(0xff)
}
