package txbuildercore

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
)

// DelegateLockState byte values carried by the
// delegateLockState(_, state) constraint. Mirror
// ledger.DelegateLockStateFrozen / .DelegateLockStateOnHold; the
// default zero value (DelegateLockStateNormal) is not exported on
// either side because it has no special semantics — every state
// other than Frozen/OnHold is "normal".
const (
	DelegateLockStateFrozen byte = 1
	DelegateLockStateOnHold byte = 2
)

// Delegation constraint symbols + source templates. The templates
// mirror ledger/lock_delegate.go and ledger/delegation_params.go
// byte-for-byte — the wallet must emit bytes the server's parser
// accepts.
const (
	// DelegateLockName is the canonical 4-arg lock at output slot 2
	// of a delegation output.
	DelegateLockName = "delegateLock"

	// delegateLockTemplate's first verb is intentionally %s — the
	// wallet emits either "0x" (when maxFrozenEpochs is 0 or equal
	// to target) or the uint8 literal otherwise. Mirrors
	// ledger.DelegateLock.Source().
	delegateLockTemplate = DelegateLockName + "(%s, z16/%d, z32/%d, %d)"

	// DelegateLockStateName is the 2-arg state-carrier constraint at
	// output slot 4 of a delegation output.
	DelegateLockStateName = "delegateLockState"

	delegateLockStateTemplate = DelegateLockStateName + "(z32/%d, %d)"

	// DelegationParamsName is the 2-arg constraint at slot 6 of a
	// chain output opting in to accept delegations.
	DelegationParamsName = "delegationParams"

	delegationParamsTemplate = DelegationParamsName + "(z32/%d, %d)"
)

// NewDelegateLockBytecode emits the 4-arg delegateLock constraint
// bytecode (slot 2 of a delegation output).
//
// The (master, target) pair lives in the index-values tuple at slot
// 1 — see how proxi/node_cmd/delegate composes the full output. The
// lock bytecode itself carries only the 4 policy args.
//
// First arg is emitted as the literal "0x" when maxFrozenEpochs == 0
// or maxFrozenEpochs == targetMaxFrozenEpochs (the "use target's"
// shorthand the validator accepts); otherwise the byte literal of
// maxFrozenEpochs. Mirrors ledger.DelegateLock.Source().
func (l *Library[any]) NewDelegateLockBytecode(
	maxFrozenEpochs byte,
	requiredInflationShare uint16,
	epochSlots uint32,
	targetMaxFrozenEpochs byte,
) ([]byte, error) {
	m := "0x"
	if maxFrozenEpochs != 0 && maxFrozenEpochs != targetMaxFrozenEpochs {
		m = fmt.Sprintf("%d", maxFrozenEpochs)
	}
	src := fmt.Sprintf(delegateLockTemplate, m, requiredInflationShare, epochSlots, targetMaxFrozenEpochs)
	return l.CompileExpression(src)
}

// NewDelegateLockState emits the 2-arg delegateLockState constraint
// bytecode (slot 4 of a delegation output). At chain origin the
// wallet uses (0, 0) — the zero state. The validator updates it on
// every transit; the wallet just needs to write the zero value.
func (l *Library[any]) NewDelegateLockState(lastFrozenEpoch uint32, state byte) ([]byte, error) {
	src := fmt.Sprintf(delegateLockStateTemplate, lastFrozenEpoch, state)
	return l.CompileExpression(src)
}

// NewDelegationParams emits the 2-arg delegationParams constraint
// bytecode (slot 6 of a chain output that opts in to accept
// delegations). Attachable only at chain origin; pinned across every
// chain transit via selfImmutableOnSuccessorIndex.
func (l *Library[any]) NewDelegationParams(epochSlots uint32, maxFrozenEpochs byte) ([]byte, error) {
	src := fmt.Sprintf(delegationParamsTemplate, epochSlots, maxFrozenEpochs)
	return l.CompileExpression(src)
}

// DelegationParamsView is the wallet-side decoded form of the 2-arg
// delegationParams constraint. Mirrors ledger.DelegationParams.
type DelegationParamsView struct {
	EpochSlots      uint32
	MaxFrozenEpochs byte
}

