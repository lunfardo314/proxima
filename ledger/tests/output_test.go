package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/stretchr/testify/require"
)

func TestRawOutputBytes(t *testing.T) {
	o := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(1337).WithLock(ledger.AddressED25519FromPrivateKey(genesisPrivateKey))
	})

	rawBytes := o.Bytes()

	o, err := ledger.OutputFromBytesReadOnly(rawBytes)
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
