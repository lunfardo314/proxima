package txbuildercore

import (
	"fmt"
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
