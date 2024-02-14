package node

import (
	"context"
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
	*global.DefaultLogging
	multiStateDB
	txStoreDB
	global.TxBytesStore
	Peers      *peering.Peers
	Workflow   *workflow.Workflow
	Sequencers []*sequencer.Sequencer
	stopOnce   sync.Once
	ctx        context.Context
}

func New(ctx context.Context) *ProximaNode {
	return &ProximaNode{
		Sequencers: make([]*sequencer.Sequencer, 0),
		ctx:        ctx,
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
	newBootstrapLogger().Info(global.BannerString())
	p.initConfig()

	p.DefaultLogging = newNodeLoggerFromConfig()
	p.Log().Info("---------------- starting up Proxima node --------------")
	//global.ReadInTraceFlags(p.log)

	err := util.CatchPanicOrError(func() error {
		p.startPProfIfEnabled()
		p.startMultiStateDB()
		p.startTxStore()
		p.loadUTXOTangle()
		p.startPeering()
		p.startWorkflow()
		p.startSequencers()
		p.startApiServer()
		p.startMemoryLogging()
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
	p.Workflow.WaitStop() // TODO not correct. Must be global stop wait group for all components
	p.Log().Info("Workflow stopped")
}

func (p *ProximaNode) startMultiStateDB() {
	var err error
	dbname := global.MultiStateDBName
	bdb, err := badger_adaptor.OpenBadgerDB(dbname)
	if err != nil {
		p.Log().Fatalf("can't open '%s'", dbname)
	}
	p.multiStateDB = multiStateDB{badger_adaptor.New(bdb)}
	p.Log().Infof("opened multi-state DB '%s", dbname)

	// init ledger with the ledger ID data from DB
	ledgerID := ledger.MustLedgerIdentityDataFromBytes(multistate.LedgerIdentityBytesFromStore(p.multiStateDB))
	ledger.Init(ledgerID)

	p.Log().Infof("Ledger identity:\n%s", ledgerID.Lines("       ").String())

	go func() {
		<-p.ctx.Done()

		_ = p.multiStateDB.Close()
		p.Log().Infof("multi-state database has been closed")
	}()
}

func (p *ProximaNode) startTxStore() {
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
			<-p.ctx.Done()

			_ = p.txStoreDB.Close()
			p.Log().Infof("transaction store database has been closed")
		}()
	}
}

// MustCompatibleStateBootstrapData branches of the latest slot sorted by coverage descending
func mustReadStateIdentity(store global.StateStore) {
	rootRecords := multistate.FetchRootRecords(store, multistate.FetchLatestSlot(store))
	util.Assertf(len(rootRecords) > 0, "at least on root record expected")
	stateReader, err := multistate.NewSugaredReadableState(store, rootRecords[0].Root)
	util.AssertNoError(err)

	// it will panic if constraint libraries are incompatible
	ledger.MustLedgerIdentityDataFromBytes(stateReader.MustLedgerIdentityBytes())
}

func (p *ProximaNode) loadUTXOTangle() {
	//mustReadStateIdentity(p.multiStateStore)
	//
	//p.UTXOTangle = utangle_old.Load(p.multiStateStore)
	//latestSlot := p.UTXOTangle.LatestTimeSlot()
	//currentSlot := ledger.TimeNow().Slot()
	//p.Log().Infof("current time slot: %d, latest time slot in the multi-state: %d, lagging behind: %d slots",
	//	currentSlot, latestSlot, currentSlot-latestSlot)
	//
	//branches := multistate.FetchLatestBranches(p.multiStateStore)
	//p.Log().Infof("latest time slot %d contains %d branches", latestSlot, len(branches))
	//for _, br := range branches {
	//	txid := br.Stem.ID.TransactionID()
	//	p.Log().Infof("    branch %s : sequencer: %s, coverage: %s", txid.StringShort(), br.SequencerID.StringShort(), br.LedgerCoverage.String())
	//}
	//p.Log().Infof("UTXO tangle has been created successfully")
	p.Log().Infof("not implemented")
}

func (p *ProximaNode) startPeering() {
	var err error
	p.Peers, err = peering.NewPeersFromConfig(p, p.ctx)
	util.AssertNoError(err)

	p.Peers.Run()
}

func (p *ProximaNode) startWorkflow() {
	p.Workflow = workflow.New(p, p.Peers, workflow.WithGlobalConfigOptions)
	p.Workflow.Start(p.ctx)
}

func (p *ProximaNode) startSequencers() {
	//traceProposers := viper.GetStringMap("debug.trace_proposers")
	viper.GetStringMap("debug.trace_proposers")

	//for pname := range traceProposers {
	//	if viper.GetBool("debug.trace_proposers." + pname) {
	//		sequencer.SetTraceProposer(pname, true)
	//		p.log.Infof("will be tracing proposer '%s'", pname)
	//	}
	//}

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
		seq, err := sequencer.NewFromConfig(p.Workflow, name)
		if err != nil {
			p.Log().Errorf("can't start sequencer '%s': '%v'", name, err)
			continue
		}
		if seq == nil {
			p.Log().Infof("skipping sequencer '%s'", name)
			continue
		}
		seq.Run(p.ctx)

		p.Log().Infof("started sequencer '%s', seqID: %s", name, seq.ID().String())
		p.Sequencers = append(p.Sequencers, seq)
		time.Sleep(500 * time.Millisecond)
	}
}
