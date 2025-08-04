package ledger

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type (
	DelegateLock struct {
		Target                      ChainLock
		MasterLock                  Accountable
		MaxFrozenSlots              uint16
		MinInflationAdvancePerEpoch uint64
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		Revoked         bool
	}

	DelegateOutput struct {
		OutputWithChainID
		DelegateLock
		DelegateLockState
	}
)

const (
	DelegateLockName       = "delegateLock"
	DelegateLockTemplate   = DelegateLockName + "(%s, %s, z16/%d, z64/%d)"
	DelegateLockTemplateHR = DelegateLockName + "(target=%s, master=%s, maxFreezeSlots=%d, inflAdvancePerEpoch=%s)"

	DelegateLockStateName       = "delegateLockState"
	DelegateLockStateTemplate   = DelegateLockStateName + "(z32/%d, %s)"
	DelegateLockStateTemplateHR = DelegateLockStateName + "(frozenUntilEpoch=%d, revoked=%v)"
)

//------------ DelegateLock

func NewDelegateLock(target ChainLock, master Accountable, maxFreezeSlots uint16, minInflationAdvancePerEpoch uint64) *DelegateLock {
	return &DelegateLock{
		Target:                      target,
		MasterLock:                  master,
		MaxFrozenSlots:              maxFreezeSlots,
		MinInflationAdvancePerEpoch: minInflationAdvancePerEpoch,
	}
}

func (d *DelegateLock) Source() string {
	return fmt.Sprintf(DelegateLockTemplate, d.Target.Source(), d.MasterLock.Source(), d.MaxFrozenSlots, d.MinInflationAdvancePerEpoch)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxFrozenSlots, util.Th(d.MinInflationAdvancePerEpoch))
}

func (d *DelegateLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func Delegate2LockFromBytes(data []byte) (*DelegateLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	if sym != DelegateLockName {
		return nil, fmt.Errorf("Delegate2LockFromBytes: not a DelegateLock")
	}
	// chain constraint index
	ret := &DelegateLock{}

	// target lock
	ret.Target, err = ChainLockFromBytes(args[0])
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	// master lock
	ret.MasterLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}

	// max coverage lock slots
	a2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max coverage lock slots: %v", err)
	}
	if a2 >= math.MaxUint16 {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max coverage lock slots")
	}
	ret.MaxFrozenSlots = uint16(a2)

	// minimum inflation advance
	ret.MinInflationAdvancePerEpoch, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong inflation advance per epoch: %v", err)
	}

	return ret, nil
}

func (d *DelegateLock) Name() string {
	return DelegateLockName
}

func (d *DelegateLock) Master() Accountable {
	return d.MasterLock
}

func registerDelegateLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateLockName, 4, func(data []byte) (Constraint, error) {
		return Delegate2LockFromBytes(data)
	}, initTestDelegateConstraint)
	lib.mustRegisterLock(DelegateLockName, func(bytes []byte) (Lock, error) {
		ret, err := Delegate2LockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(DelegateLockStateName, 2, func(data []byte) (Constraint, error) {
		return DelegateLockStateFromBytes(data)
	}, initTestDelegate2LockState)
}

func initTestDelegateConstraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	example := NewDelegateLock(target, master, 3000, 10)

	exampleBack, err := Delegate2LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.MaxFrozenSlots == 3000, "Delegate2LockFromBytes: wrong back 1")
	util.Assertf(exampleBack.MaxFrozenSlots == example.MaxFrozenSlots, "Delegate2LockFromBytes: wrong back 2")
	util.Assertf(exampleBack.MinInflationAdvancePerEpoch == example.MinInflationAdvancePerEpoch, "Delegate2LockFromBytes: wrong back 3")
	util.Assertf(example.MinInflationAdvancePerEpoch == 10, "Delegate2LockFromBytes: wrong back 4")

	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateLockName)
	exampleBack2, err := LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+DelegateLockName)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#"+DelegateLockName)
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
	util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
}

