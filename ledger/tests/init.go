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
		// clamps are reachable within a few transits. P=2 so a pace below the
		// minimum (M=1) is testable.
		ledger.WithMineDifficulty(8, 6, 10, 2),
		// target pace 4 => target span 4*4=16, so the retarget eases at span >= 18
		// and hardens at span <= 14 (pace 5 and pace 2 respectively, for 4 gaps)
		ledger.WithMineTargetPace(4),
		// R_init == 8A: enough transits to fill the slot ring (4) and drive the
		// retarget into either clamp (7), while keeping the exhausted-chain
		// (terminal) path reachable in a short loop
		ledger.WithMineRemainingInit(8 * ledger.DefaultMineAmount),
	)
}
