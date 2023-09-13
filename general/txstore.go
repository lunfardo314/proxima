package general

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/unitrie/common"
)

type SimpleTxByteStore struct {
	s common.KVStore
}

type DummyTxByteStore struct {
	s common.KVStore
}

func NewSimpleTxByteStore(store common.KVStore) SimpleTxByteStore {
	return SimpleTxByteStore{store}
}

func (s SimpleTxByteStore) SaveTxBytes(txBytes []byte) error {
	txid, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
	if err != nil {
		return err
	}
	s.s.Set(txid[:], txBytes)
	return nil
}

func (s SimpleTxByteStore) GetTxBytes(txid *core.TransactionID) []byte {
	return s.s.Get(txid[:])
}

func NewDummyTxByteStore(store common.KVStore) DummyTxByteStore {
	return DummyTxByteStore{}
}

func (d DummyTxByteStore) SaveTxBytes(_ []byte) error {
	return nil
}

func (d DummyTxByteStore) GetTxBytes(id *core.TransactionID) []byte {
	return nil
}