//--------------------------- delegationLockFreeze

func DelegateLockStateFromBytes(data []byte) (DelegateLockState, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
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
	return DelegateLockState{
		LastFrozenEpoch: fr,
		Revoked:         !easyfl_util.IsZero(easyfl.StripDataPrefix(args[1])),
	}, nil
}

func (d DelegateLockState) Source() string {
	r := "0x"
	if d.Revoked {
		r = "0xff"
	}
	return fmt.Sprintf(DelegateLockStateTemplate, d.LastFrozenEpoch, r)
}

func (d DelegateLockState) String() string {
	return fmt.Sprintf(DelegateLockStateTemplateHR, d.LastFrozenEpoch, d.Revoked)
}

func (d DelegateLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLockState) Name() string {
	return DelegateLockStateName
}

func initTestDelegate2LockState() {
	dlz := DelegateLockState{3001, true}

	dlzBack, err := DelegateLockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.LastFrozenEpoch == 3001, "DelegateLockState: inconsistency 1")
	util.Assertf(dlzBack.Revoked, "DelegateLockState: inconsistency 2")
	util.Assertf(dlz == dlzBack, "DelegateLockState: inconsistency 3")
}

type MakeDelegateInitOutputParams struct {
	Amount                      uint64
	Master                      Accountable
	Target                      ChainLock
	MaxFreezeSlots              uint16
	MinInflationAdvancePerEpoch uint64
	StartSlot                   base.Slot
}

func MakeDelegateInitOutput(par MakeDelegateInitOutputParams) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmounts(int64(par.Amount))
		o.WithLock(NewDelegateLock(par.Target, par.Master, par.MaxFreezeSlots, par.MinInflationAdvancePerEpoch))
		o.MustPushConstraint(NewChainOrigin(par.StartSlot, par.Amount).Bytes())
		o.MustPushConstraint(DelegateLockState{}.Bytes())
	})
}

func AsDelegateOutput(o *Output, oid base.OutputID) (ret DelegateOutput, ok bool) {
	out, ok := AsOutputWithChainID(o, oid)
	if !ok {
		return
	}
	return DelegateOutputFromOutputWithChainID(&out)
}

func DelegateOutputFromOutputWithChainID(o *OutputWithChainID) (ret DelegateOutput, ok bool) {
	lock := o.Output.Lock()
	if lock.Name() != DelegateLockName {
		return
	}
	ret.OutputWithChainID = *o
	dLock, ok := lock.(*DelegateLock)
	util.Assertf(ok, "DelegateOutputFromOutputWithChainID: inconsistency")
	ret.DelegateLock = *dLock

	if data, err := o.Output.ConstraintAt(3); err == nil {
		ret.DelegateLockState, err = DelegateLockStateFromBytes(data)
	}
	return
}

func IsFrozenDelegateOutput(o *Output, oid base.OutputID, txSlot base.Slot) bool {
	lock := o.Lock()
	if lock.Name() != DelegateLockName {
		return false
	}
	dOut, ok := AsDelegateOutput(o, oid)
	util.Assertf(ok, "IsFrozen: inconsistency 1")

	return dOut.IsFrozen(txSlot)
}

type MakeDelegateSuccessorOutputParams struct {
	Timestamp                   base.LedgerTime
	PredTimestamp               base.LedgerTime
	FreezeUntilEpoch            uint32
	PredOutputIndex             byte
	Inflation                   uint64
	HarvestInflation            uint64
	MinInflationAdvancePerEpoch uint64
	DisableConsistencyChecks    bool
}

func (o *DelegateOutput) IsFrozen(txSlot base.Slot) bool {
	return o.UnfreezeSlot() <= uint32(txSlot)
}

func (o *DelegateOutput) IsUnlockableByTarget(txSlot base.Slot) bool {
	return o.IsFrozen(txSlot) || txSlot.Uint32() >= o.UnfreezeSlot()+DelegationConst().SafeRevocationSlots
}

