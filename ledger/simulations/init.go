package simulations

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}
