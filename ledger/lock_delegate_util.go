package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

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
		Amount                 uint64
		MasterID               base.SpenderID
		Target                 ChainLock
		MaxFrozenEpochs        byte
		RequiredInflationShare uint16
		StartSlot              uint32
	}
)

func MakeDelegationInitOutput(par MakeDelegateInitOutputParams) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmounts(int64(par.Amount))
		o.WithLock(NewDelegateLock(par.Target, par.MasterID, par.MaxFrozenEpochs, par.RequiredInflationShare))
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
	return DelegationOutputFromOutputWithChainIDWithLib(o, L(base.MaxSlot))
}

func DelegationOutputFromOutputWithChainIDWithLib(o *OutputWithChainID, lib *Library) (ret DelegationOutput, ok bool) {
	lock := o.Output.Lock()
	if lock.Name() != DelegateLockName {
		return
	}
	ret.OutputWithChainID = *o
	dLock, ok := lock.(*DelegateLock)
	util.Assertf(ok, "DelegationOutputFromOutputWithChainID: inconsistency")
	ret.DelegateLock = *dLock

	if data, err := o.Output.ConstraintAt(3); err == nil {
		ret.DelegateLockState, err = DelegateLockStateFromBytesWithLib(data, lib)
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
		if dOut.IsInFrozenSlot(txTs.Slot) {
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

func (o *DelegationOutput) IsMarkedOnHold() bool {
	return o.State == DelegateLockStateOnHold
}

// IsInFrozenSlot true means only target can consume it in the slot
func (o *DelegationOutput) IsInFrozenSlot(slot uint32) bool {
	if slot < o.ID.Slot() {
		return false
	}
	if o.IsMarkedOnHold() || !o.IsMarkedFrozen() {
		return false
	}
	lib := L(o.ID.Slot()) // use library from output creation slot
	lastSlot := lib.LastSlotInEpochFromSource(o.Target.ChainID(), o.LastFrozenEpoch)
	return slot <= lastSlot
}

func (o *DelegationOutput) SafeRevocationWindow() (from, to uint32, applicable bool) {
	if o.IsMarkedOnHold() || !o.IsMarkedFrozen() {
		return 0, 0, false
	}
	lib := L(o.ID.Slot()) // use library from output creation slot
	fromSlot := lib.LastSlotInEpochDirect(o.Target.ChainID(), o.LastFrozenEpoch)
	return fromSlot + 1, fromSlot + lib.SafeRevocationSlots, true
}

func (o *DelegationOutput) IsInSafeRevocationWindow(txSlot uint32) bool {
	if from, to, applicable := o.SafeRevocationWindow(); applicable {
		return from <= txSlot && txSlot <= to
	}
	return false
}

// IsUnlockableByTarget true if it is not revoked and not in the safe revocation window
func (o *DelegationOutput) IsUnlockableByTarget(txSlot uint32) bool {
	if o.ID.Timestamp().Slot >= txSlot {
		return false
	}
	if o.IsMarkedOnHold() {
		return false
	}
	if !o.IsMarkedFrozen() {
		return true
	}
	// marked frozen, not revoked
	return !o.IsInSafeRevocationWindow(txSlot)
}

// IsUnlockableByTargetWithReason returns:
//   - false, <reason> if permanently unclockable,
//   - true, <reason> if temporarily unlockable
func (o *DelegationOutput) IsUnlockableByTargetWithReason(txSlot uint32) (valid bool, err error) {
	if o.ID.Timestamp().Slot >= txSlot {
		return true, fmt.Errorf("delegation output %s slot must be 1 or more slots before transaction in slot %d", o.ID.StringShort(), txSlot)
	}
	if o.IsMarkedOnHold() {
		return false, fmt.Errorf("delegation output already revoked")
	}
	if !o.IsMarkedFrozen() {
		return false, fmt.Errorf("delegation output must be in frozen state")
	}
	// marked frozen, not revoked
	if o.IsInSafeRevocationWindow(txSlot) {
		return true, fmt.Errorf("delegation output is in safe revocation window")
	}
	return true, nil
}

func (o *DelegationOutput) IsUnlockableByTargetForFreezing(txSlot uint32) bool {
	return o.IsUnlockableByTarget(txSlot) && !o.IsInFrozenSlot(txSlot)
}

func (o *DelegationOutput) IsUnlockableByMaster(txSlot uint32) bool {
	return !o.IsInFrozenSlot(txSlot)
}

func (o *DelegationOutput) UnfreezeSlot() uint32 {
	if !o.IsMarkedFrozen() {
		return 0
	}
	lib := L(o.ID.Slot()) // use library from output creation slot
	return lib.LastSlotInEpochDirect(o.Target.ChainID(), o.LastFrozenEpoch) + 1
}

func (o *DelegationOutput) InflationOneSlot() uint64 {
	return ChainInflationOneSlot(o.Output.TokenBalance(), o.ID.Slot())
}

// MakeDelegationFreezeOutput constructs successor of the delegation output using maximum possible frozen epochs
func (o *DelegationOutput) MakeDelegationFreezeOutput(txTs base.LedgerTime, freezeUntilEpoch uint32, predOutputIndex byte, advance uint64, disableConsistencyCheck ...bool) (ret *Output, err error) {
	checkConsistency := len(disableConsistencyCheck) == 0 || !disableConsistencyCheck[0]
	if checkConsistency && !o.IsUnlockableByTargetForFreezing(txTs.Slot) {
		err = fmt.Errorf("MakeDelegationFreezeOutput: delegation output cannot be unlocked by the target for freezing")
		return
	}

	if checkConsistency && o.ID.Slot() >= txTs.Slot {
		err = fmt.Errorf("MakeDelegationFreezeOutput: successor timestamp must be at least 1 slot after")
		return
	}
	if checkConsistency && txTs.IsSlotBoundary() {
		err = fmt.Errorf("MakeDelegationFreezeOutput: can't be a branch transaction")
		return
	}

	var frozenEpochs uint32

	lib := L(txTs.Slot)
	txEpoch := lib.EpochFromSlotDirect(o.Target.ChainID(), txTs.Slot)
	if freezeUntilEpoch < txEpoch {
		err = fmt.Errorf("MakeDelegationFreezeOutput: wrong freezeUntilEpoch parameter")
		return
	}
	frozenEpochs = freezeUntilEpoch - txEpoch + 1

	ownTokenBalance := o.Output.TokenBalance() + o.InflationOneSlot()
	successorTokenBalance := ownTokenBalance + advance

	var amountsVector [15]int64
	amountsVector[AmountIndexTokenBalance] = int64(successorTokenBalance)
	amountsVector[AmountIndexInflation] = int64(o.InflationOneSlot())
	for i := byte(0); i < byte(frozenEpochs); i++ {
		amountsVector[AmountIndexFrozenCoverage+i] = int64(successorTokenBalance)
	}
	chainConstraint := NewChainConstraint(o.ChainID, predOutputIndex, 2, o.OriginSlot, o.OriginAmount)

	ret = NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amountsVector[:]...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterID, o.MaxFrozenEpochs, o.RequiredInflationShare))
		o1.MustPushConstraint(chainConstraint.Bytes())
		o1.MustPushConstraint(DelegateLockState{LastFrozenEpoch: freezeUntilEpoch, State: DelegateLockStateFrozen}.Bytes())
	})
	return
}

