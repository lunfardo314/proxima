package ledger

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type (
	DelegationOutput struct {
		OutputWithChainID
		DelegateLock
		DelegateLockState
	}

	MakeDelegateInitOutputParams struct {
		Amount                          uint64
		Master                          Accountable
		Target                          ChainLock
		MaxFreezeEpochs                 byte
		MaxToleratedInflationCostMargin uint16
		StartSlot                       base.Slot
	}
)

func MakeDelegationInitOutput(par MakeDelegateInitOutputParams) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmounts(int64(par.Amount))
		o.WithLock(NewDelegateLock(par.Target, par.Master, par.MaxFreezeEpochs, par.MaxToleratedInflationCostMargin))
		o.MustPushConstraint(NewChainOrigin(par.StartSlot, par.Amount).Bytes())
		o.MustPushConstraint(DelegateLockState{}.Bytes())
	})
}

func AsDelegationOutput(o *Output, oid base.OutputID) (ret DelegationOutput, ok bool) {
	out, ok := AsOutputWithChainID(o, oid)
	if !ok {
		return
	}
	return DelegationOutputFromOutputWithChainID(&out)
}

func DelegationOutputFromOutputWithChainID(o *OutputWithChainID) (ret DelegationOutput, ok bool) {
	lock := o.Output.Lock()
	if lock.Name() != DelegateLockName {
		return
	}
	ret.OutputWithChainID = *o
	dLock, ok := lock.(*DelegateLock)
	util.Assertf(ok, "DelegationOutputFromOutputWithChainID: inconsistency")
	ret.DelegateLock = *dLock

	if data, err := o.Output.ConstraintAt(3); err == nil {
		ret.DelegateLockState, err = DelegateLockStateFromBytes(data)
	}
	return
}

// Coverage returns for the consumed output in the transaction with specified timestamp
// - coverage presented by the output, which includes frozen coverage part
// - frozen part separately
func Coverage(o *Output, oid base.OutputID, txTs base.LedgerTime) (coverage, frozen uint64) {
	outChain, isChain := AsOutputWithChainID(o, oid)
	if !isChain {
		// if not a chain, coverage is equal to the toke balance
		return o.TokenBalance(), 0
	}

	if dOut, isDelegate := DelegationOutputFromOutputWithChainID(&outChain); isDelegate {
		if dOut.IsInFrozenSlot(uint32(txTs.Slot)) {
			// delegated frozen outputs have zero coverage
			return 0, 0
		}
		// delegated not-frozen output coverage is equal to the token balance
		return o.TokenBalance(), 0
	}

	// otherwise, it is token balance plus adjusted frozen coverage stored in the chained output
	fr := uint64(outChain.AdjustedFrozenCoverage(txTs))
	return o.TokenBalance() + fr, fr
}

func (o *DelegationOutput) IsMarkedFrozen() bool {
	return o.State == DelegateLockStateFrozen
}

func (o *DelegationOutput) IsMarkedRevoked() bool {
	return o.State == DelegateLockStateRevoked
}

// IsInFrozenSlot true means only target can consume it in the slot
func (o *DelegationOutput) IsInFrozenSlot(txSlot uint32) bool {
	if o.IsMarkedRevoked() || !o.IsMarkedFrozen() {
		return false
	}
	dconst := DelegationConst()
	_, _, firstSlot, lastSlot := dconst.EpochLimitsFromSlot(o.Target.ChainID(), o.LastFrozenEpoch)
	return firstSlot <= txSlot && txSlot <= lastSlot
}

func (o *DelegationOutput) IsInSafeRevocationWindow(txSlot uint32) bool {
	if o.IsMarkedRevoked() || !o.IsMarkedFrozen() {
		return false
	}
	dconst := DelegationConst()
	_, _, _, lastSlot := dconst.EpochLimitsFromSlot(o.Target.ChainID(), o.LastFrozenEpoch)
	return lastSlot < txSlot && txSlot <= lastSlot+dconst.SafeRevocationSlots
}

// IsUnlockableByTarget true if it is not revoked and not in the safe revocation window
func (o *DelegationOutput) IsUnlockableByTarget(txSlot uint32) bool {
	if o.IsMarkedRevoked() {
		return false
	}
	if !o.IsMarkedFrozen() {
		return true
	}
	// marked frozen, not revoked
	return !o.IsInSafeRevocationWindow(txSlot)
}

