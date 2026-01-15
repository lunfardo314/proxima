package node

import (
	"encoding/hex"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/viper"
)

// initMultiStateLedger opens ledger state DB and initializes global ledger object
func (p *ProximaNode) initMultiStateLedger() {
	var err error
	dbname := global.MultiStateDBName

	// Set cache limits to prevent unbounded memory growth. Otherwise, it leaks memory. Claude Code fix
	opts := badger.DefaultOptions(dbname)
	opts.BlockCacheSize = 64 << 20 // 64MB block cache limit
	opts.IndexCacheSize = 32 << 20 // 32MB index cache limit

	bdb, err := badger_adaptor.OpenBadgerDB(dbname, opts)
	if err != nil {
		p.Log().Fatalf("can't open '%s': %v", dbname, err)
	}
	p.dbClosedWG.Add(1)
	p.multiStateDB = badger_adaptor.New(bdb)
	p.Log().Infof("opened multi-state DB '%s'", dbname)

	// initialize the ledger library singleton with the ledger ID data from DB
	multistate.InitLedgerFromStore(p.multiStateDB)
	p.Log().Infof("ledger ID params:\n%s", ledger.Const.Lines("       ").String())

	// Log all upgrades in effect
	p.logUpgradesList()

	p.snapshotBranchID = multistate.FetchSnapshotBranchID(p.multiStateDB)
	p.Log().Infof("current slot: %d", ledger.TimeNow().Slot)
	p.Log().Infof("snapshot branch id: %s", p.snapshotBranchID.String())

	// Initialize pending upgrade tracking for optimization
	// This checks which upgrade UTXOs already exist in the latest state
	branchData, ok := multistate.FetchBranchData(p.multiStateDB, p.snapshotBranchID)
	util.Assertf(ok, "FetchBranchData: branch data not found for %s", p.snapshotBranchID.String())
	stateReader := multistate.MustNewSugaredReadableState(p.multiStateDB, branchData.Root)
	ledger.InitNextPendingUpgradeSlot(func(oid base.OutputID) bool {
		return stateReader.HasUTXO(oid)
	})

	p.RepeatInBackground("Badger_DB_GC_loop", 5*time.Minute, func() bool {
		p.databaseGC()
		return true
	})

	go func() {
		// wait until others will stop
		<-p.workProcessesStopStepChan
		select {
		case <-p.workProcessesStopStepChan:
		case <-time.After(10 * time.Second):
			p.Log().Warnf("forced close of multi-state DB")
		}
		_ = p.multiStateDB.Close()
		p.Log().Infof("multi-state database has been closed")
		p.dbClosedWG.Done()
	}()
}

func (p *ProximaNode) initTxStore() {
	switch viper.GetString(global.ConfigKeyTxStoreType) {
	case "dummy":
		p.Log().Infof("transaction store is 'dummy'")
		p.txBytesStore = txstore.NewDummyTxBytesStore()

	case "url":
		panic("'url' type of transaction store is not supported yet")

	default:
		// default option is predefined database name
		dbname := global.TxStoreDBName
		p.Log().Infof("transaction store database dbname is '%s'", dbname)

		// Set cache limits to prevent unbounded memory growth. Otherwise, it leaks memory. Claude Code fix
		opts := badger.DefaultOptions(dbname)
		opts.BlockCacheSize = 64 << 20 // 64MB block cache limit
		opts.IndexCacheSize = 32 << 20 // 32MB index cache limit

		p.txStoreDB = badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(dbname, opts))
		p.dbClosedWG.Add(1)
		p.txBytesStore = txstore.NewSimpleTxBytesStore(p.txStoreDB, p)
		p.Log().Infof("opened DB '%s' as transaction store", dbname)

		go func() {
			<-p.workProcessesStopStepChan
			select {
			case <-p.workProcessesStopStepChan:
			case <-time.After(10 * time.Second):
				p.Log().Warnf("forced close of transaction store DB")
			}
			_ = p.txStoreDB.Close()
			p.Log().Infof("transaction store database has been closed")
			p.dbClosedWG.Done()
		}()
	}
}

func (p *ProximaNode) databaseGC() {
	start := time.Now()
	err := p.multiStateDB.RunValueLogGC(0.5)
	p.Log().Infof("----- Badger DB GC (%v): %v", time.Since(start), err)
}

// logUpgradesList logs all upgrades in effect with their slots and library hashes.
func (p *ProximaNode) logUpgradesList() {
	slots := ledger.GetAllUpgradeSlots(base.MaxSlot)
	if len(slots) == 0 {
		p.Log().Warnf("no upgrades found in ledger")
		return
	}

	p.Log().Infof("ledger upgrades in effect:")
	for _, slot := range slots {
		lib := ledger.L(slot)
		hash := lib.LibraryHash()
		p.Log().Infof("       slot %8d: library hash %s", slot, hex.EncodeToString(hash[:]))
	}

	// Log pending upgrade if available
	if ledger.PendingUpgrade != nil {
		p.Log().Infof("       slot %8d: PENDING", ledger.PendingUpgrade.Slot)
	}
}
