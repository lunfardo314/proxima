package txbuildercore

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
)

// returnToSender — additive constraint appended to a sendWithDeadline output.
// Forces whoever accepts the sent tokens to return `amount` base tokens to the
// master in the same tx. Spec: claude/return_to_sender.md. Mirrors the
// constants/template emitted by ledger.ReturnToSenderBytecode byte-for-byte.
const (
	ReturnToSenderName = "returnToSender"

	// amount is z-encoded (≤8 trimmed bytes) — the constraint reads it as $0.
	returnToSenderTemplate = ReturnToSenderName + "(z64/%d)"

	// Position of the anti-fold inline literal inside the return receipt.
	// The on-chain receiptLiteral helper reads pos 3 (see
	// def/lock_dex_orders.easyfl). It is NOT a chain constraint despite
	// sharing the index — the receipt is a plain sigLock output.
	returnReceiptLiteralIndex = byte(3)
)

// NewReturnToSenderBytecode compiles returnToSender(amount). The result is
// appended at a free position (≥ 3) on a sendWithDeadline output via
// b.PutConstraint(bin, idx). Errors if amount is zero (the constraint rejects
// a zero amount at produce time).
func (l *Library[any]) NewReturnToSenderBytecode(amount uint64) ([]byte, error) {
	if amount == 0 {
		return nil, fmt.Errorf("NewReturnToSenderBytecode: amount must be positive")
	}
	return l.CompileExpression(fmt.Sprintf(returnToSenderTemplate, amount))
}

// NewReturnReceiptOutput composes the consumer-side return receipt that
// satisfies a returnToSender constraint when the target accepts:
//
//	slot 0 (amounts):       baseAmount (must be ≥ the returnToSender amount)
//	slot 1 (index-values):  tuple holding masterID at position 0
//	slot 2 (lock):          canonical sigLock bytecode
//	slot 3 (literal):       inline data == the consumed SWD input index
//
// The position-3 literal is the anti-fold binding: it ties this receipt to
// exactly one consumed input, so one receipt cannot satisfy several
// returnToSender inputs at once.
func (l *Library[any]) NewReturnReceiptOutput(baseAmount uint64, masterID base.HolderID, consumedInputIndex byte) (*Output, error) {
	sigLockBin, err := l.lockBytecode(SigLockName)
	if err != nil {
		return nil, err
	}
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(baseAmount), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{masterID[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(sigLockBin, ConstraintIndexLock)
	b.PutConstraint(easyfl.InlineDataBytecode([]byte{consumedInputIndex}), returnReceiptLiteralIndex)
	return b.Output(), nil
}