func (o *DelegateOutput) IsUnlockableByMaster(txSlot base.Slot) bool {
	return !o.IsFrozen(txSlot)
}

func (o *DelegateOutput) MakeDelegateSuccessorOutput(par MakeDelegateSuccessorOutputParams) (*Output, error) {
	dconst := DelegationConst()
	txEpoch := dconst.EpochFromSlot(o.Target.ChainID(), uint32(par.Timestamp.Slot))
	freeze := par.FreezeUntilEpoch >= txEpoch

	if !par.DisableConsistencyChecks && par.Timestamp.IsSlotBoundary() {
		return nil, fmt.Errorf("MakeDelegateSuccessorOutput: can't be a branch transaction")
	}
	if !par.DisableConsistencyChecks && par.HarvestInflation > par.Inflation {
		return nil, fmt.Errorf("MakeDelegateSuccessorOutput: can't harvest more inflation (%s) than generate (%s)",
			util.Th(par.HarvestInflation), util.Th(par.Inflation))
	}

	if !par.DisableConsistencyChecks && txEpoch > par.FreezeUntilEpoch {
		return nil, fmt.Errorf("MakeDelegateSuccessorOutput: wrong FreezeUntilEpoch: %d", par.FreezeUntilEpoch)
	}
	var frozenEpochs, frozenSlots uint32
	if freeze {
		frozenEpochs = par.FreezeUntilEpoch - txEpoch + 1
		if !par.DisableConsistencyChecks && frozenEpochs > dconst.MaxFrozenEpochs {
			return nil, fmt.Errorf("MakeDelegateSuccessorOutput: too many frozen epochs: %d", par.FreezeUntilEpoch)
		}
		frozenSlots = dconst.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), uint32(par.Timestamp.Slot), byte(frozenEpochs))
		if !par.DisableConsistencyChecks && frozenSlots > uint32(o.MaxFrozenSlots) {
			return nil, fmt.Errorf("MakeDelegateSuccessorOutput: FreezeUntilEpoch %d (%d frozen epochs, %d frozen slots) inconsistent with MaxFrozenSlots set by delegator: %d",
				par.FreezeUntilEpoch, frozenEpochs, frozenSlots, o.MaxFrozenSlots)
		}
	}

	if par.Inflation > L().CalcChainInflationAmountOneSlot(par.PredTimestamp.Slot, o.Output.TokenBalance()) {
		return nil, fmt.Errorf("MakeDelegateSuccessorOutput: wrong inflation amount: %s", util.Th(par.Inflation))
	}

	var amountsVector []int64

	if freeze {
		amountsVector = make([]int64, frozenEpochs+2)
		amountsVector[0] = int64(o.Output.TokenBalance() + par.Inflation - par.HarvestInflation)
		amountsVector[1] = int64(par.Inflation)
		for i := 2; i < len(amountsVector); i++ {
			amountsVector[i] = amountsVector[0]
		}
	} else {
		amountsVector = []int64{int64(o.Output.TokenBalance() + par.Inflation - par.HarvestInflation)}
	}
	chainConstraint := NewChainConstraint(o.ChainID, par.PredOutputIndex, 2, o.OriginSlot, o.OriginAmount)
	return NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amountsVector...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterLock, o.MaxFrozenSlots, par.MinInflationAdvancePerEpoch))
		o1.MustPushConstraint(chainConstraint.Bytes())
		o1.MustPushConstraint(DelegateLockState{LastFrozenEpoch: par.FreezeUntilEpoch}.Bytes())
	}), nil
}

func (o *DelegateOutput) MinRequiredInflationAdvance(ts base.LedgerTime, frozenEpochs byte) uint64 {
	dconst := DelegationConst()
	frozenSlotsFromEpochs := dconst.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), uint32(ts.Slot), frozenEpochs)
	return (uint64(frozenSlotsFromEpochs) * o.MinInflationAdvancePerEpoch) / uint64(dconst.DelegationEpochSlots)
}

