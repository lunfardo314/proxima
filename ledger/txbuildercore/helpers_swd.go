package txbuildercore

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
)

// sendWithDeadline lock constants + source template. Values mirror
// ledger.SendWithDeadlineLockName / .SendWithDeadlineTargetSigLock /
// .SendWithDeadlineTargetChainLock and the on-the-wire source format
// emitted by (*ledger.SendWithDeadlineLock).Source() — byte-for-byte.
//
// Layout of a SWD output (see ledger/def/lock_send_with_deadline.easyfl
// and claude/send_with_deadline_lock.md):
//
//	slot 0 (amounts):       trimmed-uint64 token balance
//	slot 1 (index-values):  tuple [masterID, targetID]
//	slot 2 (lock):          sendWithDeadline(targetType, accept, cleanup)
//
// The (master, target) pair is read by the lock via selfIndexValue(0/1);
// the 3 args carried inline on the lock are just the policy (target
// kind + the two deadlines).
const (
	SendWithDeadlineLockName = "sendWithDeadline"

	SendWithDeadlineTargetSigLock   byte = 0x00
	SendWithDeadlineTargetChainLock byte = 0x01

	// targetType is encoded as a raw 1-byte 0xXX literal because
	// easyfl has no u8/ typed-literal form; the constraint reads it
	// via byte($0, 0). Mirrors lock_send_with_deadline.go's template.
	sendWithDeadlineTemplate = SendWithDeadlineLockName + "(0x%02x, u32/%d, u32/%d)"
)

// NewSendWithDeadlineLockBytecode emits the 3-arg sendWithDeadline
// constraint bytecode for slot 2 of an SWD output. The targetType
// chooses how the on-chain script unlocks the target side
// (SendWithDeadlineTargetSigLock → sigLock holder; ...TargetChainLock
// → controller of the chain whose chainID == target).
func (l *Library[any]) NewSendWithDeadlineLockBytecode(
	targetType byte,
	acceptanceSlots, cleanupSlots uint32,
) ([]byte, error) {
	src := fmt.Sprintf(sendWithDeadlineTemplate, targetType, acceptanceSlots, cleanupSlots)
	return l.CompileExpression(src)
}

// SendWithDeadlineOutputParams describes the inputs for
// NewSendWithDeadlineOutput. Mirrors the field set used by
// ledger.SendWithDeadlineLock + the surrounding NewOutput call site.
type SendWithDeadlineOutputParams struct {
	Amount          uint64
	MasterID        base.HolderID
	TargetID        base.HolderID // sigLock holderID OR chainID per TargetType
	TargetType      byte
	AcceptanceSlots uint32
	CleanupSlots    uint32
}

// NewSendWithDeadlineOutput composes a full sendWithDeadline output:
//
//	slot 0 (amounts):       trimmed-uint64 token balance
//	slot 1 (index-values):  tuple [masterID, targetID] (master-first)
//	slot 2 (lock):          sendWithDeadline(targetType, accept, cleanup)
//
// Mirrors ledger.NewOutput(o.WithTokenBalance(...).WithLock(swd))
// byte-for-byte; verified by the byte-identity test in
// helpers_swd_test.go.
func (l *Library[any]) NewSendWithDeadlineOutput(par SendWithDeadlineOutputParams) (*Output, error) {
	lockBin, err := l.NewSendWithDeadlineLockBytecode(par.TargetType, par.AcceptanceSlots, par.CleanupSlots)
	if err != nil {
		return nil, err
	}
	// target is a 24-byte chainID for a chainLock target, a 32-byte holderID otherwise
	targetID := par.TargetID[:]
	if par.TargetType == SendWithDeadlineTargetChainLock {
		targetID = par.TargetID[:base.ChainIDLength]
	}
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(par.Amount), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{par.MasterID[:], targetID}), ConstraintIndexIndexValues)
	b.PutConstraint(lockBin, ConstraintIndexLock)
	return b.Output(), nil
}
