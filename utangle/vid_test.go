package utangle

import (
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/utxodb"
	"github.com/stretchr/testify/require"
)

func TestVID(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		dict := make(map[core.TransactionID]*WrappedTx)
		u := utxodb.NewUTXODB()
		txBytes, err := u.MakeTransactionFromFaucet(core.AddressED25519Null())
		require.NoError(t, err)
		tx, err := transaction.TransactionFromBytesAllChecks(txBytes)
		require.NoError(t, err)
		txid := tx.ID()

		v := newVertex(tx, nil)
		vid := v.Wrap()
		txidBack := vid.ID()
		require.EqualValues(t, *txid, *txidBack)
		dict[*txid] = vid

	})
}
