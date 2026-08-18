package ledger

import (
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
		Amount               uint64
		MasterID             base.HolderID
		Target               base.ChainID
		RequiredInflationCut uint16
		StartSlot            uint32
	}
)

func MakeDelegationInitOutput(par MakeDelegateInitOutputParams) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmounts(int64(par.Amount))
		o.WithLock(NewDelegateLock(par.Target, par.MasterID, par.RequiredInflationCut))
		o.PutConstraint(NewChainOrigin(par.StartSlot).Bytes(), ConstraintIndexChain)
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

	// DelegateLockState lives at the LAST tuple position (Option C of
	// claude/delegation_epoch_params.md). A plain delegation has 5
	// elements with the state at index 4; a delegated foundry has
	// 6 or 7 elements with foundry / foundryPolicy between chain (3)
	// and the state.
	n := o.Output.NumElements()
	if n > 0 {
		if data, err := o.Output.ConstraintAt(byte(n - 1)); err == nil {
			ret.DelegateLockState, err = DelegateLockStateFromBytesWithLib(data, lib)
		}
	}
	return
}

// Coverage returns the coverage presented by the consumed output in a
// transaction with the given timestamp. For a sequencer chain output the
// coverage includes the (epoch-adjusted) frozen-coverage part carried on the
// sequencer; frozen delegation outputs present zero coverage.
func Coverage(o *Output, oid base.OutputID, txTs base.LedgerTime) (coverage uint64) {
	outChain, isChain := AsOutputWithChainID(o, oid)
	if !isChain {
		// if not a chain, coverage is equal to the toke balance
		return o.TokenBalance()
	}

	if dOut, isDelegate := DelegationOutputFromOutputWithChainID(&outChain); isDelegate {
		if dOut.IsInFrozenSlot(txTs.Slot) {
			// delegated frozen outputs have zero coverage
			return 0
		}
		// delegated not-frozen output coverage is equal to the token balance
		return o.TokenBalance()
	}

	// otherwise, it is token balance plus adjusted frozen coverage stored in the chained output
	return o.TokenBalance() + uint64(outChain.AdjustedFrozenCoverage(txTs))
}

// EpochSlots and TargetMaxFrozenEpochs used to be inlined into every
// delegation lock as copies of the target chain's parameters. They are ledger
// constants now, the same for every sequencer and every delegation, so they are
// read off the library version that applies to this output.
func (o *DelegationOutput) EpochSlots() uint32 {
	return L(o.ID.Slot()).DelegationEpochSlots
}

func (o *DelegationOutput) TargetMaxFrozenEpochs() byte {
	return byte(L(o.ID.Slot()).DelegationMaxFrozenEpochs)
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
	lastSlot := lib.LastSlotInEpochFromSource(o.Target, o.LastFrozenEpoch, o.EpochSlots())
	return slot <= lastSlot
}

func (o *DelegationOutput) SafeRevocationWindow() (from, to uint32, applicable bool) {
	if o.IsMarkedOnHold() || !o.IsMarkedFrozen() {
		return 0, 0, false
	}
	lib := L(o.ID.Slot()) // use library from output creation slot
	fromSlot := lib.LastSlotInEpochDirect(o.Target, o.LastFrozenEpoch, o.EpochSlots())
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
	return lib.LastSlotInEpochDirect(o.Target, o.LastFrozenEpoch, o.EpochSlots()) + 1
}

// AllowanceCeiling is the largest allowance ensureStopDelegation accepts for
// this delegation: uncut chain inflation over the remaining frozen span,
// measured from the output's own slot. Mirrors _projectedCompensation in
// ensure.easyfl — anchored to the input slot rather than the transaction
// slot so wallet and constraint agree regardless of when the request is
// picked up.
func (o *DelegationOutput) AllowanceCeiling() uint64 {
	if !o.IsMarkedFrozen() {
		return 0
	}
	lib := L(o.ID.Slot())
	lastSlot := lib.LastSlotInEpochDirect(o.Target, o.LastFrozenEpoch, o.EpochSlots())
	if lastSlot < o.ID.Slot() {
		// frozen span already run out; nothing left to compensate for
		return 0
	}
	projected := lib.ChainInflationMultiStep(o.Output.TokenBalance(), o.ID.Slot(), lastSlot-o.ID.Slot()+1)
	// at the share actually advanced: stopping early returns the unearned part
	// of the advance, not the target's foregone cut. Mirrors
	// _projectedCompensation in ensure.easyfl.
	return (projected * uint64(o.AdvanceShare)) / 1000
}

