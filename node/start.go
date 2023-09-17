package node

import (
	"os"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	state "github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/workflow"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type ProximaNode struct {
	log          *zap.SugaredLogger
	multiStateDB *badger.DB
	txStoreDB    *badger.DB
	txStore      general.TxBytesStore
	uTangle      *utangle.UTXOTangle
	workflow     *workflow.Workflow
}

func Start() *ProximaNode {
	log := newBootstrapLogger()
	log.Info(general.BannerString())

	initConfig(log)

	ret := &ProximaNode{
		log: newTopLogger(),
	}

	ret.startup()

	ret.log.Infof("Proxima node has been started successfully")
	ret.log.Debug("running in debug mode")

	return ret
}

func (p *ProximaNode) startup() {
	p.log.Info("starting up..")

	p.startMultiStateDB()
	p.startTxStore()
	p.loadUTXOTangle()
	p.startWorkflow()
}

func (p *ProximaNode) Stop() {
	p.log.Info("stopping the node..")
	if p.multiStateDB != nil {
		if err := p.multiStateDB.Close(); err == nil {
			p.log.Infof("multi-state database has been closed")
		} else {
			p.log.Warnf("error while closing multi-state database: %v", err)
		}
	}
	if p.txStoreDB != nil {
		if err := p.txStoreDB.Close(); err == nil {
			p.log.Infof("transaction store database has been closed")
		} else {
			p.log.Warnf("error while closing transaction store database: %v", err)
		}
	}
	if p.workflow != nil {
		p.workflow.Stop()
	}
	p.log.Info("node stopped")
}

func (p *ProximaNode) GetMultiStateDBName() string {
	return viper.GetString("multistate.name")
}

func (p *ProximaNode) startMultiStateDB() {
	dbname := p.GetMultiStateDBName()
	var err error
	p.multiStateDB, err = badger_adaptor.OpenBadgerDB(dbname)
	if err != nil {
		p.log.Fatalf("can't open '%s'", dbname)
	}
	p.log.Infof("opened multi-state DB '%s", dbname)
}

func (p *ProximaNode) startTxStore() {
	switch viper.GetString(general.ConfigKeyTxStoreType) {
	case "dummy":
		p.log.Infof("transaction store is 'dummy'")
		p.txStore = txstore.NewDummyTxBytesStore()

	case "db":
		name := viper.GetString(general.ConfigKeyTxStoreName)
		p.log.Infof("transaction store database name is '%s'", name)
		if name == "" {
			p.log.Errorf("transaction store database name not specified. Cannot start the node")
			p.Stop()
			os.Exit(1)
		}
		p.txStoreDB = badger_adaptor.MustCreateOrOpenBadgerDB(name)
		p.txStore = txstore.NewSimpleTxBytesStore(badger_adaptor.New(p.txStoreDB))
		p.log.Infof("opened DB '%s' as transaction store", name)

	case "url":
		panic("'url' type of transaction store is not supported yet")

	default:
		p.log.Errorf("transaction store type '%s' is wrong", viper.GetString(general.ConfigKeyTxStoreType))
		p.Stop()
		os.Exit(1)
	}
}

func (p *ProximaNode) loadUTXOTangle() {
	stateStore := badger_adaptor.New(p.multiStateDB)
	p.uTangle = utangle.Load(stateStore, p.txStore)
	latestSlot := p.uTangle.LatestTimeSlot()
	currentSlot := core.LogicalTimeNow().TimeSlot()
	p.log.Infof("current time slot: %d, latest time slot in the multi-state: %d, lagging behind: %d slots",
		currentSlot, latestSlot, currentSlot-latestSlot)

	branches := state.FetchLatestBranches(stateStore)
	p.log.Infof("latest time slot %d contains %d branches", latestSlot, len(branches))
	for _, br := range branches {
		txid := br.Stem.ID.TransactionID()
		p.log.Infof("    branch %s : sequencer: %s, coverage: %s", txid.Short(), br.SequencerID.Short(), util.GoThousands(br.Coverage))
	}
	p.log.Infof("UTXO tangle has been created successfully")
}

func (p *ProximaNode) startWorkflow() {
	p.workflow = workflow.New(p.uTangle, workflow.WithGlobalConfigOptions)
	p.workflow.Start()
}
