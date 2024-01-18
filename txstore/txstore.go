package txstore

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
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

func (s SimpleTxBytesStore) SaveTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) error {
	txid, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
	if err != nil {
		return err
	}
	if metadata != nil {
		mdTmp := *metadata
		mdTmp.IsResponseToPull = false // saving without the irrelevant metadata flag
		metadata = &mdTmp
	}
	s.s.Set(txid[:], common.ConcatBytes(metadata.Bytes(), txBytes))
	return nil
}

func (s SimpleTxBytesStore) GetTxBytesWithMetadata(txid *ledger.TransactionID) []byte {
	return s.s.Get(txid[:])
}

func NewDummyTxBytesStore() DummyTxBytesStore {
	return DummyTxBytesStore{}
}

func (d DummyTxBytesStore) SaveTxBytesWithMetadata(_ []byte, _ *txmetadata.TransactionMetadata) error {
	return nil
}

func (d DummyTxBytesStore) GetTxBytesWithMetadata(_ *ledger.TransactionID) []byte {
	return nil
}