func (o *DelegationOutput) InflationOneSlot() uint64 {
	return L(base.MaxSlot).ChainInflationOneSlot(o.Output.TokenBalance(), o.ID.Slot())
}

// MakeDelegationFreezeOutput constructs successor of the delegation output using maximum possible frozen epochs.
// advanceShare is the promille of the projected inflation the target advances;
// the advance itself is derived from it here, so it matches the constraint's
// own arithmetic by construction, and the share is pinned onto the successor's
// delegateLockState for the early-stop unwind to read.
func (o *DelegationOutput) MakeDelegationFreezeOutput(txTs base.LedgerTime, freezeUntilEpoch uint32, predOutputIndex byte, advanceShare uint16, disableConsistencyCheck ...bool) (ret *Output, err error) {
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
	txEpoch := lib.EpochFromSlotDirect(o.Target, txTs.Slot, o.EpochSlots())
	if freezeUntilEpoch < txEpoch {
		err = fmt.Errorf("MakeDelegationFreezeOutput: wrong freezeUntilEpoch parameter")
		return
	}
	frozenEpochs = freezeUntilEpoch - txEpoch + 1

	advance := o.AdvanceForShare(txTs, frozenEpochs, advanceShare)
	ownTokenBalance := o.Output.TokenBalance() + o.InflationOneSlot()
	successorTokenBalance := ownTokenBalance + advance

	// Per Phase 3 of delegation_epoch_params, the frozen-coverage vector is
	// sized by this delegation's target maxFrozenEpochs (inlined as
	// o.TargetMaxFrozenEpochs()), not by the library-wide default.
	amountsVector := make([]int64, int(AmountIndexFrozenCoverage)+int(o.TargetMaxFrozenEpochs()))
	amountsVector[AmountIndexTokenBalance] = int64(successorTokenBalance)
	amountsVector[AmountIndexInflation] = int64(o.InflationOneSlot())
	for i := byte(0); i < byte(frozenEpochs); i++ {
		amountsVector[AmountIndexFrozenCoverage+i] = int64(successorTokenBalance)
	}
	chainConstraint := NewChainConstraint(o.ChainID, predOutputIndex, o.OriginSlot, o.CumulativeChainInflation+o.InflationOneSlot(), o.CumulativeBranchBonus, o.TransitionCounter+1, o.BranchCounter)

	ret = NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amountsVector[:]...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterID, o.RequiredInflationCut))
		o1.PutConstraint(chainConstraint.Bytes(), ConstraintIndexChain)
		o1.MustPushConstraint(DelegateLockState{
			LastFrozenEpoch: freezeUntilEpoch,
			State:           DelegateLockStateFrozen,
			AdvanceShare:    advanceShare,
		}.Bytes())
	})
	return
}

// ProjectedInflation max inflation that could be generated on the output for a number of frozen epochs
func (o *DelegationOutput) ProjectedInflation(txTs base.LedgerTime, frozenEpochs byte) uint64 {
	if o.ID.Slot() >= txTs.Slot {
		return 0
	}
	lib := L(txTs.Slot)
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(o.Target, txTs.Slot, o.EpochSlots(), frozenEpochs)
	amount := o.Output.TokenBalance() + lib.ChainInflationOneSlot(o.Output.TokenBalance(), o.ID.Slot())
	return lib.ChainInflationMultiStep(amount, txTs.Slot, frozenSlots)
}

// AdvanceForShare is the advance the target must deliver to freeze this
// delegation for frozenEpochs at the given promille share. Mirrors
// requiredInflationAdvance in lock_delegate.easyfl: the constraint requires
// equality, so both sides must round identically and both must project from
// the CONSUMED balance.
func (o *DelegationOutput) AdvanceForShare(txTs base.LedgerTime, frozenEpochs uint32, share uint16) uint64 {
	lib := L(txTs.Slot)
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(o.Target, txTs.Slot, o.EpochSlots(), byte(frozenEpochs))
	inflation := lib.ChainInflationMultiStep(o.Output.TokenBalance(), txTs.Slot, frozenSlots)
	return (inflation * uint64(share)) / 1000
}