func (o *DelegateOutput) UnfreezeSlot() uint32 {
	if o.LastFrozenEpoch == 0 {
		return uint32(o.ID.Slot())
	}
	dconst := DelegationConst()
	return (o.LastFrozenEpoch+1)*dconst.DelegationEpochSlots - dconst.EpochOffsetSlots(o.Target.ChainID())
}

func (o *DelegateOutput) FreezeUntilLatestEpoch(ts base.LedgerTime) (ret uint32) {
	dconst := DelegationConst()
	ret = dconst.EpochFromSlot(o.Target.ChainID(), ts.Slot.Uint32())
	slotsToFreeze := dconst.CoveredSlotsInCurrentEpoch(o.Target.ChainID(), ts.Slot.Uint32())
	maxRet := ret + dconst.MaxFrozenEpochs - 1
	for ret < maxRet && slotsToFreeze+dconst.DelegationEpochSlots <= uint32(o.MaxFrozenSlots) {
		slotsToFreeze += dconst.DelegationEpochSlots
		ret++
	}
	return
}

func (o *DelegateOutput) FrozenEpochs(txTs base.LedgerTime) (byte, error) {
	dconst := DelegationConst()
	txEpoch := dconst.EpochFromSlot(o.Target.ChainID(), txTs.Slot.Uint32())
	if txEpoch > o.LastFrozenEpoch {
		return 0, nil
	}
	ret := o.LastFrozenEpoch - txEpoch + 1
	if ret > dconst.MaxFrozenEpochs {
		return 0, fmt.Errorf("frozen epochs cannot exceed %d", dconst.MaxFrozenEpochs)
	}
	return byte(ret), nil
}

func (o *DelegateOutput) MakeFrozenCoverageAmountDeltasForRevoking(txTs base.LedgerTime) []int64 {
	dconst := DelegationConst()
	diffEpochs := dconst.DiffEpochs(o.Target.ChainID(), txTs, o.Timestamp())
	util.Assertf(diffEpochs >= 0, "MakeFrozenCoverageAmountDeltasForRevoking: wrong timestamp %s", txTs.String)

	fc := o.Output.Amounts().FrozenCoverageVector()
	ret := make([]int64, dconst.MaxFrozenEpochs)
	idx := 0
	for i := diffEpochs; i < len(fc); i++ {
		ret[idx] = -fc[i]
		idx++
	}
	return ret
}

func (o *DelegateOutput) MakeFrozenCoverageAmounts(frozenEpochs byte, tokenBalance uint64) ([]int64, error) {
	mx := byte(DelegationConst().MaxFrozenEpochs)
	if frozenEpochs > mx {
		return nil, fmt.Errorf("MakeFrozenCoverageAmounts: frozen epochs value (%d) exceed maximum %d", frozenEpochs, mx)
	}
	ret := make([]int64, mx)
	for i := 0; i < int(frozenEpochs); i++ {
		ret[i] = int64(tokenBalance)
	}
	return ret, nil
}

type MakeDelegateRevokeOutputParams struct {
	Timestamp                base.LedgerTime
	PredTimestamp            base.LedgerTime
	PredOutputIndex          byte
	Inflation                uint64
	HarvestInflation         uint64
	DisableConsistencyChecks bool
}