// ParseDelegationParams decodes a delegationParams constraint
// bytecode. Pure byte parse — no eval. Mirrors
// ledger.DelegationParamsFromBytesWithLib.
func (l *Library[any]) ParseDelegationParams(data []byte) (*DelegationParamsView, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, fmt.Errorf("ParseDelegationParams: %w", err)
	}
	if sym != DelegationParamsName {
		return nil, fmt.Errorf("ParseDelegationParams: expected %s, got %s", DelegationParamsName, sym)
	}
	e0, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil || e0 > 0xFFFFFFFF {
		return nil, fmt.Errorf("ParseDelegationParams: epochSlots out of range: %v", err)
	}
	e1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil || e1 >= 256 {
		return nil, fmt.Errorf("ParseDelegationParams: maxFrozenEpochs out of range: %v", err)
	}
	return &DelegationParamsView{
		EpochSlots:      uint32(e0),
		MaxFrozenEpochs: byte(e1),
	}, nil
}

// DelegationInitOutputParams describes the inputs for
// NewDelegationInitOutput. Mirrors ledger.MakeDelegateInitOutputParams
// field-for-field.
type DelegationInitOutputParams struct {
	Amount                 uint64
	MasterID               base.HolderID
	Target                 base.ChainID
	MaxFrozenEpochs        byte
	RequiredInflationShare uint16
	StartSlot              uint32
	// EpochSlots and TargetMaxFrozenEpochs are copies of the target
	// chain's delegationParams. See claude/delegation_epoch_params.md.
	EpochSlots            uint32
	TargetMaxFrozenEpochs byte
}

