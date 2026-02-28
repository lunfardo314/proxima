package inflation

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
	)
}
