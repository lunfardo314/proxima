package workflow

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/consumer"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

type (
	Workflow struct {
		startOnce       sync.Once
		stopOnce        sync.Once
		working         atomic.Bool
		startPrunerOnce sync.Once
		log             *zap.SugaredLogger
		configParams    ConfigParams
		utxoTangle      *utangle.UTXOTangle
		debugCounters   *testutil.SyncCounters

		primaryInputConsumer *PrimaryConsumer
		preValidateConsumer  *PreValidateConsumer
		solidifyConsumer     *SolidifyConsumer
		validateConsumer     *ValidateConsumer
		appendTxConsumer     *AppendTxConsumer
		rejectConsumer       *RejectConsumer
		eventsConsumer       *EventsConsumer

		handlersMutex sync.RWMutex
		eventHandlers map[eventtype.EventCode][]func(any)

		terminateWG sync.WaitGroup
		startWG     sync.WaitGroup

		// if flag is set true, PrepareVertex creates log for each transaction
		logTransaction  atomic.Bool
		traceMilestones atomic.Bool

		// default transaction waiting list
		defaultTxStatusWaitingList *TxStatusWaitingList
	}

	Consumer[T any] struct {
		*consumer.Consumer[T]
		glb *Workflow
	}
)

var AllConsumerNames = []string{
	AppendTxConsumerName,
	EventsName,
	PrimaryInputConsumerName,
	PreValidateConsumerName,
	RejectConsumerName,
	SolidifyConsumerName,
	ValidateConsumerName,
}

func NewConsumer[T any](name string, wrk *Workflow) *Consumer[T] {
	lvl := wrk.configParams.logLevel
	if l, ok := wrk.configParams.consumerLogLevel[name]; ok {
		lvl = l
	}
	ret := &Consumer[T]{
		Consumer: consumer.NewConsumer[T](name, lvl),
		glb:      wrk,
	}
	ret.AddOnConsume(func(_ T) {
		wrk.IncCounter(name + ".in")
	})
	return ret
}

func (c *Consumer[T]) Start() {
	c.Consumer.Start(&c.glb.terminateWG)
}

func (c *Consumer[T]) TxLogPrefix() string {
	return c.Name() + ": "
}

func (c *Consumer[T]) Debugf(inp *PrimaryInputConsumerData, format string, args ...any) {
	c.Log().Debugf(format+"   "+inp.Tx.IDShort(), args...)
	inp.txLog.Logf(c.TxLogPrefix()+format, args...)
}

func (c *Consumer[T]) Warnf(inp *PrimaryInputConsumerData, format string, args ...any) {
	c.Log().Warnf(format+"    "+inp.Tx.IDShort(), args...)
	inp.txLog.Logf(c.TxLogPrefix()+format, args...)
}

func (c *Consumer[T]) Infof(inp *PrimaryInputConsumerData, format string, args ...any) {
	c.Log().Infof(format+"    "+inp.Tx.IDShort(), args...)
	inp.txLog.Logf(c.TxLogPrefix()+format, args...)
}

func (c *Consumer[T]) TraceMilestones(tx *transaction.Transaction, txid *core.TransactionID, msg string) {
	if !c.glb.traceMilestones.Load() {
		return
	}
	if tx.IsSequencerMilestone() {
		c.Log().Infof("%s  %s -- %s", msg, tx.SequencerInfoString(), txid.Short())
	}

}

func (c *Consumer[T]) LogfVertex(v *utangle.Vertex, format string, args ...any) {
	v.Logf(c.TxLogPrefix()+format, args...)
}

func (c *Consumer[T]) RejectTransaction(inp *PrimaryInputConsumerData, format string, args ...any) {
	c.Debugf(inp, format, args...)
	c.glb.RejectTransaction(*inp.Tx.ID(), format, args...)
}

func (c *Consumer[T]) IncCounter(name string) {
	c.glb.IncCounter(c.Name() + "." + name)
}

func (c *Consumer[T]) InfoStr() string {
	p, l := c.Info()
	return fmt.Sprintf("pushCount: %d, queueLen: %d", p, l)
}

const TxStatusDefaultWaitingTimeout = 2 * time.Second

func New(ut *utangle.UTXOTangle, configOptions ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range configOptions {
		opt(&cfg)
	}

	ret := &Workflow{
		configParams:  cfg,
		log:           general.NewLogger("[pipe]", cfg.logLevel, cfg.logOutput, cfg.logTimeLayout),
		utxoTangle:    ut,
		debugCounters: testutil.NewSynCounters(),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.defaultTxStatusWaitingList = ret.StartTxStatusWaitingList(TxStatusDefaultWaitingTimeout)
	ret.initPrimaryInputConsumer()
	ret.initPreValidateConsumer()
	ret.initSolidifyConsumer()
	ret.initValidateConsumer()
	ret.initAppendTxConsumer()
	ret.initRejectConsumer()
	ret.initEventsConsumer()

	return ret
}

func (w *Workflow) SetLogTransactions(f bool) {
	w.logTransaction.Store(f)
}

func (w *Workflow) SetTraceMilestones(f bool) {
	w.traceMilestones.Store(f)
}

func (w *Workflow) Start() {
	w.startOnce.Do(func() {
		w.log.Infof("STARTING [loglevel=%s]..", w.log.Level())

		w.startWG.Add(1)
		w.primaryInputConsumer.Start()
		w.preValidateConsumer.Start()
		w.solidifyConsumer.Start()
		w.validateConsumer.Start()
		w.appendTxConsumer.Start()
		w.rejectConsumer.Start()
		w.eventsConsumer.Start()
		w.startWG.Done()
		w.working.Store(true)
	})
}

func (w *Workflow) StartPruner() {
	w.startPrunerOnce.Do(func() {
		w.startPruner()
	})
}

func (w *Workflow) Stop() {
	w.stopOnce.Do(func() {
		util.Assertf(w.working.Swap(false), "wasn't started yet")
		w.defaultTxStatusWaitingList.Stop()
		w.startWG.Wait()
		w.primaryInputConsumer.Stop()
		w.terminateWG.Wait()
		w.log.Info("all consumers STOPPED")
		_ = w.log.Sync()
	})
}

func (w *Workflow) IsRunning() bool {
	return w.working.Load()
}

const maxWaitingTimeSlots = 10_000

func (w *Workflow) maxDurationInTheFuture() time.Duration {
	return time.Duration(maxWaitingTimeSlots) * core.TransactionTimePaceDuration()
}

func (w *Workflow) DumpTransactionLog() string {
	return w.utxoTangle.TransactionLogAsString()
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
		w.rejectConsumer.Name():       w.rejectConsumer.InfoStr(),
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

func (w *Workflow) DumpPending() string {
	return w.solidifyConsumer.DumpPendingDependencies()
}

func (w *Workflow) DumpPendingTxLogs() string {
	return w.solidifyConsumer.DumpPendingTxLogs()
}