func (o *DelegationOutput) IsUnlockableByMaster(txSlot uint32) bool {
	return !o.IsInFrozenSlot(txSlot)
}

func (o *DelegationOutput) UnfreezeSlot() uint32 {
	if o.LastFrozenEpoch == 0 {
		return uint32(o.ID.Slot())
	}
	dconst := DelegationConst()
	return (o.LastFrozenEpoch+1)*dconst.DelegationEpochSlots - dconst.epochOffsetSlots(o.Target.ChainID())
}

func (o *DelegationOutput) MakeDelegationFreezeOutput(txTs base.LedgerTime, freezeUntilEpoch uint32, predOutputIndex byte, disableConsistencyCheck ...bool) (ret *Output, requiredAdvance, projectedInflation uint64, err error) {
	checkConsistency := len(disableConsistencyCheck) == 0 || !disableConsistencyCheck[0]
	if checkConsistency && !o.IsUnlockableByTarget(uint32(txTs.Slot)) {
		return nil, 0, 0, fmt.Errorf("MakeDelegationFreezeOutput: delegation output cannot be unlocked by the target")
	}
	if checkConsistency && o.ID.Slot() >= txTs.Slot {
		return nil, 0, 0, fmt.Errorf("MakeDelegationFreezeOutput: successor timestamp must be at least 1 slot after")
	}
	if checkConsistency && txTs.IsSlotBoundary() {
		return nil, 0, 0, fmt.Errorf("MakeDelegationFreezeOutput: can't be a branch transaction")
	}
	dconst := DelegationConst()
	txEpoch := dconst.EpochFromSlot(o.Target.ChainID(), uint32(txTs.Slot))
	if freezeUntilEpoch < txEpoch {
		return nil, 0, 0, fmt.Errorf("MakeDelegationFreezeOutput: wrong value for 'freeze until epoch'")
	}
	frozenEpochs := freezeUntilEpoch - txEpoch + 1
	if frozenEpochs > uint32(o.MaxFrozenEpochs) {
		return nil, 0, 0, fmt.Errorf("MakeDelegationFreezeOutput: frozen epochs exceed limit set by the delegation outputs: %d", freezeUntilEpoch)
	}

	inflation := L().CalcChainInflationAmountOneSlot(o.ID.Slot(), o.Output.TokenBalance())
	requiredAdvance = o.RequiredInflationAdvance(txTs, byte(frozenEpochs))

	ownTokenBalance := o.Output.TokenBalance() + inflation
	successorTokenBalance := ownTokenBalance + requiredAdvance
	var amountsVector [15]int64
	amountsVector[AmountIndexTokenBalance] = int64(successorTokenBalance)
	amountsVector[AmountIndexInflation] = int64(inflation)
	for i := byte(0); i < byte(frozenEpochs); i++ {
		amountsVector[AmountIndexFrozenCoverage+i] = int64(successorTokenBalance)
	}
	chainConstraint := NewChainConstraint(o.ChainID, predOutputIndex, 2, o.OriginSlot, o.OriginAmount)

	ret = NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amountsVector[:]...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterLock, o.MaxFrozenEpochs, o.MaxInflationMarginTolerance))
		o1.MustPushConstraint(chainConstraint.Bytes())
		o1.MustPushConstraint(DelegateLockState{LastFrozenEpoch: freezeUntilEpoch, State: DelegateLockStateFrozen}.Bytes())
	})
	frozenSlots := dconst.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), uint32(txTs.Slot), byte(frozenEpochs))
	projectedInflation = InflationForSlots(ownTokenBalance, frozenSlots)
	return
}

// ProjectedInflation max inflation that could be generated on the output for a number of frozen epochs
func (o *DelegationOutput) ProjectedInflation(txTs base.LedgerTime, frozenEpochs byte) uint64 {
	if o.ID.Slot() >= txTs.Slot {
		return 0
	}
	dconst := DelegationConst()
	frozenSlots := dconst.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), uint32(txTs.Slot), frozenEpochs)
	amount := o.Output.TokenBalance() + L().CalcChainInflationAmountOneSlot(o.ID.Slot(), o.Output.TokenBalance())
	return InflationForSlots(amount, frozenSlots)
}

