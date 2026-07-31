package node

import (
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lunfardo314/easyfl/slicepool"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot_restore"
	"github.com/lunfardo314/proxima/core/core_modules/txlogger"
	"github.com/lunfardo314/proxima/core/vertex"
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
		pastConeSize          prometheus.Gauge
		numTxDependencies     prometheus.Gauge
		counterTxDependencies prometheus.Counter
		diskSpace             prometheus.Gauge
		validationTimeNs      prometheus.Gauge
		validationNumUTXO     prometheus.Gauge
		branchInflationBonus  prometheus.Gauge
		branchMutations       prometheus.Counter
		branchCounter         prometheus.Counter
		txValidatedTotal      prometheus.Counter
		txConfirmedTotal      prometheus.Counter
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

// ConsensusContribution overrides the default (0) from the embedded *global.Global:
// reports the running sequencer's own consensus mass (0 if no sequencer). Consumed by
// the peering connectivity overlay (see claude/network_connectivity.md).
func (p *ProximaNode) ConsensusContribution() uint64 {
	if p.sequencer == nil {
		return 0
	}
	return p.sequencer.ConsensusContribution()
}

func (p *ProximaNode) readInTraceTags() {
	p.Global.StartTracingTags(viper.GetStringSlice("trace_tags")...)
}

// readInHealthRelief installs the health-relief window (see global.SetHealthRelief) and refuses
// to start on the boolean key it replaces. Relief is a convention the whole network takes
// together, so a node which silently started with a different rule than its peers — either by
// carrying a key nobody reads any more, or by reverting to full enforcement — is exactly the
// failure the window is meant to avoid.
func (p *ProximaNode) readInHealthRelief() {
	if viper.IsSet("suppress_health_enforcement") {
		p.Log().Fatalf("config key 'suppress_health_enforcement' is no longer supported. Use the 'health_relief' section " +
			"(from_slot, to_slot, numerator, denominator), with the same values on every node")
	}
	if !viper.IsSet("health_relief") {
		return
	}
	fromSlot := uint32(viper.GetInt("health_relief.from_slot"))
	toSlot := uint32(viper.GetInt("health_relief.to_slot"))
	fraction := global.Fraction{
		Numerator:   viper.GetInt("health_relief.numerator"),
		Denominator: viper.GetInt("health_relief.denominator"),
	}
	if err := global.SetHealthRelief(fromSlot, toSlot, fraction); err != nil {
		p.Log().Fatalf("%v", err)
	}
	p.Log().Warnf("HEALTH RELIEF configured: slots [%d, %d], healthy-branch fraction %s instead of %s. "+
		"Every node on the network must run the same window and the same fraction",
		fromSlot, toSlot, fraction.String(), global.FractionHealthyBranch().String())
}

// Start starts the node
func (p *ProximaNode) Start() {
	p.Log().Infof(global.BannerString())
	p.readInTraceTags()
	p.readInHealthRelief()

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
		initStep = "startMemDAGDebugAPIIfEnabled"
		p.startMemDAGDebugAPIIfEnabled()
		return nil
	}, true)
	if err != nil {
		p.Log().Fatalf("error during startup step '%s': %v", initStep, err)
	}
	p.Log().Infof("Proxima node has been started successfully")
	p.Log().Debug("running in debug mode")

	p.initMemoryLimit()
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
	// Bootstrap-from-old-state (sync_semantics.md §5.2 scenario 7): the startup decision
	// determined the whole network is stalled at an old state with no fresher snapshot to
	// adopt. The node will never become "synced", so force the sequencer to start (else it
	// would wait forever) so it can issue the bootstrap transactions that advance the network.
	var extraOpts []sequencer.ConfigOption
	if snapshot_restore.BootstrapFromOldState.Load() {
		p.Log().Warnf("bootstrap-from-old-state: forcing the sequencer to start without waiting for sync")
		extraOpts = append(extraOpts, sequencer.WithDoNotWaitForSync)
	}
	seq, err := sequencer.NewFromConfig(p.workflow, extraOpts...)
	if err != nil {
		p.Log().Errorf("can't start sequencer: '%v'", err)
		return
	}
	if seq == nil {
		p.Log().Infof("sequencer is not configured or disabled")
		return
	}
	p.sequencer = seq
	p.Log().Infof("starting sequencer")
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

// defaultGOGC is the default GOGC value when memory.limit_mb is configured.
// Lower than Go's default (100) to keep the heap compact and prevent GC stall
// during allocation bursts. With GOGC=50, GC triggers when heap grows to 1.5x
// live data (vs 2x at default 100), running ~2x more often.
const defaultGOGC = 50