func (o *DelegationOutput) RequiredMinimumInflationAdvanceByFrozenEpochs(txTs base.LedgerTime, frozenEpochs uint32) (uint64, error) {
	if frozenEpochs > uint32(o.TargetMaxFrozenEpochs()) {
		return 0, fmt.Errorf("wrong frozen epochs")
	}
	return o.AdvanceForShare(txTs, frozenEpochs, o.RequiredInflationCut), nil

}

func (o *DelegationOutput) RequiredMinimumInflationAdvance(txTs base.LedgerTime, freezeUntilEpoch uint32) (uint64, error) {
	lib := L(txTs.Slot)
	epoch := lib.EpochFromSlotDirect(o.Target, txTs.Slot, o.EpochSlots())
	if epoch > freezeUntilEpoch {
		return 0, fmt.Errorf("RequiredMinimumInflationAdvance: wrong freezeUntilEpoch parameter")
	}
	frozenEpochs := freezeUntilEpoch - epoch + 1
	// reachable from a caller-supplied freezeUntilEpoch, so an error rather
	// than an assert. The ledger enforces the same bound as
	// frozen_epochs_cannot_exceed_maximum.
	if frozenEpochs > uint32(o.TargetMaxFrozenEpochs()) {
		return 0, fmt.Errorf("RequiredMinimumInflationAdvance: frozen epochs (%d) exceed maximum %d",
			frozenEpochs, o.TargetMaxFrozenEpochs())
	}
	return o.RequiredMinimumInflationAdvanceByFrozenEpochs(txTs, frozenEpochs)
}

func (o *DelegationOutput) FreezeUntilMax(ts base.LedgerTime) (freezeUntilEpoch uint32) {
	if o.IsInFrozenSlot(ts.Slot) {
		return
	}
	lib := L(ts.Slot)
	startEpoch := lib.EpochFromSlotDirect(o.Target, ts.Slot, o.EpochSlots())
	freezeUntilEpoch = startEpoch + uint32(o.TargetMaxFrozenEpochs()) - 1
	return
}

func (o *DelegationOutput) FrozenEpochs(txTs base.LedgerTime) (from, to, total uint32) {
	if !o.IsInFrozenSlot(txTs.Slot) {
		return
	}
	lib := L(txTs.Slot)
	txEpoch := lib.EpochFromSlotDirect(o.Target, txTs.Slot, o.EpochSlots())
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
	diffEpochs := lib.DiffEpochs(o.Target, txTs, o.Timestamp(), o.EpochSlots())
	util.Assertf(diffEpochs >= 0, "MakeFrozenCoverageAmountDeltasForRevoking: wrong timestamp %s", txTs.String)

	fc := o.Output.Amounts().FrozenCoverageVector(o.TargetMaxFrozenEpochs())
	ret := make([]int64, o.TargetMaxFrozenEpochs())
	idx := 0
	for i := diffEpochs; i < len(fc); i++ {
		ret[idx] = -fc[i]
		idx++
	}
	return ret
}

func (o *DelegationOutput) MakeFrozenCoverageAmounts(txTs base.LedgerTime, frozenEpochs byte, tokenBalance uint64) ([]int64, error) {
	mx := o.TargetMaxFrozenEpochs()
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
	TxTs             base.LedgerTime
	PredOutputIndex  byte
	Inflation        uint64
	HarvestInflation uint64
	// TakeFromBalance is the askstop compensation charged to the delegation
	// itself rather than to the delegator's own tokens. Must be covered by an
	// allowance on the request output, otherwise delegateLock rejects the
	// resulting balance decrease.
	TakeFromBalance          uint64
	DisableConsistencyChecks bool
}

