package utangle_old

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/utxodb"
	"github.com/stretchr/testify/require"
)

func TestVID(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		dict := make(map[ledger.TransactionID]*WrappedTx)
		u := utxodb.NewUTXODB()
		txBytes, err := u.MakeTransactionFromFaucet(ledger.AddressED25519Null())
		require.NoError(t, err)
		tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(t, err)
		txid := tx.ID()

		v := NewVertex(tx)
		vid := v.Wrap()
		txidBack := vid.ID()
		require.EqualValues(t, *txid, *txidBack)
		dict[*txid] = vid

	})
}