// RequiredInflationAdvance calculates how big advance requires the delegation output for freezing it,
// as calculated from immutable MinInflationAdvancePerFullEpoch value on it
func (o *DelegationOutput) RequiredInflationAdvance(txTs base.LedgerTime, frozenEpochs byte) uint64 {
	dconst := DelegationConst()
	frozenSlots := dconst.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), txTs.Slot.Uint32(), frozenEpochs)
	src := fmt.Sprintf("requiredMinimumInflationAdvance(u64/%d, u64/%d, u64/%d, u64/%d)",
		frozenSlots,
		txTs.Slot,
		o.Output.TokenBalance(),
		o.MaxInflationMarginTolerance,
	)

	resBin, err := L().EvalFromSource(nil, src)
	util.AssertNoError(err)

	return binary.BigEndian.Uint64(resBin)
}

func (o *DelegationOutput) FreezeLimits(ts base.LedgerTime) (freezeUntilEpoch uint32) {
	if o.IsInFrozenSlot(uint32(ts.Slot)) {
		return
	}
	dconst := DelegationConst()
	startEpoch := dconst.EpochFromSlot(o.Target.ChainID(), ts.Slot.Uint32())
	freezeUntilEpoch = startEpoch + uint32(o.MaxFrozenEpochs) - 1
	return
}

func (o *DelegationOutput) FrozenEpochs(txTs base.LedgerTime) (from, to, total uint32) {
	if !o.IsInFrozenSlot(uint32(txTs.Slot)) {
		return
	}
	dconst := DelegationConst()
	txEpoch := dconst.EpochFromSlot(o.Target.ChainID(), txTs.Slot.Uint32())
	if txEpoch > o.LastFrozenEpoch {
		return 0, 0, 0
	}
	ret := o.LastFrozenEpoch - txEpoch + 1
	return txEpoch, o.LastFrozenEpoch, ret
}

func (o *DelegationOutput) FrozenSlots(txTs base.LedgerTime) (from, to, total uint32) {
	to = o.UnfreezeSlot() - 1
	from = txTs.Slot.Uint32()
	if to < from {
		return 0, 0, 0
	}
	return from, to, to - from + 1
}

func (o *DelegationOutput) MakeFrozenCoverageAmountDeltasForRevoking(txTs base.LedgerTime) []int64 {
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

func (o *DelegationOutput) MakeFrozenCoverageAmounts(frozenEpochs byte, tokenBalance uint64) ([]int64, error) {
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

type MakeDelegationRevokeOutputParams struct {
	Timestamp                base.LedgerTime
	PredOutputIndex          byte
	Inflation                uint64
	HarvestInflation         uint64
	DisableConsistencyChecks bool
}

func (o *DelegationOutput) MakeDelegationRevokeOutput(par MakeDelegationRevokeOutputParams) (*Output, error) {
	if !par.DisableConsistencyChecks && par.Timestamp.IsSlotBoundary() {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't be a branch transaction")
	}
	if !par.DisableConsistencyChecks && !o.IsUnlockableByTarget(uint32(par.Timestamp.Slot)) {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't be unlocked by target in slot %d", par.Timestamp.Slot)
	}
	if !par.DisableConsistencyChecks && par.HarvestInflation > par.Inflation {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't harvest more inflation (%s) than generate (%s)",
			util.Th(par.HarvestInflation), util.Th(par.Inflation))
	}
	if !par.DisableConsistencyChecks && par.Inflation > L().CalcChainInflationAmountOneSlot(o.ID.Slot(), o.Output.TokenBalance()) {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: wrong inflation amount: %s", util.Th(par.Inflation))
	}

	amounts := []int64{int64(o.Output.TokenBalance() + par.Inflation - par.HarvestInflation), int64(par.Inflation)}
	frozenCoverageVector := o.MakeFrozenCoverageAmountDeltasForRevoking(par.Timestamp)
	amounts = append(amounts, frozenCoverageVector...)

	chainConstraint := NewChainConstraint(o.ChainID, par.PredOutputIndex, 2, o.OriginSlot, o.OriginAmount)
	return NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amounts...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterLock, o.MaxFrozenEpochs, 0))
		o1.MustPushConstraint(chainConstraint.Bytes())
		o1.MustPushConstraint(DelegateLockState{
			LastFrozenEpoch: o.LastFrozenEpoch,
			State:           DelegateLockStateRevoked,
		}.Bytes())
	}), nil
}

