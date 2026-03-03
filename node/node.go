package node

import (
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/easyfl/slicepool"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot_restore"
	"github.com/lunfardo314/proxima/core/core_modules/txlogger"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/diskusage"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
)

type (
	ProximaNode struct {
		*global.Global
		multiStateDB              *badger_adaptor.DB
		snapshotBranchID          base.TransactionID
		txStoreDB                 *badger_adaptor.DB
		txBytesStore              global.TxBytesStore
		txLogger                  *txlogger.TxLoggerModule
		txLogOnOffAPI             bool
		peers                     *peering.Peers
		sequencer                 *sequencer.Sequencer
		workflow                  *workflow.Workflow
		workProcessesStopStepChan chan struct{}
		dbClosedWG                sync.WaitGroup
		started                   time.Time
		metrics
	}

	metrics struct {
		lrbSlotsBehind        prometheus.Gauge
		lrbCoverage           prometheus.Gauge
		lrbSupply             prometheus.Gauge
		lrbNumTx              prometheus.Gauge
		pastConeSize          prometheus.Gauge
		numTxDependencies     prometheus.Gauge
		counterTxDependencies prometheus.Counter
		diskSpace             prometheus.Gauge
		validationTimeNs      prometheus.Gauge
		validationNumUTXO     prometheus.Gauge
		branchInflationBonus  prometheus.Gauge
	}
)

func New() *ProximaNode {
	viper.SetConfigName("proxima")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	util.AssertNoError(err)
	ret := &ProximaNode{
		Global:                    global.NewFromConfig(),
		workProcessesStopStepChan: make(chan struct{}),
		started:                   time.Now(),
	}
	ret.registerMetrics()
	return ret
}

const waitAllProcessesStopTimeout = 20 * time.Second

// WaitAllWorkProcessesStopped wait everything to stop before closing databases
func (p *ProximaNode) WaitAllWorkProcessesStopped() {
	<-p.Ctx().Done()
	p.workProcessesStopStepChan <- struct{}{} // first step release DB close goroutines
	p.Log().Infof("waiting all processes to stop for up to %v", waitAllProcessesStopTimeout)
	p.Global.WaitAllWorkProcessesStop(waitAllProcessesStopTimeout)
	close(p.workProcessesStopStepChan) // second step signals to release DB close goroutines
}

// WaitAllDBClosed ensuring databases have been closed
func (p *ProximaNode) WaitAllDBClosed() {
	p.dbClosedWG.Wait()
}

func (p *ProximaNode) StateStore() global.Store {
	return p.multiStateDB
}

func (p *ProximaNode) TxBytesStore() global.TxBytesStore {
	return p.txBytesStore
}

func (p *ProximaNode) PullFromPeers(txid base.TransactionID) int {
	return p.peers.PullTransactionsFromPeers(txid)
}

func (p *ProximaNode) GetOwnSequencerID() *base.ChainID {
	if p.sequencer == nil {
		return nil
	}
	return util.Ref(p.sequencer.SequencerID())
}

func (p *ProximaNode) readInTraceTags() {
	p.Global.StartTracingTags(viper.GetStringSlice("trace_tags")...)
}

// Start starts the node
func (p *ProximaNode) Start() {
	p.Log().Infof(global.BannerString())
	p.readInTraceTags()

	var initStep string

	if viper.GetBool("disable_slicepool") {
		slicepool.Disable() // disables optimized memory allocation in EasyFL and just uses standard make({}byte, size)
		p.Log().Infof("DISABLE optimized memory allocation in EasyFL")
	} else {
		p.Log().Infof("optimized memory allocation in EasyFL ENABLED")
	}

	err := util.CatchPanicOrError(func() error {
		initStep = "startMetrics"
		p.startMetrics()
		initStep = "checkAndRestoreOnStartup"
		if restored, err := snapshot_restore.CheckAndRestoreOnStartup(p); err != nil {
			return fmt.Errorf("state cleanup restore failed: %w", err)
		} else if restored {
			p.Log().Infof("state restored from snapshot, continuing with normal startup")
		}
		initStep = "initMultiStateLedger"
		p.initMultiStateLedger()
		initStep = "initTxStore"
		p.initTxStore()
		initStep = "initTxLogger"
		p.initTxLogger()
		initStep = "initPeering"
		p.initPeering()

		initStep = "startWorkflow"
		p.startWorkflow()
		initStep = "startSequencer"
		p.startSequencer()
		initStep = "startAPIServer"
		p.startAPIServer()
		p.startStreaming()
		initStep = "startPProfIfEnabled"
		p.startPProfIfEnabled()
		return nil
	}, true)
	if err != nil {
		p.Log().Fatalf("error during startup step '%s': %v", initStep, err)
	}
	p.Log().Infof("Proxima node has been started successfully")
	p.Log().Debug("running in debug mode")

	p.goLoggingMemStats()
	p.goLoggingSync()
}

