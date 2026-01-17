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
		ledger.WithAttachmentRecursionDepthBase(100),
	)
	lib := ledger.L(base.MaxSlot)
	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration    : %v
     slot duration    : %v
     transaction pace : %d ticks
     sequencer pace   : %d ticks
     attachment depth : %d
`,
		lib.TickDuration,
		lib.SlotDuration(),
		lib.TransactionPace,
		lib.TransactionPaceSequencer,
		lib.AttachmentRecursionDepthBase,
	)
}

// reinitTestLedger resets and re-initializes the ledger with a fresh genesis timestamp.
// Use this at the start of tests that depend on fresh genesis timing.
func reinitTestLedger() {
	ledger.ResetForTesting()
	initTestLedger()
}
