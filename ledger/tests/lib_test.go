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
	constants := ledger.ConstantsFromLibrary(lib.Library)

	t.Logf("------------------ Version data: '\n%s'", string(lib.VersionData))
	t.Logf("------------------ Main constants (defaults)\n%s", constants.String())
	t.Logf("------------------ Time-related constants\n%s", constants.TimeConstantsToString())
	t.Logf("------------------ Main constants (from global singleton) -------------------- \n%s", ledger.L(0).Lines("      ").String())
}

func TestLedgerToYAML(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	t.Run("compiled", func(t *testing.T) {
		yamlData := lib.ToYAML(true, "# ------------------- Proxima ledger definitions COMPILED -------------------------")
		t.Logf("\n%s", string(yamlData))
	})
	t.Run("not compiled", func(t *testing.T) {
		yamlData := lib.ToYAML(false, "# ------------------- Proxima ledger definitions NOT COMPILED -------------------------")
		t.Logf("\n%s", string(yamlData))
	})
}

func TestLedgerToYAMLFile(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	lib.PrintLibraryStats()
	h := lib.LibraryHash()
	yamlData := lib.ToYAML(true, "# ------------------- Proxima ledger definitions COMPILED -------------------------")
	t.Logf("Full library YAML size: %d bytes", len(yamlData))
	//_ = os.WriteFile("ledger.yaml", yamlData, 0644)
	libBack, err := easyfl.NewLibraryFromYAML[*ledger.EvalContext](yamlData)
	require.NoError(t, err)
	require.EqualValues(t, h, libBack.LibraryHash())
}

func TestLedgerConstantsYAML(t *testing.T) {
	pk := testutil.GetTestingPrivateKey(1)
	id := ledger.DefaultParameters(pk, uint32(time.Now().UnixNano()), "---- testing the description ----")
	yamlData := ledger.ConstantsYAMLFromParamsUpgrade0(id)
	t.Logf("\n%s", string(yamlData))
}

func TestProxi(t *testing.T) {
	t.Logf("1 x Proxi  = %s", util.Th(ledger.Proxi))
	t.Logf("1 x KProxi = %s", util.Th(ledger.KProxi))
	t.Logf("1 x MProxi = %s", util.Th(ledger.MProxi))
	t.Logf("1 x GProxi = %s", util.Th(ledger.GProxi))
	t.Logf("1 x TProxi = %s", util.Th(ledger.TProxi))
}