func (o *DelegateOutput) MakeDelegateRevokeOutput(par MakeDelegateRevokeOutputParams) (*Output, error) {
	if !par.DisableConsistencyChecks && par.Timestamp.IsSlotBoundary() {
		return nil, fmt.Errorf("MakeDelegateRevokeOutput: can't be a branch transaction")
	}
	if !par.DisableConsistencyChecks && !o.IsUnlockableByTarget(par.Timestamp.Slot) {
		return nil, fmt.Errorf("MakeDelegateRevokeOutput: can't be unlocked by target in slot %d", par.Timestamp.Slot)
	}
	if !par.DisableConsistencyChecks && par.HarvestInflation > par.Inflation {
		return nil, fmt.Errorf("MakeDelegateRevokeOutput: can't harvest more inflation (%s) than generate (%s)",
			util.Th(par.HarvestInflation), util.Th(par.Inflation))
	}
	if !par.DisableConsistencyChecks && par.Inflation > L().CalcChainInflationAmountOneSlot(par.PredTimestamp.Slot, o.Output.TokenBalance()) {
		return nil, fmt.Errorf("MakeDelegateRevokeOutput: wrong inflation amount: %s", util.Th(par.Inflation))
	}

	amounts := []int64{int64(o.Output.TokenBalance() + par.Inflation - par.HarvestInflation), int64(par.Inflation)}
	frozenCoverageVector := o.MakeFrozenCoverageAmountDeltasForRevoking(par.Timestamp)
	amounts = append(amounts, frozenCoverageVector...)

	chainConstraint := NewChainConstraint(o.ChainID, par.PredOutputIndex, 2, o.OriginSlot, o.OriginAmount)
	return NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amounts...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterLock, o.MaxFrozenSlots, 0))
		o1.MustPushConstraint(chainConstraint.Bytes())
		o1.MustPushConstraint(DelegateLockState{
			LastFrozenEpoch: o.LastFrozenEpoch,
			Revoked:         true,
		}.Bytes())
	}), nil
}

// SafeRevocationSlots return slots from-to (inclusive) when target cannot consume the delegation output
// (0, 0) means it is revoked, i.e., it cannot be consumed by the target
func (o *DelegateOutput) SafeRevocationSlots() (from, to uint32) {
	if o.Revoked {
		return
	}
	unfreeze := o.UnfreezeSlot()
	return unfreeze, unfreeze + DelegationConst().SafeRevocationSlots - 1
}

func (o *DelegateOutput) LinesSource(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("---- delegation output ----")
	ret.Append(o.OutputWithChainID.Lines("   "))
	ret.Add("Master: %s", o.MasterLock.Source())
	ret.Add("Target: %s", o.Target.Source())
	ret.Add("MaxFrozenSlots: %d", o.MaxFrozenSlots)
	ret.Add("Frozen until epoch: %d", o.LastFrozenEpoch)
	revStr := "all (permanently revoked by the master)"
	f, t := o.SafeRevocationSlots()
	util.Assertf(f <= t, "f<=t")
	if f != 0 || t != 0 {
		revStr = fmt.Sprintf("from %d to %d (inclusive)", f, t)
	}
	ret.Add("Safe revocation slots: %s", revStr)
	return ret
}

// ------------------ delegation constants

type DelegationConstants struct {
	SafeRevocationSlots  uint32
	DelegationEpochSlots uint32
	MaxFrozenEpochs      uint32
}

var _delegationConstants atomic.Pointer[DelegationConstants]

func DelegationConst() *DelegationConstants {
	if ret := _delegationConstants.Load(); ret != nil {
		return ret
	}
	c := _precalcDelegationConstants()
	_delegationConstants.Store(c)
	return c
}

func (c *DelegationConstants) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	ln.Add("safe revocation slots:  %d", c.SafeRevocationSlots)
	ln.Add("delegation epoch slots: %d", c.DelegationEpochSlots)
	ln.Add("max frozen epochs:      %d", c.MaxFrozenEpochs)
	return ln
}