func (o *DelegationOutput) LinesHR(prefix ...string) *lines.Lines {
	return o._lines(func(ln *lines.Lines) {
		ln.Append(o.OutputWithChainID.LinesHR(prefix...))
	}, prefix...)
}

func (o *DelegationOutput) LinesSource(prefix ...string) *lines.Lines {
	return o._lines(func(ln *lines.Lines) {
		ln.Append(o.OutputWithChainID.LinesSource(prefix...))
	}, prefix...)
}

func (o *DelegationOutput) _lines(insert func(ln *lines.Lines), prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("---- delegation output ----")
	insert(ret)
	ret.Add("Master: %s", o.MasterLock.Source())
	ret.Add("Target: %s", o.Target.Source())
	ret.Add("MaxFrozenEpochs: %d", o.MaxFrozenEpochs)
	ret.Add("MaxInflationMarginTolerance: %d%%", o.MaxInflationMarginTolerance)
	ret.Add("Frozen until epoch: %d", o.LastFrozenEpoch)
	from, to, total := o.FrozenEpochs(o.Timestamp())
	ret.Add("Frozen epochs: %d - %d (total: %d)", from, to, total)
	from, to, total = o.FrozenSlots(o.Timestamp())
	ret.Add("Frozen slots: %d - %d (total: %d)", from, to, total)
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

// epochOffsetSlots returns slot offset unique for the delegation target chain ChainID.
// Each chain ChainID defines own grid of epochs. It spreads delegation output consumption among sequencers
func (c *DelegationConstants) epochOffsetSlots(targetID base.ChainID) uint32 {
	return binary.BigEndian.Uint32(targetID[:4]) % c.DelegationEpochSlots
}

// CoveredSlotsInCurrentEpoch returns how many slots are covered in the current epoch defined by txSlot and
// taking into account the offset calculated from the target
func (c *DelegationConstants) CoveredSlotsInCurrentEpoch(targetID base.ChainID, txSlot uint32) uint32 {
	_, ret, _, _ := c.EpochLimitsFromSlot(targetID, txSlot)
	return ret
}

func (c *DelegationConstants) FrozenSlotsFromFrozenEpochs(target base.ChainID, txSlot uint32, frozenEpochs byte) uint32 {
	util.Assertf(frozenEpochs > 0, "frozenEpochs > 0")
	return c.CoveredSlotsInCurrentEpoch(target, txSlot) + uint32(frozenEpochs-1)*c.DelegationEpochSlots
}

func (c *DelegationConstants) EpochFromSlot(targetID base.ChainID, txSlot uint32) uint32 {
	ret, _, _, _ := c.EpochLimitsFromSlot(targetID, txSlot)
	return ret
}

func (c *DelegationConstants) EpochLimitsFromSlot(targetID base.ChainID, txSlot uint32) (epoch, coveredInFirstEpoch, firstSlot, lastSlot uint32) {
	offs := c.epochOffsetSlots(targetID)
	epoch = (txSlot + offs) / c.DelegationEpochSlots
	coveredInFirstEpoch = c.DelegationEpochSlots - (txSlot+offs)%c.DelegationEpochSlots
	if txSlot > coveredInFirstEpoch {
		firstSlot = txSlot - coveredInFirstEpoch
	}
	lastSlot = firstSlot + c.DelegationEpochSlots - 1
	return
}

func (c *DelegationConstants) EpochLimits(targetID base.ChainID, txEpoch uint32) (firstSlot, lastSlot uint32) {
	offs := c.epochOffsetSlots(targetID)
	firstSlot = txEpoch*c.DelegationEpochSlots + offs
	lastSlot = c.DelegationEpochSlots*(txEpoch+1) - 1
	return
}

// DiffEpochs return ts1 - ts2 in delegation epochs
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