// ProjectedInflation max inflation that could be generated on the output for a number of frozen epochs
func (o *DelegationOutput) ProjectedInflation(txTs base.LedgerTime, frozenEpochs byte) uint64 {
	if o.ID.Slot() >= txTs.Slot {
		return 0
	}
	lib := L(txTs.Slot)
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), txTs.Slot, frozenEpochs)
	amount := o.Output.TokenBalance() + ChainInflationOneSlot(o.Output.TokenBalance(), o.ID.Slot())
	return ChainInflation(amount, txTs.Slot, frozenSlots)
}

// RequiredMinimumInflationAdvanceOriginal calculates how big advance requires the delegation output for freezing it,
// as calculated from immutable MinInflationAdvancePerFullEpoch value on it
func (o *DelegationOutput) RequiredMinimumInflationAdvanceOriginal(txTs base.LedgerTime, frozenEpochs byte) uint64 {
	lib := L(txTs.Slot)
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), txTs.Slot, frozenEpochs)
	src := fmt.Sprintf("requiredInflationAdvance(u64/%d, u64/%d, u64/%d, u64/%d)",
		frozenSlots,
		txTs.Slot,
		o.Output.TokenBalance(),
		o.RequiredInflationShare,
	)

	resBin, err := L(base.MaxSlot).EvalFromSource(nil, src)
	util.AssertNoError(err)

	return binary.BigEndian.Uint64(resBin)
}