func _precalcDelegationConstants() *DelegationConstants {
	resRevoc, err := L().EvalFromSource(nil, "constDelegationSafeRevocationSlots")
	util.AssertNoError(err)

	resEpochSlots, err := L().EvalFromSource(nil, "constDelegationEpochSlots")
	util.AssertNoError(err)

	resMaxFrozenEpochs, err := L().EvalFromSource(nil, "constDelegationMaxFrozenEpochs")
	util.AssertNoError(err)

	ret := &DelegationConstants{
		SafeRevocationSlots:  easyfl_util.MustUint32FromBytes(resRevoc),
		DelegationEpochSlots: easyfl_util.MustUint32FromBytes(resEpochSlots),
		MaxFrozenEpochs:      easyfl_util.MustUint32FromBytes(resMaxFrozenEpochs),
	}
	util.Assertf(uint32(AmountIndexFrozenCoverage)+ret.MaxFrozenEpochs <= 16, "int(AmountIndexFrozenCoverage)+MaxFrozenEpochs <= 16")
	return ret
}

// EpochOffsetSlots returns slot offset unique for the delegation target chain ChainID.
// Each chain ChainID defines own grid of epochs. It spreads delegation output consumption among sequencers
func (c *DelegationConstants) EpochOffsetSlots(targetID base.ChainID) uint32 {
	return binary.BigEndian.Uint32(targetID[:4]) % c.DelegationEpochSlots
}

// CoveredSlotsInCurrentEpoch returns how many slots are covered in the current epoch defined by txSlot and
// taking into account the offset calculated from the target
func (c *DelegationConstants) CoveredSlotsInCurrentEpoch(target base.ChainID, txSlot uint32) uint32 {
	offs := c.EpochOffsetSlots(target)
	return c.DelegationEpochSlots - (txSlot+offs)%c.DelegationEpochSlots
}

func (c *DelegationConstants) _validUnfreezeSlot(target base.ChainID, unfreezeSlot uint32) bool {
	return (unfreezeSlot+c.EpochOffsetSlots(target))%c.DelegationEpochSlots == 0
}

func (c *DelegationConstants) FrozenSlotsFromFrozenEpochs(target base.ChainID, txSlot uint32, frozenEpochs byte) uint32 {
	if frozenEpochs == 0 {
		return 0
	}
	return c.CoveredSlotsInCurrentEpoch(target, txSlot) + uint32(frozenEpochs-1)*c.DelegationEpochSlots
}

func (c *DelegationConstants) EpochFromSlot(target base.ChainID, txSlot uint32) uint32 {
	return (txSlot + c.EpochOffsetSlots(target)) / c.DelegationEpochSlots
}

func (c *DelegationConstants) DiffEpochs(targetID base.ChainID, ts1, ts2 base.LedgerTime) int {
	dconst := DelegationConst()
	epoch1 := dconst.EpochFromSlot(targetID, ts1.Slot.Uint32())
	epoch2 := dconst.EpochFromSlot(targetID, ts2.Slot.Uint32())
	return int(epoch1) - int(epoch2)
}

func (c *DelegationConstants) AdjustFrozenCoverageVector(targetID base.ChainID, vect []int64, predTs, succTs base.LedgerTime) []int64 {
	shift := c.DiffEpochs(targetID, succTs, predTs)
	util.Assertf(shift >= 0, "wrong order of timestamps %s and %s", predTs.String, succTs.String)
	ret := make([]int64, c.MaxFrozenEpochs)
	if uint32(shift) >= c.MaxFrozenEpochs {
		return ret
	}
	for i, v := range vect[shift:] {
		ret[i] = v
	}
	return ret
}

