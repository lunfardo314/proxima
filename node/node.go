package node

import (
	"os"
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
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type (
	multiStateDB struct {
		*badger_adaptor.DB
	}
	txStoreDB struct {
		*badger_adaptor.DB
	}
)

type ProximaNode struct {
	*global.Global
	multiStateDB
	txStoreDB
	global.TxBytesStore
	peers      *peering.Peers
	workflow   *workflow.Workflow
	Sequencers []*sequencer.Sequencer
	stopOnce   sync.Once
}

const (
	bootstrapLoggerName = "[boot]"
)

func newBootstrapLogger() *zap.SugaredLogger {
	return global.NewLogger(bootstrapLoggerName, zap.InfoLevel, []string{"stderr"}, "")
}

func New() *ProximaNode {
	return &ProximaNode{
		Global:     global.New(),
		Sequencers: make([]*sequencer.Sequencer, 0),
	}
}

func (p *ProximaNode) initConfig() {
	pflag.Parse()
	err := viper.BindPFlags(pflag.CommandLine)
	util.AssertNoError(err)

	viper.SetConfigName("proxima")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err = viper.ReadInConfig()
	util.AssertNoError(err)
}

func (p *ProximaNode) Run() {
	p.Log().Info(global.BannerString())
	p.initConfig()

	p.Log().Info("---------------- starting up Proxima node ver. %s--------------", global.Version)

	err := util.CatchPanicOrError(func() error {
		p.initMultiStateLedger()
		p.initTxStore()
		p.initPeering()

		p.startWorkflow()
		p.startSequencers()
		p.startApiServer()
		p.startMemoryLogging()
		p.startPProfIfEnabled()
		return nil
	})
	if err != nil {
		p.Log().Errorf("error on startup: %v", err)
		os.Exit(1)
	}
	p.Log().Infof("Proxima node has been started successfully")
	p.Log().Debug("running in debug mode")
}

func (p *ProximaNode) WaitStop() {
	p.MustWaitStop()
	p.Log().Info("all components stopped")
}

// initMultiStateLedger opens ledger state DB and initializes global ledger object
func (p *ProximaNode) initMultiStateLedger() {
	var err error
	dbname := global.MultiStateDBName
	bdb, err := badger_adaptor.OpenBadgerDB(dbname)
	if err != nil {
		p.Log().Fatalf("can't open '%s'", dbname)
	}
	p.multiStateDB = multiStateDB{badger_adaptor.New(bdb)}
	p.Log().Infof("opened multi-state DB '%s", dbname)

	// initialize global ledger object with the ledger ID data from DB
	ledgerID := ledger.MustLedgerIdentityDataFromBytes(multistate.LedgerIdentityBytesFromStore(p.multiStateDB))
	ledger.Init(ledgerID)

	p.Log().Infof("Ledger identity:\n%s", ledgerID.Lines("       ").String())
	go func() {
		<-p.Ctx().Done()
		// wait until others will stop
		p.MustWaitStop()
		_ = p.multiStateDB.Close()
		p.Log().Infof("multi-state database has been closed")
	}()
}

func (p *ProximaNode) initTxStore() {
	switch viper.GetString(global.ConfigKeyTxStoreType) {
	case "dummy":
		p.Log().Infof("transaction store is 'dummy'")
		p.TxBytesStore = txstore.NewDummyTxBytesStore()

	case "url":
		panic("'url' type of transaction store is not supported yet")

	default:
		// default option is predefined database name
		dbname := global.TxStoreDBName
		p.Log().Infof("transaction store database dbname is '%s'", dbname)
		p.txStoreDB = txStoreDB{badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(dbname))}
		p.TxBytesStore = txstore.NewSimpleTxBytesStore(p.txStoreDB)
		p.Log().Infof("opened DB '%s' as transaction store", dbname)

		go func() {
			<-p.Ctx().Done()
			// wait until others will stop
			p.MustWaitStop()
			_ = p.txStoreDB.Close()
			p.Log().Infof("transaction store database has been closed")
		}()
	}
}

func (p *ProximaNode) initPeering() {
	var err error
	p.peers, err = peering.NewPeersFromConfig(p, p.ctx)
	util.AssertNoError(err)

	p.peers.Run()

	go func() {
		<-p.Ctx().Done()
		p.peers.Stop()
	}()
}

func (p *ProximaNode) startWorkflow() {
	p.workflow = workflow.New(p, p.peers, workflow.WithGlobalConfigOptions)
	p.workflow.Start(p.Ctx())
}

func (p *ProximaNode) startSequencers() {
	sequencers := viper.GetStringMap("Sequencers")
	if len(sequencers) == 0 {
		p.Log().Infof("No Sequencers will be started")
		return
	}
	p.Log().Infof("%d sequencer config profiles has been found", len(sequencers))

	seqNames := util.SortKeys(sequencers, func(k1, k2 string) bool {
		return k1 < k2
	})
	for _, name := range seqNames {
		seq, err := sequencer.NewFromConfig(name, p.workflow, p.ctx)
		if err != nil {
			p.Log().Errorf("can't start sequencer '%s': '%v'", name, err)
			continue
		}
		if seq == nil {
			p.Log().Infof("skipping sequencer '%s'", name)
			continue
		}
		seq.Run()

		p.Log().Infof("started sequencer '%s', seqID: %s", name, util.Ref(seq.SequencerID()).String())
		p.Sequencers = append(p.Sequencers, seq)
		time.Sleep(500 * time.Millisecond)
	}
}
