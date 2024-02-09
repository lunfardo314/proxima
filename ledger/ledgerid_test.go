package ledger

import (
	"testing"

	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestYAML(t *testing.T) {
	privateKey := testutil.GetTestingPrivateKey()
	id := DefaultIdentityData(privateKey)
	yamlableStr := id.YAMLAble().YAML()
	t.Logf("\n" + string(yamlableStr))

	idBack, err := StateIdentityDataFromYAML(yamlableStr)
	require.NoError(t, err)
	require.EqualValues(t, id.Bytes(), idBack.Bytes())
}
