package node

import (
	"encoding/hex"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core/core_modules/txlogger"
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
	opts.NumCompactors = 2         // reduce from default 4 to lower I/O contention

	bdb, err := badger_adaptor.OpenBadgerDB(dbname, opts)
	if err != nil {
		p.Log().Fatalf("can't open '%s': %v", dbname, err)
	}
	p.dbClosedWG.Add(1)
	p.multiStateDB = badger_adaptor.New(bdb)
	p.Log().Infof("opened multi-state DB '%s'", dbname)

	// initialize the ledger library singleton with the ledger ID data from DB
	multistate.InitLedgerFromStore(p.multiStateDB)
	p.Log().Infof("ledger ID params:\n%s", ledger.L(0).ConstantsLines("       ").String())

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

	p.RepeatInBackground("Badger_DB_GC_loop", 30*time.Minute, func() bool {
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
	dbname := global.TxStoreDBName
	p.Log().Infof("transaction store database dbname is '%s'", dbname)

	// Set cache limits to prevent unbounded memory growth. Otherwise, it leaks memory. Claude Code fix
	opts := badger.DefaultOptions(dbname)
	opts.BlockCacheSize = 64 << 20 // 64MB block cache limit
	opts.IndexCacheSize = 32 << 20 // 32MB index cache limit
	opts.NumCompactors = 2         // reduce from default 4 to lower I/O contention

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

func (p *ProximaNode) databaseGC() {
	start := time.Now()
	err := p.multiStateDB.RunValueLogGC(0.5)
	p.Log().Infof("Badger DB GC, took %v, err = '%v'", time.Since(start), err)
}

// logUpgradesList logs all upgrades with their slots and library hashes.
// Activated upgrades are marked as "IN EFFECT", pending ones as "PENDING".
func (p *ProximaNode) logUpgradesList() {
	slots := ledger.GetAllUpgradeSlots(base.MaxSlot)
	if len(slots) == 0 {
		p.Log().Warnf("no upgrades found in ledger")
		return
	}

	currentSlot := ledger.TimeNow().Slot

	p.Log().Infof("ledger upgrades stored in the database:")
	for _, slot := range slots {
		lib := ledger.L(slot)
		hash := lib.Library.LibraryHash()
		status := "IN EFFECT"
		if slot > currentSlot {
			status = "PENDING"
		}
		p.Log().Infof("       slot %8d: %s  %s", slot, hex.EncodeToString(hash[:]), status)
	}
}

// initTxLogger initializes the transaction logger module.
// The logger starts disabled and can be enabled via API or config.
func (p *ProximaNode) initTxLogger() {
	p.txLogger = txlogger.New(p)
	p.Log().Infof("transaction logger initialized (disabled by default)")

	// Configure TTL if specified
	ttlHours := viper.GetInt("txlogger.ttl_hours")
	if ttlHours > 0 {
		p.txLogger.SetTTL(time.Duration(ttlHours) * time.Hour)
	}

	// Configure on/off API gate
	p.txLogOnOffAPI = viper.GetBool("txlogger.enable_on_off_api")

	// Auto-enable if configured.
	//
	// Diagnostics for the common silent-no-op trap: enable_on_start=true with a
	// missing / unknown / "off" level used to skip silently and leave the logger
	// disabled with no log line. Now: an explicit "off" warns and leaves
	// disabled (the config is internally contradictory); empty / unknown level
	// warns and defaults to "all" (matches the config-template default and the
	// user's evident intent).
	if viper.GetBool("txlogger.enable_on_start") {
		levelStr := viper.GetString("txlogger.level")
		level := parseTxLogLevel(levelStr)
		switch {
		case levelStr == "off":
			p.Log().Warnf(`txlogger: enable_on_start=true but level="off" — contradictory config, leaving disabled`)
		default:
			if level == global.TxLogLevelOff {
				// Empty or unknown string → default to "all".
				if levelStr == "" {
					p.Log().Warnf(`txlogger: enable_on_start=true but level is unset — defaulting to "all"`)
				} else {
					p.Log().Warnf(`txlogger: enable_on_start=true but level=%q is not recognised (valid: off/branch/sequencer/non_sequencer/all) — defaulting to "all"`, levelStr)
				}
				level = global.TxLogLevelAllTransactions
				levelStr = "all"
			}
			p.txLogger.TxLogEnable(level)
			p.Log().Infof("transaction logger auto-enabled with level: %s", levelStr)
		}
	}

	// Handle graceful shutdown
	go func() {
		<-p.Ctx().Done()
		if p.txLogger.IsEnabled() {
			if err := p.txLogger.Close(); err != nil {
				p.Log().Warnf("error closing transaction logger: %v", err)
			}
			p.Log().Infof("transaction logger closed")
		}
	}()
}

// parseTxLogLevel converts a string level name to TxLogLevel.
func parseTxLogLevel(s string) global.TxLogLevel {
	switch s {
	case "off", "":
		return global.TxLogLevelOff
	case "branch":
		return global.TxLogLevelBranchTransactionsOnly
	case "sequencer":
		return global.TxLogLevelSequencerTransactionsOnly
	case "non_sequencer":
		return global.TxLogLevelNonSequencerTransactionsOnly
	case "all":
		return global.TxLogLevelAllTransactions
	default:
		return global.TxLogLevelOff
	}
}
