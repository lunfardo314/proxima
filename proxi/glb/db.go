package glb

import (
	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
)

var (
	stateDB      *badger.DB
	stateStore   global.StateStore
	txBytesDB    *badger.DB
	txBytesStore global.TxBytesStore
)

func InitLedgerFromDB(verbose ...bool) {
	dbName := global.MultiStateDBName
	Infof("Multi-state database: %s", dbName)
	FileMustExist(dbName)
	stateDB = badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	stateStore = badger_adaptor.New(stateDB)
	multistate.InitLedgerFromStore(stateStore, verbose...)
}

func StateStore() global.StateStore {
	return stateStore
}

func CloseDatabases() {
	if stateDB != nil {
		_ = stateDB.Close()
	}
	if txBytesDB != nil {
		_ = txBytesDB.Close()

	}
}

func InitTxStoreDB() {
	txDBName := global.TxStoreDBName
	Infof("Transaction store database: %s", txDBName)

	txBytesDB = badger_adaptor.MustCreateOrOpenBadgerDB(txDBName)
	txBytesStore = txstore.NewSimpleTxBytesStore(badger_adaptor.New(txBytesDB))
}

func TxBytesStore() global.TxBytesStore {
	return txBytesStore
}
