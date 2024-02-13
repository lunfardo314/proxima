package tests

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData(
		ledger.WithTickDuration(10*time.Millisecond),
		ledger.WithTransactionPace(1),
		ledger.WithSequencerPace(1))

	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration    : %v
     transaction pace : %d ticks
     sequencer pace   : %d ticks
`,
		ledger.TickDuration(), ledger.TransactionPace(), ledger.TransactionPaceSequencer(),
	)
}
