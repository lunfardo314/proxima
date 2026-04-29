package tests

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	initTestLedger()
}

// initTestLedger initializes the ledger for testing. Called once in init().
func initTestLedger() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(600),
		ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
	)
	lib := ledger.L(base.MaxSlot)
	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration        : %v
     slot duration        : %v
     transaction pace     : %d ticks
     sequencer pace       : %d ticks
     attachment cost budget: %d
`,
		lib.TickDuration,
		lib.SlotDuration(),
		lib.TransactionPace,
		lib.TransactionPaceSequencer,
		lib.AttachmentCostBudget,
	)
}

// reinitTestLedger resets and re-initializes the ledger with a fresh genesis timestamp.
// Use this at the start of tests that depend on fresh genesis timing.
func reinitTestLedger() {
	ledger.ResetForTesting()
	initTestLedger()
}

// reinitTestLedgerWithBudget resets and re-initializes the ledger with a custom attachment cost budget.
// Use this to test budget-exceeded scenarios with a lower budget.
// Returns a cleanup function that restores the original budget.
func reinitTestLedgerWithBudget(budget int) func() {
	ledger.ResetForTesting()
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(budget),
		ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
	)
	return func() {
		// Restore default budget
		ledger.ResetForTesting()
		initTestLedger()
	}
}

// reinitTestLedgerWithCoverageBounds resets and re-initializes the ledger with custom
// branch coverage bounds. Used to test that sequencers with coverage outside [lower, upper]
// cannot produce branches. Returns a cleanup function that restores defaults.
func reinitTestLedgerWithCoverageBounds(lowerBound, upperBound uint64) func() {
	ledger.ResetForTesting()
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(600),
		ledger.WithBranchCoverageBounds(lowerBound, upperBound),
	)
	return func() {
		ledger.ResetForTesting()
		initTestLedger()
	}
}