func (p *ProximaNode) initPeering() {
	var err error
	p.peers, err = peering.NewPeersFromConfig(p)
	util.AssertNoError(err)

	p.peers.Run()

	go func() {
		<-p.Ctx().Done()
		p.peers.Stop()
	}()
}

func (p *ProximaNode) startWorkflow() {
	p.workflow = workflow.StartFromConfig(p, p.peers)
}

func (p *ProximaNode) startSequencer() {
	var err error
	p.sequencer, err = sequencer.NewFromConfig(p.workflow)
	if err != nil {
		p.Log().Errorf("can't start sequencer: '%v'", err)
		return
	}
	if p.sequencer == nil {
		p.Log().Infof("sequencer is not configured or disabled")
		return
	}
	p.sequencer.Start()
}

const defaultMetricsPort = 14000

func (p *ProximaNode) startMetrics() {
	if !viper.GetBool("metrics.enable") {
		p.Log().Infof("Prometheus metrics disabled")
		return
	}
	port := viper.GetInt("metrics.port")
	if port == 0 {
		p.Log().Warnf("metrics.port not specified. Will use %d for Prometheus metrics exposure", defaultMetricsPort)
		port = defaultMetricsPort
	}
	reg := p.MetricsRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewBuildInfoCollector(),
	)
	go func() {
		http.Handle("/metrics", promhttp.HandlerFor(
			reg,
			promhttp.HandlerOpts{
				Registry: reg,
			},
		))
		p.Log().Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
	}()
	p.Log().Infof("Prometheus metrics exposed on port %d", port)
}

func (p *ProximaNode) goLoggingMemStats() {
	const memStatsLogPeriodDefault = 10 * time.Second

	var memStats runtime.MemStats
	p.RepeatInBackground("logging_memStats", memStatsLogPeriodDefault, func() bool {
		runtime.ReadMemStats(&memStats)
		_, availableHDD, _ := diskusage.GetDiskUsage("/")
		availableMB := float64(availableHDD) / (1 << 20)
		p.diskSpace.Set(availableMB)

		availableGB := float64(availableHDD) / (1 << 30)
		diskSpace := ""
		if availableGB > 0 {
			diskSpace = fmt.Sprintf(", available disk space: %.2f GB", availableGB)
		}
		p.Log().Infof("[memstats] current slot: %d, [%s], uptime: %v, allocated memory: %.1f MB, GC counter: %d, Goroutines: %d%s",
			ledger.TimeNow().Slot,
			p.CounterLines().Join(","),
			time.Since(p.started).Round(time.Second),
			float32(memStats.Alloc*10/(1<<20))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
			diskSpace,
		)

		if availableGB > 0 && availableGB < 2 {
			p.Log().Warnf("------- available disk space is < 2 GB !!! ----------")
		}
		return true
	})
}

