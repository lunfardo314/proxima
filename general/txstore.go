package general

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/unitrie/common"
)

type SimpleTransactionStore struct {
	s common.KVStore
}

func (s SimpleTransactionStore) SaveTxBytes(txBytes []byte) error {
	txid, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
	if err != nil {
		return err
	}
	s.s.Set(txid[:], txBytes)
	return nil
}

func (s SimpleTransactionStore) GetTxBytes(txid *core.TransactionID) []byte {
	return s.s.Get(txid[:])
}

func NewSimpleTransactionStore(store common.KVStore) SimpleTransactionStore {
	return SimpleTransactionStore{store}
}