// MakeDelegationRevokeOutput error means reason why it cannot be constructed in particular situation
func (o *DelegationOutput) MakeDelegationRevokeOutput(par MakeDelegationRevokeOutputParams) (*Output, error) {
	if !par.DisableConsistencyChecks && !o.IsUnlockableByTarget(par.TxTs.Slot) {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't be unlocked by target in slot %d", par.TxTs.Slot)
	}
	if !par.DisableConsistencyChecks && par.HarvestInflation > par.Inflation {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't harvest more inflation (%s) than generate (%s)",
			util.Th(par.HarvestInflation), util.Th(par.Inflation))
	}
	if !par.DisableConsistencyChecks && par.Inflation > L(par.TxTs.Slot).ChainInflationOneSlot(o.Output.TokenBalance(), o.ID.Slot()) {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: wrong inflation amount: %s", util.Th(par.Inflation))
	}
	remaining := o.Output.TokenBalance() + par.Inflation - par.HarvestInflation
	if par.TakeFromBalance > remaining {
		return nil, fmt.Errorf("MakeDelegationRevokeOutput: can't take %s out of a balance of %s",
			util.Th(par.TakeFromBalance), util.Th(remaining))
	}

	// the frozen-coverage bound cell is left to NewAmounts, which derives it
	amounts := []int64{int64(remaining - par.TakeFromBalance), int64(par.Inflation), 0}
	frozenCoverageVector := o.MakeFrozenCoverageAmountDeltasForRevoking(par.TxTs)
	amounts = append(amounts, frozenCoverageVector...)

	chainConstraint := NewChainConstraint(o.ChainID, par.PredOutputIndex, o.OriginSlot, o.CumulativeChainInflation+par.Inflation, o.CumulativeBranchBonus, o.TransitionCounter+1, o.BranchCounter)
	return NewOutput(func(o1 *OutputBuilder) {
		o1.WithAmounts(amounts...)
		o1.WithLock(NewDelegateLock(o.Target, o.MasterID, o.RequiredInflationCut))
		o1.PutConstraint(chainConstraint.Bytes(), ConstraintIndexChain)
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
	ret.Add("Target: %s", o.Target.String())
	ret.Add("MaxFrozenEpochs: %d", o.TargetMaxFrozenEpochs())
	ret.Add("RequiredInflationCut: %d promille (%.1f%%)", o.RequiredInflationCut, float64(o.RequiredInflationCut)/10)
	if o.IsMarkedFrozen() {
		lib := L(currentSlot) // use library for current slot for display
		_, lastSlot := lib.EpochLimits(o.Target, o.LastFrozenEpoch, o.EpochSlots())
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

// Per-target delegation epoch helpers. As of Phase 3 of
// claude/delegation_epoch_params.md, these no longer use the global
// constants from Constants — every helper takes the target chain's
// epochSlots (and, where vector-sized, maxFrozenEpochs) explicitly.
// They remain methods on *Constants only for namespacing.

// The arithmetic helpers (EpochOffsetSlotsDirect, EpochFromSlotDirect,
// EpochLimits, LastSlotInEpochDirect, CoveredSlotsInCurrentEpoch,
// FrozenSlotsFromFrozenEpochs, DiffEpochs, AdjustFrozenCoverageVector)
// live on txbuildercore.Constants — promoted onto ledger.Library via
// the embedded *txbuildercore.Constants. The three *FromSource
// variants below cross-check the same math against the on-chain
// EasyFL definitions; they need the eval engine and therefore sit on
// *Library directly.

func (lib *Library) EpochOffsetSlotsFromSource(targetID base.ChainID, epochSlots uint32) uint32 {
	src := fmt.Sprintf("delegationEpochOffset(0x%s, u32/%d)", targetID.StringHex(), epochSlots)
	resBin, err := lib.Library.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return uint32(easyfl_util.MustUint64FromBytes(resBin))
}

// EpochFromSlotFromSource which delegation epoch slot belongs to
// (evaluated via the on-chain definition).
func (lib *Library) EpochFromSlotFromSource(targetID base.ChainID, slot, epochSlots uint32) uint32 {
	src := fmt.Sprintf("delegationEpochFromSlot(0x%s, u32/%d, u32/%d)", targetID.StringHex(), slot, epochSlots)
	resBin, err := lib.Library.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return uint32(easyfl_util.MustUint64FromBytes(resBin))
}

func (lib *Library) LastSlotInEpochFromSource(targetID base.ChainID, epoch, epochSlots uint32) uint32 {
	src := fmt.Sprintf("lastSlotInDelegationEpoch(0x%s, u32/%d, u32/%d)", targetID.StringHex(), epoch, epochSlots)
	resBin, err := lib.Library.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return uint32(easyfl_util.MustUint64FromBytes(resBin))
}
