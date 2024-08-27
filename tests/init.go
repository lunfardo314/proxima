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
		ledger.WithTickDuration(4*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithSequencerPace(3))

	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration    : %v
     transaction pace : %d ticks
     sequencer pace   : %d ticks
`,
		ledger.TickDuration(), ledger.TransactionPace(), ledger.TransactionPaceSequencer(),
	)
}
