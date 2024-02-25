package tests

import (
	"encoding/json"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestMarshaling(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		txids := []ledger.TransactionID{
			ledger.RandomTransactionID(true),
			ledger.RandomTransactionID(true),
			ledger.RandomTransactionID(false),
		}
		data, err := json.Marshal(txids)
		require.NoError(t, err)
		t.Logf("txid JSON: %s", string(data))
		var txidsBack []ledger.TransactionID
		err = json.Unmarshal(data, &txidsBack)
		require.NoError(t, err)
		require.True(t, util.EqualSlices(txids, txidsBack))
	})
	t.Run("1", func(t *testing.T) {
		chainIDs := []ledger.ChainID{
			ledger.RandomChainID(),
			ledger.RandomChainID(),
			ledger.RandomChainID(),
			ledger.RandomChainID(),
		}
		data, err := json.Marshal(chainIDs)
		require.NoError(t, err)
		t.Logf("chainid JSON: %s", string(data))
		var chainIDsBack []ledger.ChainID
		err = json.Unmarshal(data, &chainIDsBack)
		require.NoError(t, err)
		require.True(t, util.EqualSlices(chainIDs, chainIDsBack))
	})
}
