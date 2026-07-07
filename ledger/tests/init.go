package tests

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithCoverageContributionBounds(0, 2*ledger.DefaultInitialSupply),
		// low mine-chain difficulty so mine_test can find a proof-of-work fast
		ledger.WithMineDifficulty(8, 4, 1),
	)
}
