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
	id, _ := ledger.GetTestingIdentityData()
	lib := ledger.LibraryFromIdentityParameters(id, true)

	t.Logf("------------------ ORIG \n%s", lib.ID.String())
	t.Logf("------------------\n%s", lib.ID.TimeConstantsToString())

	idBack, err := ledger.IDParametersFromLibrary(lib.Library)
	require.NoError(t, err)
	t.Logf("------------------ ID LOADED FROM LIBRARY \n%s", idBack.String())

	require.EqualValues(t, id, idBack)
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
	id := ledger.DefaultIdentityParameters(pk, uint32(time.Now().UnixNano()), "---- testing the description ----")
	yamlData := ledger.ConstantsYAMLFromIdentity(id)
	t.Logf("\n%s", string(yamlData))
}