const delegateLock2Source = `
func constDelegationSafeRevocationSlots  : 30
func constDelegationEpochSlots : u32/512
func constDelegationMaxFrozenEpochs : 4

// $0 target chain ChainID
func delegationEpochOffset : mod( slice($0, 0, 3), constDelegationEpochSlots)

// $0 target chain ChainID
// $1 epoch
func firstSlotInDelegationEpoch :
if(
   isZero($1),
   u64/0,
   sub(mul($1, constDelegationEpochSlots), delegationEpochOffset($0))
)

// $0 target chain ChainID
// $1 slot
func delegationEpochFromSlot :
div(
   add($1, delegationEpochOffset($0)),
   constDelegationEpochSlots
)

func _selfChainID : parseInlineDataArgument(selfSiblingConstraint(2), #chain, 0)
func _isDelegationOrigin : isChainOriginID(_selfChainID)

// $0 index of the constraint on the successor output
func successorConstraint : atPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2),0), $0))

func _predecessorLastFrozenEpoch : parseInlineDataArgument(consumedConstraintByIndex(selfChainPredInputIndex(2), 3),selfBytecodePrefix, 0)
func _predecessorTokenBalance : amountAt(consumedConstraintByIndex(selfChainPredInputIndex(2), 0), 0)

// $0 last frozen epoch
// $1 revoked
// mutable part of the delegation output
func delegateLockState : 
or(
   // not checked in the consumed context
   not(selfIsProducedOutput),
   // 'produced' context
   require(
      or( not($1), equalUint($0, _predecessorLastFrozenEpoch) ),
      !!!revocation_cant_mutate_frozen_epochs
   )
)

// self id delegation output

func _selfTarget : parseArgumentBytecode(self,selfBytecodePrefix,0)
func _selfTargetChainID : parseInlineDataArgumentAnyPrefix(_selfTarget,0)
func _selfDelegationEpochOffset : delegationEpochOffset(_selfTargetChainID)
func _selfLastFrozenEpoch : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(3),#delegateLockState, 0))
func _selfIsRevoked : parseInlineDataArgument(selfSiblingConstraint(3),#delegateLockState, 1)
func _selfEpoch : delegationEpochFromSlot(_selfTargetChainID, txSlot)

// $0 _selfLastFrozenEpoch
// $1 _selfEpoch
func __selfFrozenEpochs : if( lessThanUint($0, $1), u64/0, add(sub($0, $1),1) ) 
func _selfFrozenEpochs : __selfFrozenEpochs(_selfLastFrozenEpoch, _selfEpoch)

// $0 - _selfLastFrozenEpoch
func __selfIsNotFrozen : or( isZero($0), lessThan( $0, delegationEpochFromSlot(_selfTargetChainID,txSlot) ) )

func _selfIsNotFrozen : __selfIsNotFrozen(_selfLastFrozenEpoch)

// $0 output slot
func _coveredSlotsInCurrentEpoch :
sub(
   constDelegationEpochSlots,
   mod(
      add($0, _selfDelegationEpochOffset),
      constDelegationEpochSlots 
   )
)

// $0 slot of the output
// $1 last frozen epoch
func _frozenSlots : 
if(
   isZero($1),
   u64/0,
   sub( firstSlotInDelegationEpoch(_selfTargetChainID, add($1,1)), $0 )
)

// $0 slot of the output (either consumed or produced context)
func _selfFrozenSlots : _frozenSlots($0, _selfLastFrozenEpoch)

// $0 slot of the output
func _selfUnfreezeSlot : add($0, _frozenSlots($0, _selfLastFrozenEpoch))

func _equalTo1Of2 : or(equal($0,$1), equal($0,$2))

// $0 minimum advance inflation upon freeze per full epoch 
// returns minimum required amount of inflation advance
func _calcMinimumAdvanceForSuccessor : div(mul(_frozenSlots(txSlot, _selfLastFrozenEpoch),$0), constDelegationEpochSlots)

// $0 max freeze slots
// $1 minimum advance inflation upon freeze per full epoch 
func _validLimits :
and(
    require(
       lessOrEqualThan(len($0), u64/2),
       !!!max_freeze_slots_must_be_max_2_bytes 
    ),
    require(
       lessOrEqualThan(uint8Bytes(_selfFrozenSlots(txSlot)), uint8Bytes($0)),
       !!!frozen_slots_cannot_exceed_maximum_set_by_delegator
    ),
    require(
       lessOrEqualThan(uint8Bytes(_selfFrozenEpochs), uint8Bytes(constDelegationMaxFrozenEpochs)),
       !!!frozen_epochs_cannot_exceed_constDelegationMaxFrozenEpochs
    ),
    require(
       or(_isDelegationOrigin, lessOrEqualThan(_calcMinimumAdvanceForSuccessor($1), sub(selfTokenBalanceValue, _predecessorTokenBalance))),
       !!!not_enough_inflation_advance
    )
)

func _validBase :
and(
    require(
	   equal(selfNumConstraints, u64/4), 
	   !!!delegation_must_have_exactly_4_constraints
    ), // to prevent injection attacks
    require( 
        _equalTo1Of2(parsePrefixBytecode(_selfTarget), #c, #chainLock),
        !!!delegation_target_must_by_chainLock
    ),
    require(
	   equal(parsePrefixBytecode(selfSiblingConstraint(2)), #chain), 
	   !!!#chain_is_expected_at_index_2
    ),
    require(
	   equal(parsePrefixBytecode(selfSiblingConstraint(3)), #delegateLockState), 
	   !!!#delegateLockState_is_expected_at_index_3
    ),
    require(
	   or(not(_isDelegationOrigin), and(not(_selfIsRevoked), isZero(_selfLastFrozenEpoch))), 
	   !!!wrong_delegation_origin_parameters
    )
)

// checks validity of the composition of the produced constraint 
// $0 max freeze slots
// $1 minimum advance inflation upon freeze per full epoch 
func _validDelegationProduced :
and(
    selfIsProducedOutput,
    enforceMinimumStorageDeposit,
    _validBase,
    _validLimits($0,$1),
	//_validFrozenCoverageVector(_selfLastFrozenEpoch)
)

// $0 master lock
// (consumed context)
func _masterUnlocked : and( $0, require(_selfIsNotFrozen, !!!master_can't_unlock_frozen_delegation_output) )

func _amountOnSuccessor : tokenBalanceByOutputPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2), 0)))

// $0 unfreezeSlot
func _insideSafeRevocationWindow : and(
    not(_isDelegationOrigin),
	lessOrEqualThan(uint8Bytes($0), uint8Bytes(txSlot)),
    lessThan(uint8Bytes(txSlot), add($0, constDelegationSafeRevocationSlots))
)

func _consumedUnfreezeSlot : _selfUnfreezeSlot( timeSlotOfInputByIndex( selfOutputIndex ) )

//func _successorFrozenEpochs : parseInlineDataArgument(successorConstraint(3),#delegateLockState,0)
func _successorIsRevoked : parseInlineDataArgument(successorConstraint(3),#delegateLockState,1)

// $0 target lock
// 'consumed' context
func _targetUnlocked :
and(
	  // if it is revoked, only master can unlock it
   require(not(_selfIsRevoked), !!!revoked_delegation_cannot_be_unlocked_by_the_target),
   require(not(_insideSafeRevocationWindow(_consumedUnfreezeSlot)), !!!delegation_target_should_not_be_unlocked_inside_safe_revocation_window),
   require( or( _successorIsRevoked, _selfIsNotFrozen ), !!!frozen_delegation_can_be_unlocked_by_the_target_only_for_revocation),
	  // target lock must be unlocked
   require($0, !!!delegation_target_chain_must_be_unlocked),  
	  // amount should not decrease
   require(lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor), !!!delegated_amount_should_not_decrease),
	  // delegation lock must be immutable
   require(equal(successorConstraint(1), selfSiblingConstraint(lockConstraintIndex)), !!!delegation_lock_must_be_immutable)
)


// $0 target chain lock
// $1 master lock
func _validDelegationConsumed : and(
   selfIsConsumedOutput,
   or(
      _masterUnlocked($1),
      _targetUnlocked($0)
   )
)

// Delegation lock output. Immutable 
// $0 target chain lock
// $1 master lock
// $2 max freeze slots
// $3 minimum advance inflation upon freeze per full epoch. In order to freeze coverage, target just provide in advance inflation  
func delegateLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_index_1),
    or(
       _validDelegationProduced($2, $3),
       _validDelegationConsumed($0,$1)
    )
)
`
