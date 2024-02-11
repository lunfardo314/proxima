package ledger

import (
	"testing"

	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/stretchr/testify/require"
)

func TestRawOutputBytes(t *testing.T) {
	pk := InitWithTestingLedgerIDData()

	o := NewOutput(func(o *Output) {
		o.WithAmount(1337).WithLock(AddressED25519FromPrivateKey(pk))
	})

	rawBytes := o.Bytes()

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
