package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	par, _ := ledger.GetTestingLedgerParams()
	lib := ledger.LibraryFromParameters(par, true)

	t.Logf("------------------ Version data: '\n%s'", string(lib.VersionData))
	t.Logf("------------------ Main constants (defaults)\n%s", ledger.ConstantsStringFromLibrary(lib.Library))
	t.Logf("------------------ Time-related constants\n%s", ledger.L(0).TimeConstantsToString())
	t.Logf("------------------ Main constants (from global singleton) -------------------- \n%s", ledger.L(0).ConstantsLines("      ").String())
}

func TestLedgerToJSON(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	t.Run("compiled", func(t *testing.T) {
		jsonData := easyfl.ToJSON(lib.Library, true, true)
		t.Logf("\n%s", string(jsonData))
	})
	t.Run("not compiled", func(t *testing.T) {
		jsonData := easyfl.ToJSON(lib.Library, false, true)
		t.Logf("\n%s", string(jsonData))
	})
}

func TestLedgerToJSONFile(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	lib.PrintLibraryStats()
	h := lib.LibraryHash()
	jsonData := easyfl.ToJSON(lib.Library, true, true)
	t.Logf("Full library JSON size: %d bytes", len(jsonData))
	//_ = os.WriteFile("ledger.json", jsonData, 0644)
	libBack, err := easyfl.NewLibraryFromJSON[*ledger.EvalContext](jsonData)
	require.NoError(t, err)
	require.EqualValues(t, h, libBack.LibraryHash())
}

func TestLedgerConstantsJSON(t *testing.T) {
	pk := testutil.GetTestingPrivateKey(1)
	id := ledger.DefaultParameters(pk, uint32(time.Now().UnixNano()), "---- testing the description ----")
	jsonData := ledger.ConstantsJSONFromParamsUpgrade0(id)
	t.Logf("\n%s", string(jsonData))
}

func TestProxi(t *testing.T) {
	t.Logf("1 x Proxi  = %s", util.Th(ledger.Proxi))
	t.Logf("1 x KProxi = %s", util.Th(ledger.KProxi))
	t.Logf("1 x MProxi = %s", util.Th(ledger.MProxi))
	t.Logf("1 x GProxi = %s", util.Th(ledger.GProxi))
	t.Logf("1 x TProxi = %s", util.Th(ledger.TProxi))
}