// NewDelegationInitOutput composes a chain-origin delegation output:
//
//	slot 0 (amounts):       trimmed-uint64 encoding of `par.Amount`
//	                        (no frozen-coverage cells at origin)
//	slot 1 (index-values):  tuple [masterID, target] (master-first)
//	slot 2 (lock):          delegateLock bytecode with the 4 policy args
//	slot 3 (chain):         chain-origin constraint for `par.StartSlot`
//	slot 4 (lock state):    delegateLockState{0, 0} — zero / no freeze
//
// Mirrors ledger.MakeDelegationInitOutput byte-for-byte (verified by
// the byte-identity test in helpers_delegate_test.go).
func (l *Library[any]) NewDelegationInitOutput(par DelegationInitOutputParams) (*Output, error) {
	delegateLockBin, err := l.NewDelegateLockBytecode(par.MaxFrozenEpochs, par.RequiredInflationShare, par.EpochSlots, par.TargetMaxFrozenEpochs)
	if err != nil {
		return nil, err
	}
	chainOriginBin, err := l.NewChainOrigin(par.StartSlot)
	if err != nil {
		return nil, err
	}
	stateBin, err := l.NewDelegateLockState(0, 0)
	if err != nil {
		return nil, err
	}

	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(par.Amount), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{par.MasterID[:], par.Target[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(delegateLockBin, ConstraintIndexLock)
	b.PutConstraint(chainOriginBin, ConstraintIndexChain)
	b.MustPushConstraint(stateBin)
	return b.Output(), nil
}

// DelegateLockStateView is the wallet-side decoded form of the
// delegateLockState(lastFrozenEpoch, state) constraint.
type DelegateLockStateView struct {
	LastFrozenEpoch uint32
	State           byte // 0 = normal, 1 = Frozen, 2 = OnHold
}

// ParseDelegateLockState decodes a delegateLockState constraint
// bytecode. Pure byte parse via the wallet library — no eval.
func (l *Library[any]) ParseDelegateLockState(data []byte) (DelegateLockStateView, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return DelegateLockStateView{}, fmt.Errorf("ParseDelegateLockState: %w", err)
	}
	if sym != DelegateLockStateName {
		return DelegateLockStateView{}, fmt.Errorf("ParseDelegateLockState: expected %s, got %s", DelegateLockStateName, sym)
	}
	frBytes := easyfl.StripDataPrefix(args[0])
	fr, err := easyfl_util.Uint32FromBytes(frBytes)
	if err != nil {
		return DelegateLockStateView{}, fmt.Errorf("ParseDelegateLockState: arg 0: %w", err)
	}
	stBytes := easyfl.StripDataPrefix(args[1])
	if len(stBytes) != 1 {
		return DelegateLockStateView{}, fmt.Errorf("ParseDelegateLockState: arg 1 must be 1 byte, got %d", len(stBytes))
	}
	return DelegateLockStateView{LastFrozenEpoch: fr, State: stBytes[0]}, nil
}

// DelegationOutputView is the wallet-side decoded form of a
// delegate-lock output, carrying enough to compute the frozen-slot
// UX guard and the standard status-string display. The
// DelegateLockState constraint is read from the LAST output element
// (Option C of claude/delegation_epoch_params.md); a plain
// delegation has it at element index 4, a delegated foundry has it
// at 5 or 6.
type DelegationOutputView struct {
	OriginSlot             uint32        // output creation slot (oid.Slot())
	ChainID                base.ChainID  // delegation's own chainID (computed for origin)
	MasterID               base.HolderID // index-values[0]
	Target                 base.ChainID  // index-values[1]
	MaxFrozenEpochs        byte          // delegateLock arg 0 (caller-supplied cap)
	RequiredInflationShare uint16        // delegateLock arg 1 (z16 promille)
	EpochSlots             uint32        // delegateLock arg 2 (z32/epochSlots)
	LastFrozenEpoch        uint32        // delegateLockState arg 0
	State                  byte          // delegateLockState arg 1 (0 / Frozen / OnHold)
	// Chain-constraint metadata (mirrors ChainConstraintView fields).
	// Useful for the standard status-line display + annualized
	// inflation estimate.
	ChainOriginSlot          uint32 // chain constraint arg 2 (z32)
	TransitionCounter        uint64 // chain constraint arg 5 (z64)
	BranchCounter            uint32 // chain constraint arg 6 (z32)
	CumulativeChainInflation uint64 // chain constraint arg 3 (z64)
	CumulativeBranchBonus    uint64 // chain constraint arg 4 (z64)
}

// ParseDelegationOutput decodes a parsed output if it carries a
// delegateLock at slot 2. Returns (view, true, nil) on success;
// (nil, false, nil) when the output is not a delegation. Errors only
// on malformed bytes that look like delegation (right symbol, wrong
// arg shape).
func (l *Library[any]) ParseDelegationOutput(o *Output, oid base.OutputID) (*DelegationOutputView, bool, error) {
	if o == nil || o.NumElements() < 5 {
		// every delegation output has at least 5 elements (amounts,
		// index-values, lock, chain, state).
		return nil, false, nil
	}
	lockBin, err := o.ConstraintAt(ConstraintIndexLock)
	if err != nil {
		return nil, false, err
	}
	sym, _, args, err := l.ParseBytecodeOneLevel(lockBin, 4)
	if err != nil || sym != DelegateLockName {
		return nil, false, nil
	}
	if len(args) < 4 {
		return nil, false, fmt.Errorf("ParseDelegationOutput: delegateLock with %d args, expected 4", len(args))
	}
	// arg 0: delegator's chosen max frozen epochs; 0 means "use target's"
	//        (arg 3). Mirrors ledger.DelegateLockFromBytesWithLib.
	a0, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, false, fmt.Errorf("ParseDelegationOutput: maxFrozenEpochs: %w", err)
	}
	a3, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, false, fmt.Errorf("ParseDelegationOutput: targetMaxFrozenEpochs: %w", err)
	}
	maxFrozenEpochs := byte(a0)
	if maxFrozenEpochs == 0 {
		maxFrozenEpochs = byte(a3)
	}
	// arg 1: required inflation share (z16 promille)
	requiredShare64, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil {
		return nil, false, fmt.Errorf("ParseDelegationOutput: requiredInflationShare: %w", err)
	}
	requiredShare := uint16(requiredShare64)
	// arg 2: epochSlots (z32)
	epochSlotsBytes := easyfl.StripDataPrefix(args[2])
	epochSlots, err := easyfl_util.Uint32FromBytes(epochSlotsBytes)
	if err != nil {
		return nil, false, fmt.Errorf("ParseDelegationOutput: epochSlots: %w", err)
	}

	ivBin, err := o.ConstraintAt(ConstraintIndexIndexValues)
	if err != nil {
		return nil, false, err
	}
	vals, err := DecodeIndexValuesTuple(ivBin)
	if err != nil {
		return nil, false, err
	}
	if len(vals) < 2 || len(vals[0]) != 32 || len(vals[1]) != 32 {
		return nil, false, fmt.Errorf("ParseDelegationOutput: master/target IDs not at index-values[0..1]")
	}
	var master base.HolderID
	copy(master[:], vals[0])
	var target base.ChainID
	copy(target[:], vals[1])

	// DelegateLockState lives at the LAST element (Option C).
	n := o.NumElements()
	stateBin, err := o.ConstraintAt(byte(n - 1))
	if err != nil {
		return nil, false, err
	}
	state, err := l.ParseDelegateLockState(stateBin)
	if err != nil {
		return nil, false, err
	}

	// Chain constraint at slot 3 — full parse for metadata. For a
	// transit chain arg 0 carries the explicit chainID; for an origin
	// it's NilChainID and the real chainID is blake2b(oid).
	chainBin, err := o.ConstraintAt(ConstraintIndexChain)
	if err != nil {
		return nil, false, err
	}
	cc, err := l.ParseChainConstraint(chainBin)
	if err != nil {
		return nil, false, err
	}
	chainID := cc.ChainID
	if chainID == base.NilChainID {
		chainID = base.MakeOriginChainID(oid)
	}

	return &DelegationOutputView{
		OriginSlot:               oid.Slot(),
		ChainID:                  chainID,
		MasterID:                 master,
		Target:                   target,
		MaxFrozenEpochs:          maxFrozenEpochs,
		RequiredInflationShare:   requiredShare,
		EpochSlots:               epochSlots,
		LastFrozenEpoch:          state.LastFrozenEpoch,
		State:                    state.State,
		ChainOriginSlot:          cc.OriginSlot,
		TransitionCounter:        cc.TransitionCounter,
		BranchCounter:            cc.BranchCounter,
		CumulativeChainInflation: cc.CumulativeChainInflation,
		CumulativeBranchBonus:    cc.CumulativeBranchBonus,
	}, true, nil
}

