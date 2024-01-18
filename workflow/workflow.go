package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/transaction/txmetadata"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/queue"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Workflow struct {
		stopFun         context.CancelFunc
		startOnce       sync.Once
		stopOnce        sync.Once
		working         atomic.Bool
		startPrunerOnce sync.Once
		log             *zap.SugaredLogger
		configParams    ConfigParams
		utxoTangle      *utangle_old.UTXOTangle
		peers           *peering.Peers
		txBytesStore    global.TxBytesStore
		debugCounters   *testutil.SyncCounters

		primaryInputConsumer *PrimaryConsumer
		preValidateConsumer  *PreValidateConsumer
		solidifyConsumer     *SolidifyConsumer
		pullConsumer         *PullTxConsumer
		validateConsumer     *ValidateConsumer
		appendTxConsumer     *AppendTxConsumer
		eventsConsumer       *EventsConsumer
		pullRequestConsumer  *PullRespondConsumer
		txGossipOutConsumer  *TxGossipSendConsumer

		handlersMutex sync.RWMutex
		eventHandlers map[eventtype.EventCode][]func(any)

		terminateWG sync.WaitGroup
		startWG     sync.WaitGroup

		traceMilestones atomic.Bool
	}

	Consumer[T any] struct {
		*queue.Queue[T]
		glb       *Workflow
		traceFlag bool
	}

	DropTxData struct {
		TxID       *ledger.TransactionID
		WhoDropped string
		Msg        string
	}
)

var EventDroppedTx = eventtype.RegisterNew[DropTxData]("droptx")

const workflowLogName = "[workflow_old]"

func New(ut *utangle_old.UTXOTangle, peers *peering.Peers, txBytesStore global.TxBytesStore, configOptions ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range configOptions {
		opt(&cfg)
	}

	ret := &Workflow{
		configParams:  cfg,
		log:           global.NewLogger(workflowLogName, cfg.logLevel, cfg.logOutput, cfg.logTimeLayout),
		utxoTangle:    ut,
		peers:         peers,
		txBytesStore:  txBytesStore,
		debugCounters: testutil.NewSynCounters(),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.initPrimaryInputConsumer()
	ret.initPreValidateConsumer()
	ret.initSolidifyConsumer()
	ret.initPullConsumer()
	ret.initValidateConsumer()
	ret.initAppendTxConsumer()
	ret.initEventsConsumer()
	ret.initRespondTxQueryConsumer()
	ret.initGossipSendConsumer()

	ret.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, txMetadata *txmetadata.TransactionMetadata) {
		if !ret.working.Load() {
			return
		}
		err := ret.TransactionIn(txBytes,
			WithTransactionSourcePeer(from),
			WithTransactionMetadata(txMetadata),
			WithTraceCondition(func(tx *transaction.Transaction, _ TransactionSource, _ peer.ID) bool {
				return tx.IsSequencerMilestone()
			},
			))
		if err != nil {
			ret.log.Debugf("TxBytesIn: %v", err)
			return
		}
	})

	ret.peers.OnReceivePullRequest(func(from peer.ID, txids []ledger.TransactionID) {
		if !ret.working.Load() {
			return
		}

		for _, txid := range txids {
			global.TracePull(ret.pullRequestConsumer.Log(), "pull request received for %s", func() any { return txid.StringShort() })
			ret.pullRequestConsumer.Push(PullRespondData{
				TxID:   txid,
				PeerID: from,
			})
		}
	})

	err := ret.OnEvent(EventDroppedTx, func(dropData DropTxData) {
		ret.IncCounter("drop." + dropData.WhoDropped)
		ret.log.Infof("DROP %s by '%s'. Reason: '%s'", dropData.TxID.StringShort(), dropData.WhoDropped, dropData.Msg)
	})
	util.AssertNoError(err)

	return ret
}

func (w *Workflow) LogLevel() zapcore.Level {
	return w.log.Level()
}

func (w *Workflow) SetTraceMilestones(f bool) {
	w.traceMilestones.Store(f)
}

func (w *Workflow) Start(parentCtx ...context.Context) {
	w.startOnce.Do(func() {
		w.log.Infof("STARTING [loglevel=%s]..", w.log.Level())

		var ctx context.Context
		if len(parentCtx) > 0 {
			ctx, w.stopFun = context.WithCancel(parentCtx[0])
		} else {
			ctx, w.stopFun = context.WithCancel(context.Background())
		}
		w.startWG.Add(1)

		w.primaryInputConsumer.Start()
		w.preValidateConsumer.Start()
		w.solidifyConsumer.Start()
		w.pullConsumer.Start()
		w.validateConsumer.Start()
		w.appendTxConsumer.Start()
		w.eventsConsumer.Start()
		w.pullRequestConsumer.Start()
		w.txGossipOutConsumer.Start()

		w.startWG.Done()
		w.working.Store(true)

		go func() {
			<-ctx.Done()

			util.Assertf(w.working.Swap(false), "wasn't started yet")
			w.startWG.Wait()
			w.primaryInputConsumer.Stop()
			w.terminateWG.Wait()
			w.log.Info("all consumers STOPPED")
			_ = w.log.Sync()
		}()
	})
}

func (w *Workflow) StartPruner() {
	w.startPrunerOnce.Do(func() {
		w.startPruner()
	})
}

func (w *Workflow) Stop() {
	w.stopOnce.Do(func() {
		w.stopFun()
	})
}

func (w *Workflow) WaitStop() {
	w.terminateWG.Wait()
}

func (w *Workflow) IsRunning() bool {
	return w.working.Load()
}

const maxWaitingTimeSlots = 10_000

func (w *Workflow) maxDurationInTheFuture() time.Duration {
	return time.Duration(maxWaitingTimeSlots) * ledger.TransactionTimePaceDuration()
}

func (w *Workflow) AddCounter(name string, i int) {
	w.debugCounters.Add(name, i)
}

func (w *Workflow) IncCounter(name string) {
	w.debugCounters.Inc(name)
}

func (w *Workflow) QueueInfo() string {
	m := map[string]string{
		w.primaryInputConsumer.Name(): w.primaryInputConsumer.InfoStr(),
		w.preValidateConsumer.Name():  w.preValidateConsumer.InfoStr(),
		w.solidifyConsumer.Name():     w.solidifyConsumer.InfoStr(),
		w.validateConsumer.Name():     w.validateConsumer.InfoStr(),
		w.appendTxConsumer.Name():     w.appendTxConsumer.InfoStr(),
		w.eventsConsumer.Name():       w.eventsConsumer.InfoStr(),
	}
	var ret strings.Builder
	for n, i := range m {
		_, _ = fmt.Fprintf(&ret, "%s: %s\n", n, i)
	}
	return ret.String()
}

func (w *Workflow) CounterInfo() string {
	return w.debugCounters.String()
}

func (w *Workflow) CheckDebugCounters(expect map[string]int) error {
	return w.debugCounters.CheckValues(expect)
}

func (w *Workflow) PostEventDropTxID(txid *ledger.TransactionID, whoDropped string, reasonFormat string, args ...any) {
	w.PostEvent(EventDroppedTx, DropTxData{
		TxID:       txid,
		WhoDropped: whoDropped,
		Msg:        fmt.Sprintf(reasonFormat, args...),
	})
}

func (w *Workflow) StoreTxBytes(txBytes []byte) func() error {
	return func() error {
		return w.txBytesStore.SaveTxBytes(txBytes)
	}
}
