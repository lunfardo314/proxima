package node

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
)

type ProximaNode struct {
	*global.Global
	multiStateDB              *badger_adaptor.DB
	txStoreDB                 *badger_adaptor.DB
	txBytesStore              global.TxBytesStore
	peers                     *peering.Peers
	workflow                  *workflow.Workflow
	Sequencers                []*sequencer.Sequencer
	stopOnce                  sync.Once
	workProcessesStopStepChan chan struct{}
	dbClosedWG                sync.WaitGroup
	started                   time.Time
}

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
		Sequencers:                make([]*sequencer.Sequencer, 0),
		workProcessesStopStepChan: make(chan struct{}),
		started:                   time.Now(),
	}
	global.SetGlobalLogger(ret.Global)
	return ret
}

const waitAllProcessesStopTimeout = 10 * time.Second

// WaitAllWorkProcessesToStop wait everything to stop before closing databases
func (p *ProximaNode) WaitAllWorkProcessesToStop() {
	<-p.Ctx().Done()
	p.workProcessesStopStepChan <- struct{}{} // first step release DB close goroutines
	p.Log().Infof("waiting all processes to stop for up to %v", waitAllProcessesStopTimeout)
	p.Global.MustWaitAllWorkProcessesStop(waitAllProcessesStopTimeout)
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

func (p *ProximaNode) readInTraceTags() {
	p.Global.StartTracingTags(viper.GetStringSlice("trace_tags")...)
}

// Start starts the node
func (p *ProximaNode) Start() {
	p.Log().Info(global.BannerString())
	if p.IsBootstrapNode() {
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
		initStep = "startSequencers"
		p.startSequencers()
		initStep = "startAPIServer"
		p.startAPIServer()
		initStep = "startPProfIfEnabled"
		p.startPProfIfEnabled()
		return nil
	})
	if err != nil {
		p.Log().Fatalf("error during startup step '%s': %v", initStep, err)
	}
	p.Log().Infof("Proxima node has been started successfully")
	p.Log().Debug("running in debug mode")
	if p.LogAttacherStats() {
		p.Log().Infof("will be logging attacher stats")
	} else {
		p.Log().Infof("will NOT be logging attacher stats")
	}

	// periodic logging of general stats
	p.RepeatEvery(5*time.Second, func() bool {
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		p.Log().Infof("uptime: %v, allocated memory: %.1f MB, GC counter: %d, Goroutines: %d",
			time.Since(p.started).Round(time.Second),
			float32(memStats.Alloc*10/(1024*1024))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
		return true
	})
}

// initMultiStateLedger opens ledger state DB and initializes global ledger object
func (p *ProximaNode) initMultiStateLedger() {
	var err error
	dbname := global.MultiStateDBName
	bdb, err := badger_adaptor.OpenBadgerDB(dbname)
	if err != nil {
		p.Log().Fatalf("can't open '%s'", dbname)
	}
	p.dbClosedWG.Add(1)
	p.multiStateDB = badger_adaptor.New(bdb)
	p.Log().Infof("opened multi-state DB '%s'", dbname)

	// initialize global ledger object with the ledger ID data from DB
	multistate.InitLedgerFromStore(p.multiStateDB)
	p.Log().Infof("ledger identity:\n%s", ledger.L().ID.Lines("       ").String())
	h := ledger.L().LibraryHash()
	p.Log().Infof("ledger constraint library hash: %s", hex.EncodeToString(h[:]))

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
		p.txStoreDB = badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(dbname))
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
	p.workflow = workflow.NewFromConfig(p, p.peers)
	p.workflow.Start()
}

func (p *ProximaNode) startSequencers() {
	sequencers := viper.GetStringMap("sequencers")
	if len(sequencers) == 0 {
		p.Log().Infof("no Sequencers will be started")
		return
	}
	p.Log().Infof("%d sequencer config profile(s) has been found", len(sequencers))

	seqNames := util.KeysSorted(sequencers, func(k1, k2 string) bool {
		return k1 < k2
	})
	for _, name := range seqNames {
		seq, err := sequencer.NewFromConfig(name, p.workflow)
		if err != nil {
			p.Log().Errorf("can't start sequencer '%s': '%v'", name, err)
			continue
		}
		if seq == nil {
			p.Log().Infof("skipping disabled sequencer '%s'", name)
			continue
		}
		seq.Start()

		p.Sequencers = append(p.Sequencers, seq)
		time.Sleep(500 * time.Millisecond)
	}
}

func (p *ProximaNode) UpTime() time.Duration {
	return time.Since(p.started)
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

func (p *ProximaNode) SyncServerDisabled() bool {
	return viper.GetBool("workflow.sync_server.disable")
}
