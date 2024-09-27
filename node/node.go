package node

import (
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util"
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
		peers                     *peering.Peers
		sequencer                 *sequencer.Sequencer
		workflow                  *workflow.Workflow
		stopOnce                  sync.Once
		workProcessesStopStepChan chan struct{}
		dbClosedWG                sync.WaitGroup
		started                   time.Time
		metrics
	}

	metrics struct {
		lrbSlotsBehind prometheus.Gauge
		lrbCoverage    prometheus.Gauge
		lrbSupply      prometheus.Gauge
		lrbNumTx       prometheus.Gauge
	}
)

func init() {
	viper.SetConfigName("proxima")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	util.AssertNoError(err)
}

func New() *ProximaNode {
	ret := &ProximaNode{
		Global:                    global.NewFromConfig(),
		workProcessesStopStepChan: make(chan struct{}),
		started:                   time.Now(),
	}
	ret.registerMetrics()
	global.SetGlobalLogger(ret.Global)
	return ret
}

const waitAllProcessesStopTimeout = 20 * time.Second

// WaitAllWorkProcessesToStop wait everything to stop before closing databases
func (p *ProximaNode) WaitAllWorkProcessesToStop() {
	<-p.Ctx().Done()
	p.workProcessesStopStepChan <- struct{}{} // first step release DB close goroutines
	p.Log().Infof("waiting all processes to stop for up to %v", waitAllProcessesStopTimeout)
	p.Global.WaitAllWorkProcessesStop(waitAllProcessesStopTimeout)
	close(p.workProcessesStopStepChan) // second step signals to release DB close goroutines
}

// WaitAllDBClosed ensuring databases has been closed
func (p *ProximaNode) WaitAllDBClosed() {
	p.dbClosedWG.Wait()
}

func (p *ProximaNode) StateStore() global.StateStore {
	return p.multiStateDB
}

func (p *ProximaNode) TxBytesStore() global.TxBytesStore {
	return p.txBytesStore
}

func (p *ProximaNode) PullFromRandomPeers(nPeers int, txid *ledger.TransactionID) int {
	return p.peers.PullTransactionsFromRandomPeers(nPeers, *txid)
}

func (p *ProximaNode) GetOwnSequencerID() *ledger.ChainID {
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
	p.Log().Info(global.BannerString())
	if p.IsBootstrapMode() {
		p.Log().Info("it is a bootstrap node")
	}
	p.readInTraceTags()

	var initStep string

	err := util.CatchPanicOrError(func() error {
		initStep = "startMetrics"
		p.startMetrics()
		initStep = "initMultiStateLedger"
		p.initMultiStateLedger()
		initStep = "initTxStore"
		p.initTxStore()
		initStep = "initPeering"
		p.initPeering()

		initStep = "startWorkflow"
		p.startWorkflow()
		initStep = "startSequencer"
		p.startSequencer()
		initStep = "startAPIServer"
		p.startAPIServer()
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
	const memstatsLogPeriodDefault = 10 * time.Second

	memStatsPeriod := time.Duration(viper.GetInt("logger.memstats_period_sec")) * time.Second
	if memStatsPeriod == 0 {
		memStatsPeriod = memstatsLogPeriodDefault
	}

	var memStats runtime.MemStats
	p.RepeatInBackground("logging_memStats", memStatsPeriod, func() bool {
		runtime.ReadMemStats(&memStats)
		p.Log().Infof("[memstats] current slot: %d, [%s], uptime: %v, allocated memory: %.1f MB, GC counter: %d, Goroutines: %d",
			ledger.TimeNow().Slot(),
			p.CounterLines().Join(","),
			time.Since(p.started).Round(time.Second),
			float32(memStats.Alloc*10/(1024*1024))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
		return true
	})
}

func (p *ProximaNode) goLoggingSync() {
	const (
		syncLogPeriodDefault = 5 * time.Second
		slotSyncThreshold    = 5
	)

	logSyncPeriod := time.Duration(viper.GetInt("logger.syncstate_period_sec")) * time.Second
	if logSyncPeriod == 0 {
		logSyncPeriod = syncLogPeriodDefault
	}

	p.RepeatInBackground("logging_sync", logSyncPeriod, func() bool {
		start := time.Now()
		lrb := p.GetLatestReliableBranch()
		if lrb == nil {
			p.Log().Warnf("[sync] can't find latest reliable branch")
		} else {
			curSlot := ledger.TimeNow().Slot()
			slotsBehind := curSlot - lrb.Stem.ID.Slot()
			p.lrbSlotsBehind.Set(float64(slotsBehind))
			msg := fmt.Sprintf("[sync] latest reliable branch is %d slots behind from now, current slot: %d, coverage: %s (took: %v)",
				slotsBehind, curSlot, util.Th(lrb.LedgerCoverage), time.Since(start))
			if slotsBehind <= slotSyncThreshold {
				p.Log().Info(msg)
			} else {
				p.Log().Warn(msg)
			}

			p.lrbCoverage.Set(float64(lrb.LedgerCoverage))
			p.lrbSupply.Set(float64(lrb.Supply))
			p.lrbNumTx.Set(float64(lrb.NumTransactions))
		}
		return true
	})
}

func (p *ProximaNode) registerMetrics() {
	p.lrbCoverage = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_coverage",
		Help: "ledger coverage of the latest reliable branch (LRBID)",
	})
	p.lrbSlotsBehind = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_slots_behind",
		Help: "latest reliable branch (LRBID) slots behind the current slot",
	})
	p.lrbSupply = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_supply",
		Help: "total supply on the latest reliable branch (LRBID)",
	})

	p.lrbNumTx = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_lrb_num_tx",
		Help: "number of transactions committed on the latest reliable branch (LRBID)",
	})
	p.MetricsRegistry().MustRegister(p.lrbCoverage, p.lrbSlotsBehind, p.lrbSupply, p.lrbNumTx)
}
