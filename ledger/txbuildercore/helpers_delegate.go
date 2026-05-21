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
func (l *Library) NewDelegateLockBytecode(
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
func (l *Library) NewDelegateLockState(lastFrozenEpoch uint32, state byte) ([]byte, error) {
	src := fmt.Sprintf(delegateLockStateTemplate, lastFrozenEpoch, state)
	return l.CompileExpression(src)
}

// NewDelegationParams emits the 2-arg delegationParams constraint
// bytecode (slot 6 of a chain output that opts in to accept
// delegations). Attachable only at chain origin; pinned across every
// chain transit via selfImmutableOnSuccessorIndex.
func (l *Library) NewDelegationParams(epochSlots uint32, maxFrozenEpochs byte) ([]byte, error) {
	src := fmt.Sprintf(delegationParamsTemplate, epochSlots, maxFrozenEpochs)
	return l.CompileExpression(src)
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
func (l *Library) NewDelegationInitOutput(par DelegationInitOutputParams) (*Output, error) {
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
func (l *Library) ParseDelegateLockState(data []byte) (DelegateLockStateView, error) {
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
	OriginSlot      uint32        // output creation slot (oid.Slot())
	ChainID         base.ChainID  // delegation's own chainID (computed for origin)
	MasterID        base.HolderID // index-values[0]
	Target          base.ChainID  // index-values[1]
	MaxFrozenEpochs byte          // delegateLock arg 0 (caller-supplied cap)
	EpochSlots      uint32        // delegateLock arg 2 (z32/epochSlots)
	LastFrozenEpoch uint32        // delegateLockState arg 0
	State           byte          // delegateLockState arg 1 (0 / Frozen / OnHold)
}

// ParseDelegationOutput decodes a parsed output if it carries a
// delegateLock at slot 2. Returns (view, true, nil) on success;
// (nil, false, nil) when the output is not a delegation. Errors only
// on malformed bytes that look like delegation (right symbol, wrong
// arg shape).
func (l *Library) ParseDelegationOutput(o *Output, oid base.OutputID) (*DelegationOutputView, bool, error) {
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

	// ChainID — for a transit chain the chain constraint's arg 0
	// carries the explicit chainID; for a chain origin the arg is
	// NilChainID and the real chainID is blake2b(oid).
	chainBin, err := o.ConstraintAt(ConstraintIndexChain)
	if err != nil {
		return nil, false, err
	}
	chainID, err := parseChainConstraintChainID(l, chainBin, oid)
	if err != nil {
		return nil, false, err
	}

	return &DelegationOutputView{
		OriginSlot:      oid.Slot(),
		ChainID:         chainID,
		MasterID:        master,
		Target:          target,
		MaxFrozenEpochs: maxFrozenEpochs,
		EpochSlots:      epochSlots,
		LastFrozenEpoch: state.LastFrozenEpoch,
		State:           state.State,
	}, true, nil
}

// parseChainConstraintChainID extracts the chainID from a chain
// constraint's first arg. Arg 0 == NilChainID means this is a chain
// origin output — the real chainID is computed from the OutputID
// (blake2b). Pure byte parse — no eval.
func parseChainConstraintChainID(l *Library, chainBin []byte, oid base.OutputID) (base.ChainID, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(chainBin, 7)
	if err != nil {
		return base.NilChainID, fmt.Errorf("parseChainConstraintChainID: %w", err)
	}
	if sym != ChainConstraintName {
		return base.NilChainID, fmt.Errorf("parseChainConstraintChainID: expected %s, got %s", ChainConstraintName, sym)
	}
	idBytes := easyfl.StripDataPrefix(args[0])
	id, err := base.ChainIDFromBytes(idBytes)
	if err != nil {
		return base.NilChainID, fmt.Errorf("parseChainConstraintChainID: %w", err)
	}
	if id == base.NilChainID {
		return base.MakeOriginChainID(oid), nil
	}
	return id, nil
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
