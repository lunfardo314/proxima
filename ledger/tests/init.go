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
		// Low mine-chain difficulty so mine_test can find a proof-of-work fast.
		// Seed B0=8 sits in the middle of a narrow band [6,10] so both retarget
		// clamps are reachable within a few transits. P=1, as in production.
		ledger.WithMineDifficulty(8, 6, 10, 1),
		// harden after 2 full slots in a row, so a harden is reachable in three
		// transits at pace 1 (the first holds: its predecessor is genesis)
		ledger.WithMineHardenAfter(2),
		// R_init == 8A: enough transits to fill the slot ring (4) and drive the
		// retarget into either clamp (7), while keeping the exhausted-chain
		// (terminal) path reachable in a short loop
		ledger.WithMineRemainingInit(8*ledger.DefaultMineAmountBase),
	)
}
