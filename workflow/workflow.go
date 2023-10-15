package workflow

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/consumer"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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
	}

	Consumer[T any] struct {
		*consumer.Consumer[T]
		glb *Workflow
	}
)

const workflowLogName = "[workflow]"
const TxStatusDefaultWaitingTimeout = 2 * time.Second

func New(ut *utangle.UTXOTangle, configOptions ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range configOptions {
		opt(&cfg)
	}

	ret := &Workflow{
		configParams:  cfg,
		log:           general.NewLogger(workflowLogName, cfg.logLevel, cfg.logOutput, cfg.logTimeLayout),
		utxoTangle:    ut,
		debugCounters: testutil.NewSynCounters(),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.initPrimaryInputConsumer()
	ret.initPreValidateConsumer()
	ret.initSolidifyConsumer()
	ret.initValidateConsumer()
	ret.initAppendTxConsumer()
	ret.initRejectConsumer()
	ret.initEventsConsumer()

	return ret
}

func (w *Workflow) LogLevel() zapcore.Level {
	return w.log.Level()
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

func (w *Workflow) DumpPending() *lines.Lines {
	return w.solidifyConsumer.DumpPending()
}

func (w *Workflow) DumpUnresolvedDependencies() *lines.Lines {
	return w.solidifyConsumer.DumpUnresolvedDependencies()
}
