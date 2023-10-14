package general

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/unitrie/common"
)

type (
	StateReader interface {
		GetUTXO(id *core.OutputID) ([]byte, bool)
		HasUTXO(id *core.OutputID) bool
		KnowsTransaction(txid *core.TransactionID) bool // all txids are kept in the state for some time
	}

	StateIndexReader interface {
		GetIDSLockedInAccount(addr core.AccountID) ([]core.OutputID, error)
		GetUTXOsLockedInAccount(accountID core.AccountID) ([]*core.OutputDataWithID, error)
		GetUTXOForChainID(id *core.ChainID) (*core.OutputDataWithID, error)
		Root() common.VCommitment
		MustStateIdentityBytes() []byte // either state identity consistent or panic
	}

	// IndexedStateReader state and indexer readers packing together
	IndexedStateReader interface {
		StateReader
		StateIndexReader
	}

	StateStore interface {
		common.KVReader
		common.BatchedUpdatable
		common.Traversable
		IsClosed() bool
	}

	TxBytesStore interface {
		SaveTxBytes([]byte) error
		GetTxBytes(id *core.TransactionID) []byte // returns empty slice on absence
	}
)