func (o *DelegationOutput) RequiredMinimumInflationAdvanceByFrozenEpochs(txTs base.LedgerTime, frozenEpochs uint32) (uint64, error) {
	lib := L(txTs.Slot)
	if frozenEpochs > lib.MaxFrozenEpochs {
		return 0, fmt.Errorf("wrong frozen epochs")
	}
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(o.Target.ChainID(), txTs.Slot, byte(frozenEpochs))
	inflation := ChainInflation(o.Output.TokenBalance(), txTs.Slot, frozenSlots)
	return (inflation * uint64(o.RequiredInflationShare)) / 1000, nil

}

func (o *DelegationOutput) RequiredMinimumInflationAdvance(txTs base.LedgerTime, freezeUntilEpoch uint32) (uint64, error) {
	lib := L(txTs.Slot)
	epoch := lib.EpochFromSlotDirect(o.Target.ChainID(), txTs.Slot)
	if epoch > freezeUntilEpoch {
		return 0, fmt.Errorf("RequiredMinimumInflationAdvance: wrong freezeUntilEpoch parameter")
	}
	frozenEpochs := freezeUntilEpoch - epoch + 1
	util.Assertf(frozenEpochs <= lib.MaxFrozenEpochs, "frozenEpochs<=dconst.MaxFrozenEpochs")
	return o.RequiredMinimumInflationAdvanceByFrozenEpochs(txTs, frozenEpochs)
}

func (o *DelegationOutput) FreezeUntilMax(ts base.LedgerTime) (freezeUntilEpoch uint32) {
	if o.IsInFrozenSlot(ts.Slot) {
		return
	}
	lib := L(ts.Slot)
	startEpoch := lib.EpochFromSlotDirect(o.Target.ChainID(), ts.Slot)
	freezeUntilEpoch = startEpoch + uint32(o.MaxFrozenEpochs) - 1
	return
}

func (o *DelegationOutput) FrozenEpochs(txTs base.LedgerTime) (from, to, total uint32) {
	if !o.IsInFrozenSlot(txTs.Slot) {
		return
	}
	lib := L(txTs.Slot)
	txEpoch := lib.EpochFromSlotDirect(o.Target.ChainID(), txTs.Slot)
	if txEpoch > o.LastFrozenEpoch {
		return 0, 0, 0
	}
	ret := o.LastFrozenEpoch - txEpoch + 1
	return txEpoch, o.LastFrozenEpoch, ret
}

func (o *DelegationOutput) FrozenSlots(txTs ...base.LedgerTime) (from, to, total uint32) {
	ts := o.ID.Timestamp()
	if len(txTs) > 0 {
		ts = txTs[0]
	}
	to = o.UnfreezeSlot() - 1
	from = ts.Slot
	if to < from {
		return 0, 0, 0
	}
	return from, to, to - from + 1
}

func (o *DelegationOutput) MakeFrozenCoverageAmountDeltasForRevoking(txTs base.LedgerTime) []int64 {
	lib := L(txTs.Slot)
	diffEpochs := lib.DiffEpochs(o.Target.ChainID(), txTs, o.Timestamp())
	util.Assertf(diffEpochs >= 0, "MakeFrozenCoverageAmountDeltasForRevoking: wrong timestamp %s", txTs.String)

	fc := o.Output.Amounts().FrozenCoverageVector()
	ret := make([]int64, lib.MaxFrozenEpochs)
	idx := 0
	for i := diffEpochs; i < len(fc); i++ {
		ret[idx] = -fc[i]
		idx++
	}
	return ret
}

func (o *DelegationOutput) MakeFrozenCoverageAmounts(txTs base.LedgerTime, frozenEpochs byte, tokenBalance uint64) ([]int64, error) {
	lib := L(txTs.Slot)
	mx := byte(lib.MaxFrozenEpochs)
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
	TxTs                     base.LedgerTime
	PredOutputIndex          byte
	Inflation                uint64
	HarvestInflation         uint64
	DisableConsistencyChecks bool
}

