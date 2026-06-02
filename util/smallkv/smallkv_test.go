package smallkv

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	skv := New()
	skv.Set(1, []byte("abc"))
	skv.Set(3, []byte("def"))
	skv.Set(100, []byte("---"))

	skvBin := skv.Bytes()
	skvBack, err := FromBytes(skvBin)
	require.NoError(t, err)
	require.EqualValues(t, skv.Len(), skvBack.Len())
	require.EqualValues(t, 3, skvBack.Len())
	require.EqualValues(t, skvBin, skvBack.Bytes())
	require.EqualValues(t, []byte("abc"), skvBack.Get(1))
	require.EqualValues(t, []byte("def"), skvBack.Get(3))
	require.EqualValues(t, []byte("---"), skvBack.Get(100))
	require.True(t, len(skvBack.Get(22)) == 0)
	t.Logf("byte size: %d", len(skvBin))
}
