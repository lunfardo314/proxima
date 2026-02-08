package glb

import (
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
)

var (
	stateDB      *badger.DB
	stateStore   global.Store
	txBytesDB    *badger.DB
	txBytesStore global.TxBytesStore
)

func InitLedgerFromDB() {
	dbName := global.MultiStateDBName
	Infof("Multi-state database: %s", dbName)
	FileMustExist(dbName)
	stateDB = badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	stateStore = badger_adaptor.New(stateDB)
	multistate.InitLedgerFromStore(stateStore)
	Infof("ledger was initialized from definitions provided in database '%s'", global.MultiStateDBName)
}

func InitLedgerFromProvidedID() {
	idBytes, err := os.ReadFile(LedgerDefinitionsFileName)
	AssertNoError(err)
	ledger.MustInitLibraryCacheFromYAML(idBytes)
	Infof("ledger was initialized from definitions provided in file '%s'", LedgerDefinitionsFileName)
}

func StateStore() global.Store {
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

func InitDBRaw(dbName string) *badger_adaptor.DB {
	Infof("Opening raw database: %s", dbName)
	FileMustExist(dbName)
	stateDB = badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	return badger_adaptor.New(stateDB)
}
