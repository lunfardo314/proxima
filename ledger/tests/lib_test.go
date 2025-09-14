package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	par, _ := ledger.GetTestingLedgerParams()
	lib := ledger.LibraryFromParameters(par, true)
	constants := ledger.ConstantsFromLibrary(lib.Library)

	t.Logf("------------------ Main constants (default params)\n%s", constants.String())
	t.Logf("------------------ Time-related constants\n%s", constants.TimeConstantsToString())
	t.Logf("------------------ Main constants (from global singleton) -------------------- \n%s", ledger.Const.Lines("      ").String())
}

func TestLedgerToYAML(t *testing.T) {
	t.Run("compiled", func(t *testing.T) {
		yamlData := ledger.L().ToYAML(true, "# ------------------- Proxima ledger definitions COMPILED -------------------------")
		t.Logf("\n%s", string(yamlData))
	})
	t.Run("not compiled", func(t *testing.T) {
		yamlData := ledger.L().ToYAML(false, "# ------------------- Proxima ledger definitions NOT COMPILED -------------------------")
		t.Logf("\n%s", string(yamlData))
	})
}

func TestLedgerToYAMLFile(t *testing.T) {
	ledger.L().PrintLibraryStats()
	h := ledger.L().LibraryHash()
	yamlData := ledger.L().ToYAML(true, "# ------------------- Proxima ledger definitions COMPILED -------------------------")
	t.Logf("Full library YAML size: %d bytes", len(yamlData))
	//_ = os.WriteFile("ledger.yaml", yamlData, 0644)
	libBack, err := easyfl.NewLibraryFromYAML[*ledger.EvalContext](yamlData)
	require.NoError(t, err)
	require.EqualValues(t, h, libBack.LibraryHash())
}

func TestLedgerConstantsYAML(t *testing.T) {
	pk := testutil.GetTestingPrivateKey(1)
	id := ledger.DefaultParameters(pk, uint32(time.Now().UnixNano()), "---- testing the description ----")
	yamlData := ledger.ConstantsYAMLFromParams(id)
	t.Logf("\n%s", string(yamlData))
}
