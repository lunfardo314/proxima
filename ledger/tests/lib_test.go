package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	id, _ := ledger.GetTestingIdentityData()
	lib := ledger.InitLocally(id, true)
	t.Logf("------------------\n%s", lib.ID.String())
	t.Logf("------------------\n" + string(lib.ID.YAML()))
	t.Logf("------------------\n" + lib.ID.TimeConstantsToString())
}

func TestLedgerIDYAML(t *testing.T) {
	id := ledger.L().ID
	yamlableStr := id.YAMLAble().YAML()
	t.Logf("\n" + string(yamlableStr))

	idBack, err := ledger.StateIdentityDataFromYAML(yamlableStr)
	require.NoError(t, err)
	require.EqualValues(t, id.Bytes(), idBack.Bytes())
}
