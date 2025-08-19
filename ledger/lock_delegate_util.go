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
	DelegateOutput struct {
		OutputWithChainID
		DelegateLock
		DelegateLockState
	}

	MakeDelegateInitOutputParams struct {
		Amount                      uint64
		Master                      Accountable
		Target                      ChainLock
		MaxFreezeSlots              uint16
		MinInflationAdvancePerEpoch uint64
		StartSlot                   base.Slot
	}
	MakeDelegateSuccessorOutputParams struct {
		Timestamp                   base.LedgerTime
		PredTimestamp               base.LedgerTime
		FreezeUntilEpoch            uint32
		PredOutputIndex             byte
		Inflation                   uint64
		HarvestInflation            uint64
		MinInflationAdvancePerEpoch uint64
		DisableConsistencyChecks    bool
	}
)

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

// Coverage returns for the consumed output in the transaction with specified timestamp
// - coverage presented by the output, which includes frozen coverage part
// - frozen part separately
func Coverage(o *Output, oid base.OutputID, txTs base.LedgerTime) (coverage, frozen uint64) {
	outChain, isChain := AsOutputWithChainID(o, oid)
	if !isChain {
		// if not a chain, coverage is equal to the toke balance
		return o.TokenBalance(), 0
	}

	if dOut, isDelegate := DelegateOutputFromOutputWithChainID(&outChain); isDelegate {
		if dOut.IsFrozen(txTs.Slot) {
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
	if !par.DisableConsistencyChecks && par.Inflation > L().CalcChainInflationAmountOneSlot(o.ID.Slot(), o.Output.TokenBalance()) {
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
	ret.Append(o.OutputWithChainID.LinesSource("   "))
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
