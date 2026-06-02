package chess_poc

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
)

// genesisPrivateKey initialises the ledger singleton for all chess_poc tests
// (mirrors ledger/tests/init.go). Wide coverage bounds disable the on-chain
// "healthiness" gate that's not relevant to this PoC.
var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
	)
}
