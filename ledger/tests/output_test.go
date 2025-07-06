package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

func TestRawOutputBytes(t *testing.T) {
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(1337).WithLock(ledger.AddressED25519FromPrivateKey(genesisPrivateKey))
	})

	rawBytes := o.Bytes()

	o, err := ledger.OutputFromBytes(rawBytes)
	require.NoError(t, err)

	t.Logf("Decompiled:\n%s", o.ToString())

	rawBytesConstr := o.ConstraintsRawBytes()
	size := 0
	for _, b := range rawBytesConstr {
		size += len(b) + 1
	}
	require.EqualValues(t, len(rawBytes), size+2)

	rawBytesBack := tuples.MakeTupleFromDataElements(rawBytesConstr...).Bytes()
	require.EqualValues(t, rawBytes, rawBytesBack)
	require.EqualValues(t, o.Bytes(), rawBytesBack)

}
