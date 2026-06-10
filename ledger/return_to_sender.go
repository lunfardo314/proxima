package ledger

import (
	_ "embed"
	"fmt"
)

// =============================================================================
// returnToSender — an additive constraint (not a Lock kind) attached to a
// sendWithDeadline UTXO. It forces whoever accepts the sent tokens to pay
// `amount` base tokens back to the master in the same transaction. Spec:
// claude/return_to_sender.md.
//
// Like randomizeConsumption it needs no serde registration (it never becomes
// the output's lock); only a bytecode helper plus the embedded source, which
// is introduced after lockDexOrdersSource so it can reuse the public receipt
// helpers defined there.
// =============================================================================

//go:embed def/return_to_sender.easyfl
var returnToSenderSource string

const ReturnToSenderName = "returnToSender"

// amount is z-encoded (≤ 8 trimmed bytes), matching the constraint's $0 reads.
const returnToSenderTemplate = ReturnToSenderName + "(z64/%d)"

// ReturnToSenderBytecode compiles returnToSender(amount). Returns an error if
// amount is zero (the constraint rejects a zero amount at produce time).
func ReturnToSenderBytecode(amount uint64) ([]byte, error) {
	if amount == 0 {
		return nil, fmt.Errorf("ReturnToSenderBytecode: amount must be positive")
	}
	return mustBinFromSource(fmt.Sprintf(returnToSenderTemplate, amount)), nil
}