// ParseChainConstraintChainID extracts the chainID of a chain output,
// resolving the origin case (NilChainID → blake2b(oid)). Thin wrapper
// over ParseChainConstraint for sites that need only the chainID.
func (l *Library[any]) ParseChainConstraintChainID(chainBin []byte, oid base.OutputID) (base.ChainID, error) {
	cc, err := l.ParseChainConstraint(chainBin)
	if err != nil {
		return base.NilChainID, err
	}
	if cc.ChainID == base.NilChainID {
		return base.MakeOriginChainID(oid), nil
	}
	return cc.ChainID, nil
}

// IsInFrozenSlot reports whether the delegation output is in a frozen
// slot at txSlot — the master cannot unlock it then. Mirrors
// ledger.DelegationOutput.IsInFrozenSlot. Pure arithmetic against the
// Constants epoch grid; no library eval.
func (v *DelegationOutputView) IsInFrozenSlot(txSlot uint32, c *Constants) bool {
	if txSlot < v.OriginSlot {
		return false
	}
	if v.State != DelegateLockStateFrozen {
		return false
	}
	return txSlot <= c.LastSlotInEpochDirect(v.Target, v.LastFrozenEpoch, v.EpochSlots)
}

// UnfreezeSlot returns the first slot at which the delegation is no
// longer frozen, or 0 when the output isn't marked frozen. Mirrors
// ledger.DelegationOutput.UnfreezeSlot.
func (v *DelegationOutputView) UnfreezeSlot(c *Constants) uint32 {
	if v.State != DelegateLockStateFrozen {
		return 0
	}
	return c.LastSlotInEpochDirect(v.Target, v.LastFrozenEpoch, v.EpochSlots) + 1
}

// IsMarkedFrozen / IsMarkedOnHold are convenience aliases over the
// DelegateLockState byte. Mirror the same-named ledger methods.
func (v *DelegationOutputView) IsMarkedFrozen() bool { return v.State == DelegateLockStateFrozen }
func (v *DelegationOutputView) IsMarkedOnHold() bool { return v.State == DelegateLockStateOnHold }

// SafeRevocationWindow returns the slot window [from, to] inside
// which the delegation can be safely revoked. Applicable iff the
// output is marked frozen (state == Frozen) and not on hold.
// Window width = Constants.SafeRevocationSlots. Mirrors
// ledger.DelegationOutput.SafeRevocationWindow.
func (v *DelegationOutputView) SafeRevocationWindow(c *Constants) (from, to uint32, applicable bool) {
	if v.IsMarkedOnHold() || !v.IsMarkedFrozen() {
		return 0, 0, false
	}
	fromSlot := c.LastSlotInEpochDirect(v.Target, v.LastFrozenEpoch, v.EpochSlots)
	return fromSlot + 1, fromSlot + c.SafeRevocationSlots, true
}

// IsInSafeRevocationWindow reports whether txSlot lies inside the
// safe-revocation window. Mirrors
// ledger.DelegationOutput.IsInSafeRevocationWindow.
func (v *DelegationOutputView) IsInSafeRevocationWindow(txSlot uint32, c *Constants) bool {
	from, to, applicable := v.SafeRevocationWindow(c)
	if !applicable {
		return false
	}
	return from <= txSlot && txSlot <= to
}