func (p *ProximaNode) initMemoryLimit() {
	limitBytes := p.MemLimitBytes()
	if limitBytes == 0 {
		return
	}
	limitMB := limitBytes >> 20
	debug.SetMemoryLimit(int64(limitBytes))
	p.Log().Infof("[memory] soft GC limit set to %d MB", limitMB)

	// set GOGC: configurable via memory.gogc, default 50
	gogc := viper.GetInt("memory.gogc")
	if gogc <= 0 {
		gogc = defaultGOGC
	}
	oldGOGC := debug.SetGCPercent(gogc)
	p.Log().Infof("[memory] GOGC set to %d (was %d)", gogc, oldGOGC)

	// memory watchdog: graceful shutdown when stress reaches 100% (allocated >= limit)
	p.RepeatInBackground("memory_watchdog", 5*time.Second, func() bool {
		stress := p.MemoryStressLevel()
		if stress >= 100 {
			p.GracefulShutdown(fmt.Sprintf("memory stress %d%% (allocated >= limit %d MB)", stress, limitMB))
			return false
		}
		if stress >= 80 {
			p.Log().Warnf("[memory] stress %d%% approaching limit %d MB", stress, limitMB)
		}
		return true
	})
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
		pipelineStr := ""
		if p.workflow != nil {
			pipelineStr = fmt.Sprintf(", pipeline: %d", p.workflow.PipelineSize())
		}
		p.Log().Infof("[memstats] current slot: %d, [%s]%s, uptime: %v, allocated memory: %.1f MB, GC counter: %d, Goroutines: %d%s",
			ledger.TimeNow().Slot,
			p.CounterLines().Join(","),
			pipelineStr,
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

	// proxima_tx_confirmed_total is bumped here. lrb.NumConfirmedTransactions is the
	// per-branch count of new (non-rooted) transactions this branch is committing
	// — i.e. the slot's delta, not a cumulative total — so on each observed LRB
	// advance to a higher slot we simply add lrb.NumConfirmedTransactions to the counter.
	// During forking/lineage switches the LRB slot can stand still or wobble; the
	// metric is approximate over those windows but they're rare in steady state.
	var prevLRBSlot uint32

	p.RepeatInBackground("logging_sync", syncLogPeriodDefault, func() bool {
		start := time.Now()
		lrb := p.GetLatestReliableBranch()
		if lrb == nil {
			p.Log().Warnf("[sync] can't find latest reliable branch")
			return true
		}
		curSlot := ledger.TimeNow().Slot
		lrbSlot := lrb.Stem.ID.Slot()
		slotsBehind := curSlot - lrbSlot
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

		if lrbSlot > prevLRBSlot {
			p.txConfirmedTotal.Add(float64(lrb.NumConfirmedTransactions))
			prevLRBSlot = lrbSlot
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
	p.branchMutations = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_branch_mutations",
		Help: "cumulative number of mutation commands in branch commits",
	})
	p.branchCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_branch_counter",
		Help: "cumulative number of branch commits on this node (one increment per committed branch, competing branches included)",
	})
	p.txValidatedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_tx_validated_total",
		Help: "cumulative number of transactions that passed Stage-3 constraint validation on this node (one increment per tx)",
	})
	p.txConfirmedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_tx_confirmed_total",
		Help: "cumulative number of transactions confirmed in the latest reliable branch (LRB). On each observed LRB advance to a higher slot, lrb.NumConfirmedTransactions (per-branch slot delta) is added.",
	})

	p.MetricsRegistry().MustRegister(
		p.lrbCoverage,
		p.lrbSlotsBehind,
		p.lrbSupply,
		p.pastConeSize,
		p.numTxDependencies,
		p.counterTxDependencies,
		p.diskSpace,
		p.validationTimeNs,
		p.validationNumUTXO,
		p.branchInflationBonus,
		p.branchMutations,
		p.branchCounter,
		p.txValidatedTotal,
		p.txConfirmedTotal,
	)
}

func (p *ProximaNode) EvidencePastConeSize(sz int) {
	p.pastConeSize.Set(float64(sz))
}

func (p *ProximaNode) EvidenceNumberOfTxDependencies(n int) {
	p.numTxDependencies.Set(float64(n))
	p.counterTxDependencies.Add(float64(n))
}


func (p *ProximaNode) DurationSinceLastMessageFromPeer() time.Duration {
	return p.peers.DurationSinceLastMessageFromPeer()
}

func (p *ProximaNode) IsConnectedToNetwork() bool {
	return p.peers.IsConnectedToNetwork()
}

func (p *ProximaNode) EvidenceTxValidationStats(took time.Duration, numIn, numOut int) {
	p.validationTimeNs.Set(float64(took.Nanoseconds()))
	p.validationNumUTXO.Set(float64(numIn + numOut))
	p.txValidatedTotal.Inc()
}

func (p *ProximaNode) EvidenceBranchInflationBonus(ib uint64) {
	p.branchInflationBonus.Set(float64(ib))
}

func (p *ProximaNode) EvidenceBranchMutations(numMutations int) {
	p.branchMutations.Add(float64(numMutations))
	p.branchCounter.Inc()
}

func (p *ProximaNode) CheckTxSenderConfig() (checkSeq, checkNonSeq bool) {
	// in tests it may be differently to avoid problems with reusing private keys
	return true, true
}

// IsVertexReferencedBySequencer returns true if the vertex is referenced by the sequencer's
// tippool, backlog, or own milestones. Returns false if no sequencer is running.
func (p *ProximaNode) IsVertexReferencedBySequencer(vid *vertex.WrappedTx) bool {
	if p.sequencer == nil {
		return false
	}
	// check tippool (workflow-level)
	if p.workflow.IsVertexReferencedInTippool(vid) {
		return true
	}
	// check own milestones and backlog (sequencer-level)
	return p.sequencer.IsVertexReferenced(vid)
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
