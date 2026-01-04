package tests

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	initTestLedger()
}

// initTestLedger initializes the ledger for testing. Called once in init().
func initTestLedger() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3))

	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration    : %v
     slot duration    : %v
     transaction pace : %d ticks
     sequencer pace   : %d ticks
`,
		ledger.TickDuration(), ledger.SlotDuration(), ledger.Const.TransactionPace, ledger.Const.TransactionPaceSequencer,
	)
}

// reinitTestLedger resets and re-initializes the ledger with a fresh genesis timestamp.
// Use this at the start of tests that depend on fresh genesis timing.
func reinitTestLedger() {
	ledger.ResetForTesting()
	initTestLedger()
}
