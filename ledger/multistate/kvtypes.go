package multistate

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

// access to the state

type (
	StateReader interface {
		GetUTXO(id base.OutputID) ([]byte, bool)
		HasUTXO(id base.OutputID) bool
		KnowsCommittedTransaction(txid base.TransactionID) bool // all txids are kept in the state for some time
	}

	StateIndexReader interface {
		IterateUTXOIDsInAccount(addr ledger.AccountID, fun func(oid base.OutputID) bool) (err error)
		IterateUTXOsInAccount(addr ledger.AccountID, fun func(oid base.OutputID, odata []byte) bool) (err error)
		IterateUTXOsInSlot(slot uint32, fun func(oid base.OutputID, oData []byte) bool) (err error)
		IterateChainTips(fun func(chainID base.ChainID, oid base.OutputID) bool) error

		GetUTXOIDsInAccount(addr ledger.AccountID) ([]base.OutputID, error)
		GetUTXOsInAccount(accountID ledger.AccountID) ([]*ledger.OutputDataWithID, error) // TODO leave Iterate.. only?

		GetUTXOForChainID(id base.ChainID) (*ledger.OutputDataWithID, error)
		Root() common.VCommitment
		MustLedgerIdentityBytes() []byte // either state identity consistent or panic
	}

	// IndexedStateReader state and indexer readers packing together
	IndexedStateReader interface {
		StateReader
		StateIndexReader
	}

	StateStoreReader interface {
		common.KVReader
		common.Traversable
		IsClosed() bool
	}

	StateStore interface {
		StateStoreReader
		common.BatchedUpdatable
	}
)