func (p *ProximaNode) goLoggingSync() {
	const (
		syncLogPeriodDefault = 10 * time.Second
		slotSyncThreshold    = 5
	)

	p.RepeatInBackground("logging_sync", syncLogPeriodDefault, func() bool {
		start := time.Now()
		lrb := p.GetLatestReliableBranch()
		if lrb == nil {
			p.Log().Warnf("[sync] can't find latest reliable branch")
		} else {
			curSlot := ledger.TimeNow().Slot
			slotsBehind := curSlot - lrb.Stem.ID.Slot()
			p.lrbSlotsBehind.Set(float64(slotsBehind))
			cov := p.workflow.Branches().LedgerCoverage(lrb.TxID())
			msg := fmt.Sprintf("[sync] latest reliable branch is %d slots behind from now, current slot: %d, coverage: %s (%v)",
				slotsBehind, curSlot, util.Th(cov), time.Since(start))
			if slotsBehind <= slotSyncThreshold {
				p.Log().Info(msg)
			} else {
				p.Log().Warn(msg)
			}

			p.lrbCoverage.Set(float64(cov))
			p.lrbSupply.Set(float64(lrb.Supply))
			p.lrbNumTx.Set(float64(lrb.NumTransactions))
		}
		return true
	})
}

func (p *ProximaNode) registerMetrics() {
	p.lrbCoverage = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_coverage",
		Help: "ledger coverage of the latest reliable branch (LRB)",
	})
	p.lrbSlotsBehind = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_slots_behind",
		Help: "latest reliable branch (LRB) slots behind the current slot",
	})
	p.lrbSupply = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_supply",
		Help: "total supply on the latest reliable branch (LRB)",
	})
	p.lrbNumTx = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_num_tx",
		Help: "number of transactions committed on the latest reliable branch (LRB)",
	})
	p.pastConeSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_past_cone_size",
		Help: "number of transactions in the past cone delta of the sequencer transaction",
	})
	p.numTxDependencies = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_num_tx_dependencies",
		Help: "number of inputs plus endorsements in the transaction",
	})
	p.counterTxDependencies = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_counter_tx_dependencies",
		Help: "cumulative number of inputs plus endorsements in the transaction",
	})
	p.diskSpace = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_disk_space",
		Help: "available disk space in MB",
	})
	p.validationTimeNs = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_tx_validation_time_ns",
		Help: "transaction validation time in nanoseconds",
	})
	p.validationNumUTXO = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_tx_validation_num_utxo",
		Help: "total number of inputs and outputs in the transaction",
	})
	p.branchInflationBonus = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_branch_inflation_bonus",
		Help: "branch inflation bonus values of attached branches",
	})

	p.MetricsRegistry().MustRegister(
		p.lrbCoverage,
		p.lrbSlotsBehind,
		p.lrbSupply,
		p.lrbNumTx,
		p.pastConeSize,
		p.numTxDependencies,
		p.counterTxDependencies,
		p.diskSpace,
		p.validationTimeNs,
		p.validationNumUTXO,
		p.branchInflationBonus,
	)
}

func (p *ProximaNode) EvidencePastConeSize(sz int) {
	p.pastConeSize.Set(float64(sz))
}

func (p *ProximaNode) EvidenceNumberOfTxDependencies(n int) {
	p.numTxDependencies.Set(float64(n))
	p.counterTxDependencies.Add(float64(n))
}

func (p *ProximaNode) SnapshotBranchID() base.TransactionID {
	return p.snapshotBranchID
}

func (p *ProximaNode) DurationSinceLastMessageFromPeer() time.Duration {
	return p.peers.DurationSinceLastMessageFromPeer()
}

func (p *ProximaNode) EvidenceTxValidationStats(took time.Duration, numIn, numOut int) {
	p.validationTimeNs.Set(float64(took.Nanoseconds()))
	p.validationNumUTXO.Set(float64(numIn + numOut))
}

func (p *ProximaNode) EvidenceBranchInflationBonus(ib uint64) {
	p.branchInflationBonus.Set(float64(ib))
}

func (p *ProximaNode) CheckTxSenderConfig() (checkSeq, checkNonSeq bool) {
	// in tests it may be differently to avoid problems with reusing private keys
	return true, true
}

// TxLogger returns the transaction logger module.
func (p *ProximaNode) TxLogger() *txlogger.TxLoggerModule {
	return p.txLogger
}

// LogTx logs a message for the given transaction(s) with the specified clock timestamp.
// No-op if txLogger is nil or disabled.
func (p *ProximaNode) LogTx(clockTs time.Time, msg string, txid ...base.TransactionID) {
	if p.txLogger != nil {
		p.txLogger.TxLog(clockTs, msg, txid...)
	}
}
