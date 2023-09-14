package txstore

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/unitrie/common"
)

type SimpleTxBytesStore struct {
	s common.KVStore
}

type DummyTxBytesStore struct {
	s common.KVStore
}

func NewSimpleTxBytesStore(store common.KVStore) SimpleTxBytesStore {
	return SimpleTxBytesStore{store}
}

func (s SimpleTxBytesStore) SaveTxBytes(txBytes []byte) error {
	txid, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
	if err != nil {
		return err
	}
	s.s.Set(txid[:], txBytes)
	return nil
}

func (s SimpleTxBytesStore) GetTxBytes(txid *core.TransactionID) []byte {
	return s.s.Get(txid[:])
}

func NewDummyTxBytesStore() DummyTxBytesStore {
	return DummyTxBytesStore{}
}

func (d DummyTxBytesStore) SaveTxBytes(_ []byte) error {
	return nil
}

func (d DummyTxBytesStore) GetTxBytes(_ *core.TransactionID) []byte {
	return nil
}
