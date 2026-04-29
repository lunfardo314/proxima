package seq_cmd

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func TestHelper(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	t.Logf("init supply: %s", util.Th(ledger.L(0).InitialSupply))
	t.Logf("1/5 of init supply: %s", util.Th(ledger.L(0).InitialSupply/5))
}