// MakeDelegationRevokeOutput error means reason why it cannot be constructed in particular situation
func (o *DelegationOutput) MakeDelegationRevokeOutput(par MakeDelegationRevokeOutputParams) (*Output, error) {
	if !par.DisableConsistencyChecks && !o.IsUnlockableByTarget(uint32(par.TxTs.Slot)) {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't be unlocked by target in slot %d", par.TxTs.Slot)
	}
	if !par.DisableConsistencyChecks && par.HarvestInflation > par.Inflation {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't harvest more inflation (%s) than generate (%s)",
			util.Th(par.HarvestInflation), util.Th(par.Inflation))
	}
	if !par.DisableConsistencyChecks && par.Inflation > ChainInflationOneSlot(o.Output.TokenBalance(), uint32(o.ID.Slot())) {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: wrong inflation amount: %s", util.Th(par.Inflation))
	}

	amounts := []int64{int64(o.Output.TokenBalance() + par.Inflation - par.HarvestInflation), int64(par.Inflation)}
	frozenCoverageVector := o.MakeFrozenCoverageAmountDeltasForRevoking(par.TxTs)
	amounts = append(amounts, frozenCoverageVector...)

	chainConstraint := NewChainConstraint(o.ChainID, par.PredOutputIndex, 2, o.OriginSlot, o.OriginAmount)
	return NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amounts...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterID, o.MaxFrozenEpochs, o.RequiredInflationShare))
		o1.MustPushConstraint(chainConstraint.Bytes())
		o1.MustPushConstraint(DelegateLockState{
			LastFrozenEpoch: 0,
			State:           DelegateLockStateOnHold,
		}.Bytes())
	}), nil
}

func (o *DelegationOutput) LinesDelegationData(prefix ...string) *lines.Lines {
	return o._linesDelegationData(func(ln *lines.Lines) {}, prefix...)
}

func (o *DelegationOutput) LinesHRFull(prefix ...string) *lines.Lines {
	return o._linesDelegationData(func(ln *lines.Lines) {
		ln.Append(o.OutputWithChainID.LinesHR(prefix...))
	}, prefix...)
}

func (o *DelegationOutput) LinesSourceFull(prefix ...string) *lines.Lines {
	return o._linesDelegationData(func(ln *lines.Lines) {
		ln.Add("---- delegation output ----")
		ln.Append(o.OutputWithChainID.LinesSource(prefix...))
	}, prefix...)
}

func (o *DelegationOutput) _linesDelegationData(insertPrefixLines func(ln *lines.Lines), prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	insertPrefixLines(ret)
	currentSlot := SlotNow()
	ret.Add("Master: %s", hex.EncodeToString(o.MasterID[:]))
	ret.Add("Target: %s", o.Target.Source())
	ret.Add("MaxFrozenEpochs: %d", o.MaxFrozenEpochs)
	ret.Add("RequiredInflationShare: %d promille (%.1f%%)", o.RequiredInflationShare, float64(o.RequiredInflationShare)/10)
	if o.IsMarkedFrozen() {
		lib := L(currentSlot) // use library for current slot for display
		_, lastSlot := lib.EpochLimits(o.Target.ChainID(), o.LastFrozenEpoch)
		frozenSlots := int(lastSlot) - int(currentSlot) + 1
		ret.Add("Status: marked frozen")
		ret.Add("   frozen until epoch: %d, %d slots from now", o.LastFrozenEpoch, frozenSlots)
		from, to, total := o.FrozenEpochs(o.Timestamp())
		ret.Add("   frozen epochs: %d - %d (total: %d)", from, to, total)
		from, to, total = o.FrozenSlots(o.Timestamp())
		ret.Add("   frozen slots: %d - %d (total: %d)", from, to, total)
		if o.IsInFrozenSlot(currentSlot) {
			ret.Add("Output is FROZEN in the current slot %d", currentSlot)
			untilUnfreeze := time.Until(ClockTime(base.T(uint32(lastSlot)+1, 0)))
			hr := untilUnfreeze / time.Hour
			minutes := (untilUnfreeze - hr*time.Hour) / time.Minute
			ret.Add("End of freeze is in %d slots, %d hours, %d minutes from now", frozenSlots, hr, minutes)
		} else if o.IsInSafeRevocationWindow(currentSlot) {
			fromSRW, toSRW, applicable := o.SafeRevocationWindow()
			util.Assertf(applicable, "inconsistency")
			endOfSRW := time.Until(ClockTime(base.T(uint32(toSRW)+1, 0)))
			minutes := endOfSRW / time.Minute
			ret.Add("Delegation is the SAFE REVOCATION WINDOW from slot %d to %d, for %d minutes more", fromSRW, toSRW, minutes)
		}
	} else if o.IsMarkedOnHold() {
		ret.Add("Status: on hold")
	} else {
		ret.Add("Status: undef")
	}
	return ret

}

