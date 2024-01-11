package ledger

import (
	"encoding/json"
	"testing"

	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestMarshaling(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		txids := []TransactionID{
			RandomTransactionID(true, true),
			RandomTransactionID(true, false),
			RandomTransactionID(false, false),
		}
		data, err := json.Marshal(txids)
		require.NoError(t, err)
		t.Logf("txid JSON: %s", string(data))
		var txidsBack []TransactionID
		err = json.Unmarshal(data, &txidsBack)
		require.NoError(t, err)
		require.True(t, util.EqualSlices(txids, txidsBack))
	})
	t.Run("1", func(t *testing.T) {
		chainIDs := []ChainID{
			RandomChainID(),
			RandomChainID(),
			RandomChainID(),
			RandomChainID(),
		}
		data, err := json.Marshal(chainIDs)
		require.NoError(t, err)
		t.Logf("chainid JSON: %s", string(data))
		var chainIDsBack []ChainID
		err = json.Unmarshal(data, &chainIDsBack)
		require.NoError(t, err)
		require.True(t, util.EqualSlices(chainIDs, chainIDsBack))
	})
}
