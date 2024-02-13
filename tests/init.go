package tests

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData(10 * time.Millisecond)
	fmt.Printf("---------- tick duration has been set to %v -----------\n", ledger.TickDuration())
}