func (o *DelegationOutput) RevocationCompensationEstimate(txSlot uint32) uint64 {
	if !o.IsInFrozenSlot(txSlot) {
		return 0
	}
	unfreeze := o.UnfreezeSlot()
	util.Assertf(txSlot < unfreeze, "txSlot(%d) < unfreeze(%d)", txSlot, unfreeze)

	return ChainInflation(o.Output.TokenBalance(), txSlot, unfreeze-txSlot+1)
}

// EpochOffsetSlotsDirect returns slot offset unique for the delegation target chain ChainID.
// Each chain ChainID defines own grid of epochs. It spreads delegation output consumption among sequencers
func (c *Constants) EpochOffsetSlotsDirect(targetID base.ChainID) uint32 {
	return binary.BigEndian.Uint32(targetID[:4]) % c.DelegationEpochSlots
}

func (c *Constants) EpochOffsetSlotsFromSource(targetID base.ChainID) uint32 {
	src := fmt.Sprintf("delegationEpochOffset(0x%s)", targetID.StringHex())
	resBin, err := L(base.MaxSlot).EvalFromSource(nil, src)
	util.AssertNoError(err)
	return uint32(easyfl_util.MustUint64FromBytes(resBin))
}

// CoveredSlotsInCurrentEpoch returns how many slots are covered in the current epoch defined by txSlot and
// taking into account the offset calculated from the target
func (c *Constants) CoveredSlotsInCurrentEpoch(targetID base.ChainID, slot uint32) uint32 {
	last := c.LastSlotInEpochDirect(targetID, c.EpochFromSlotDirect(targetID, slot))
	util.Assertf(slot <= last, "slot<=last")
	return last - slot + 1
}

func (c *Constants) FrozenSlotsFromFrozenEpochs(target base.ChainID, txSlot uint32, frozenEpochs byte) uint32 {
	util.Assertf(frozenEpochs > 0, "frozenEpochs > 0")
	return c.CoveredSlotsInCurrentEpoch(target, txSlot) + uint32(frozenEpochs-1)*c.DelegationEpochSlots
}

// EpochFromSlotDirect which delegation epoch slot belongs to
func (c *Constants) EpochFromSlotDirect(targetID base.ChainID, slot uint32) (epoch uint32) {
	offs := c.EpochOffsetSlotsDirect(targetID)
	if slot > offs {
		epoch = (slot-offs-1)/c.DelegationEpochSlots + 1
	}
	return
}

// EpochFromSlotFromSource which delegation epoch slot belongs to
func (c *Constants) EpochFromSlotFromSource(targetID base.ChainID, slot uint32) (epoch uint32) {
	src := fmt.Sprintf("delegationEpochFromSlot(0x%s, u32/%d)", targetID.StringHex(), slot)
	resBin, err := L(base.MaxSlot).EvalFromSource(nil, src)
	util.AssertNoError(err)
	return uint32(easyfl_util.MustUint64FromBytes(resBin))
}

func (c *Constants) EpochLimits(targetID base.ChainID, epoch uint32) (firstSlot, lastSlot uint32) {
	offs := c.EpochOffsetSlotsDirect(targetID)
	lastSlot = epoch*c.DelegationEpochSlots + offs
	if epoch > 0 {
		firstSlot = lastSlot - c.DelegationEpochSlots + 1
	}
	return
}

func (c *Constants) LastSlotInEpochDirect(targetID base.ChainID, epoch uint32) (lastSlot uint32) {
	_, lastSlot = c.EpochLimits(targetID, epoch)
	return
}

func (c *Constants) LastSlotInEpochFromSource(targetID base.ChainID, epoch uint32) (lastSlot uint32) {
	src := fmt.Sprintf("lastSlotInDelegationEpoch(0x%s, u32/%d)", targetID.StringHex(), epoch)
	resBin, err := L(base.MaxSlot).EvalFromSource(nil, src)
	util.AssertNoError(err)
	return uint32(easyfl_util.MustUint64FromBytes(resBin))
}

// DiffEpochs return ts1 - ts2 in delegation epochs
func (c *Constants) DiffEpochs(targetID base.ChainID, ts1, ts2 base.LedgerTime) int {
	epoch1 := c.EpochFromSlotDirect(targetID, ts1.Slot)
	epoch2 := c.EpochFromSlotDirect(targetID, ts2.Slot)
	return int(epoch1) - int(epoch2)
}

func (c *Constants) AdjustFrozenCoverageVector(targetID base.ChainID, vect []int64, predTs, succTs base.LedgerTime) []int64 {
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
