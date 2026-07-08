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
		// low mine-chain difficulty so mine_test can find a proof-of-work fast;
		// P=2 so a pace below the minimum (M=1) is testable
		ledger.WithMineDifficulty(8, 4, 2),
		// R_init == A: exactly one mint possible, so the exhausted-chain (terminal)
		// path is reachable in a single transit
		ledger.WithMineRemainingInit(ledger.DefaultMineAmount),
	)
}
