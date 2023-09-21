package core

import (
	"encoding/hex"
	"testing"

	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/stretchr/testify/require"
)

func TestRawOutputBytes(t *testing.T) {
	const rawBytesStr = "40050b459488000000e8d40c798023459ba0c83f68984984271cd5e67c5c5c58cdadb69f2335109b5334c727d6f172291b542645aaa3108c086a4183c3434392f0d16f07c6895990a96dd74753d1a5695bad3e9934b40002000d49bb81028800000000000000000d49bc8102880000000000000000"

	rawBytes, err := hex.DecodeString(rawBytesStr)
	require.NoError(t, err)

	o, err := OutputFromBytesReadOnly(rawBytes)
	require.NoError(t, err)

	t.Logf("Decompiled:\n%s", o.ToString())

	rawBytesConstr := o.ConstraintsRawBytes()
	size := 0
	for _, b := range rawBytesConstr {
		size += len(b) + 1
	}
	require.EqualValues(t, len(rawBytes), size+2)

	rawBytesBack := lazybytes.MakeArrayFromDataReadOnly(rawBytesConstr...).Bytes()
	require.EqualValues(t, rawBytes, rawBytesBack)
	require.EqualValues(t, o.Bytes(), rawBytesBack)

}
