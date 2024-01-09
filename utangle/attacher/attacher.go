package attacher

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
)

type (
	DAGAccess interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx)
		EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID)
		EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID)
	}

	PullEnvironment interface {
		Pull(txid core.TransactionID)
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
	}

	AttachEnvironment interface {
		DAGAccess
		PullEnvironment
		Log() *zap.SugaredLogger
	}

	attacher struct {
		env                   AttachEnvironment
		vid                   *vertex.WrappedTx
		reason                error
		baselineBranch        *vertex.WrappedTx
		validPastVertices     set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rooted                map[*vertex.WrappedTx]set.Set[byte]
		ctx                   context.Context
		closeOnce             sync.Once
		pokeChan              chan *vertex.WrappedTx
		pokeMutex             sync.Mutex
		stats                 *attachStats
		closed                bool
		flags                 uint8
		forceTrace1Ahead      bool
	}
	_attacherOptions struct {
		ctx                  context.Context
		finalizationCallback func(vid *vertex.WrappedTx)
		pullNonBranch        bool
		doNotLoadBranch      bool
		calledBy             string
	}
	Option func(*_attacherOptions)

	attachStats struct {
		coverage          multistate.LedgerCoverage
		numTransactions   int
		numCreatedOutputs int
		numDeletedOutputs int
		baseline          *vertex.WrappedTx
	}
)

const (
	periodicCheckEach               = 1 * time.Second
	maxToleratedParasiticChainSlots = 1
)

func OptionWithContext(ctx context.Context) Option {
	return func(options *_attacherOptions) {
		options.ctx = ctx
	}
}

func OptionWithFinalizationCallback(fun func(vid *vertex.WrappedTx)) Option {
	return func(options *_attacherOptions) {
		options.finalizationCallback = fun
	}
}

func OptionPullNonBranch(options *_attacherOptions) {
	options.pullNonBranch = true
}

func OptionDoNotLoadBranch(options *_attacherOptions) {
	options.doNotLoadBranch = true
}

func OptionInvokedBy(name string) Option {
	return func(options *_attacherOptions) {
		options.calledBy = name
	}
}

// AttachTxID ensures the txid is on the utangle_old. Must be called from globally locked environment
func AttachTxID(txid core.TransactionID, env AttachEnvironment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	by := ""
	if options.calledBy != "" {
		by = " by " + options.calledBy
	}
	tracef(env, "AttachTxID: %s%s", txid.StringShort(), by)
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			tracef(env, "AttachTxID: found existing %s%s", txid.StringShort(), by)
			return
		}
		// it is new
		if !txid.IsBranchTransaction() {
			// if not branch -> just place the empty virtualTx on the utangle_old, no further action
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			if options.pullNonBranch {
				env.Pull(txid)
			}
			return
		}
		// it is a branch transaction
		if options.doNotLoadBranch {
			// only needed ID (for call from the AttachTransaction)
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			return
		}
		// look up for the corresponding state
		if bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid); branchAvailable {
			// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
			vid = vertex.NewVirtualBranchTx(&bd).WrapWithID(txid)
			vid.SetTxStatus(vertex.Good)
			vid.SetLedgerCoverage(bd.LedgerCoverage)
			env.AddVertexNoLock(vid)
			env.AddBranchNoLock(vid) // <<<< will be reading branch data twice. Not big problem
			tracef(env, "AttachTxID: branch fetched from the state: %s%s", txid.StringShort(), by)
		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle_old -> pull it
			// the puller will trigger further solidification
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid) // always pull new branch. This will spin off sync process on the node
			tracef(env, "AttachTxID: added new branch vertex and pulled %s%s", txid.StringShort(), by)
		}
	})
	return
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts attacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}
	tracef(env, "AttachTransaction: %s", tx.IDShortString)

	if tx.IsBranchTransaction() {
		env.EvidenceIncomingBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
	vid = AttachTxID(*tx.ID(), env, OptionDoNotLoadBranch, OptionInvokedBy("addTx"))
	vid.Unwrap(vertex.UnwrapOptions{
		// full vertex will be ignored, virtual tx will be converted into full vertex and attacher started, if necessary
		VirtualTx: func(v *vertex.VirtualTransaction) {
			vid.ConvertVirtualTxToVertexNoLock(vertex.New(tx))
			env.PokeAllWith(vid)
			if !vid.IsSequencerMilestone() {
				//env.PokeAllWith(vid)
				return
			}
			// starts attacher goroutine for each sequencer transaction
			ctx := options.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			callback := options.finalizationCallback
			if callback == nil {
				callback = func(_ *vertex.WrappedTx) {}
			}

			runFun := func() {
				status, stats, err := runAttacher(vid, env, ctx)
				vid.SetTxStatus(status)
				vid.SetReason(err)
				env.Log().Info(logFinalStatusString(vid, stats))
				env.PokeAllWith(vid)
				callback(vid)
			}

			const forDebugging = true
			if forDebugging {
				go runFun()
			} else {
				util.RunWrappedRoutine(vid.IDShortString(), runFun, nil, common.ErrDBUnavailable)
			}
		},
	})
	return
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) (vertex.Status, *attachStats, error) {
	a := newAttacher(vid, env, ctx)
	defer func() {
		go a.close()
	}()

	a.tracef(">>>>>>>>>>>>> START")
	defer a.tracef("<<<<<<<<<<<<< EXIT")

	// first solidify baseline state
	status := a.solidifyBaselineState()
	if status == vertex.Bad {
		a.tracef("baseline solidification failed. Reason: %v", a.vid.GetReason())
		return vertex.Bad, nil, a.reason
	}

	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")

	// then continue with the rest
	a.tracef("baseline is OK <- %s", a.baselineBranch.IDShortString())

	status = a.solidifyPastCone()
	if status != vertex.Good {
		a.tracef("past cone solidification failed. Reason: %v", a.vid.GetReason())
		return vertex.Bad, nil, a.reason
	}

	a.tracef("past cone OK")

	util.AssertNoError(a.checkPastConeVerticesConsistent())

	a.finalize()
	a.vid.SetTxStatus(vertex.Good)
	a.stats.baseline = a.baselineBranch
	return vertex.Good, a.stats, nil
}

// AttachTransactionFromBytes used for testing
func AttachTransactionFromBytes(txBytes []byte, env AttachEnvironment, opts ...Option) (*vertex.WrappedTx, error) {
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	return AttachTransaction(tx, env, opts...), nil
}

const maxTimeout = 10 * time.Minute

func EnsureBranch(txid core.TransactionID, env AttachEnvironment, timeout ...time.Duration) (*vertex.WrappedTx, error) {
	vid := AttachTxID(txid, env)
	to := maxTimeout
	if len(timeout) > 0 {
		to = timeout[0]
	}
	deadline := time.Now().Add(to)
	for vid.GetTxStatus() == vertex.Undefined {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("failed to fetch branch %s in %v", txid.StringShort(), to)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return vid, nil
}

func EnsureLatestBranches(env AttachEnvironment) error {
	branchTxIDs := multistate.FetchLatestBranchTransactionIDs(env.StateStore())
	for _, branchID := range branchTxIDs {
		if _, err := EnsureBranch(branchID, env); err != nil {
			return err
		}
	}
	return nil
}

func newAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:                   ctx,
		vid:                   vid,
		env:                   env,
		pokeChan:              make(chan *vertex.WrappedTx, 1),
		rooted:                make(map[*vertex.WrappedTx]set.Set[byte]),
		validPastVertices:     set.New[*vertex.WrappedTx](),
		undefinedPastVertices: set.New[*vertex.WrappedTx](),
		stats:                 &attachStats{},
	}
	ret.vid.OnPoke(func(withVID *vertex.WrappedTx) {
		ret._doPoke(withVID)
	})
	return ret
}

func (a *attacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.ctx.Done():
			return vertex.Undefined
		case withVID := <-a.pokeChan:
			if withVID != nil {
				a.trace1Ahead()
				a.tracef("poked with %s", withVID.IDShortString)
			}
		case <-time.After(periodicCheckEach):
			a.trace1Ahead()
			a.tracef("periodic check")
		}
	}
}

func logFinalStatusString(vid *vertex.WrappedTx, stats *attachStats) string {
	var msg string

	status := vid.GetTxStatus()
	if vid.IsBranchTransaction() {
		msg = fmt.Sprintf("ATTACH BRANCH (%s) %s", status.String(), vid.IDShortString())
	} else {
		msg = fmt.Sprintf("ATTACH SEQ TX (%s) %s", status.String(), vid.IDShortString())
	}
	if status == vertex.Bad {
		msg += fmt.Sprintf(" reason = '%v'", vid.GetReason())
	} else {
		bl := "<nil>"
		if stats.baseline != nil {
			bl = stats.baseline.IDShortString()
		}
		if vid.IsBranchTransaction() {
			msg += fmt.Sprintf("baseline: %s, tx: %d, UTXO +%d/-%d, cov: %s",
				bl, stats.numTransactions, stats.numCreatedOutputs, stats.numDeletedOutputs, stats.coverage.String())
		} else {
			msg += fmt.Sprintf("baseline: %s, tx: %d, cov: %s", bl, stats.numTransactions, stats.coverage.String())
		}
	}
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memStr := fmt.Sprintf(", alloc: %.1f MB, GC: %d, Gort: %d, ",
		float32(memStats.Alloc*10/(1024*1024))/10,
		memStats.NumGC,
		runtime.NumGoroutine(),
	)

	return msg + memStr
}

func (a *attacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.env.GetStateReaderForTheBranch(a.baselineBranch))
}

func (a *attacher) setReason(err error) {
	a.tracef("set reason: '%v'", err)
	a.reason = err
}

func (a *attacher) pastConeVertexVisited(vid *vertex.WrappedTx, good bool) {
	if good {
		a.tracef("pastConeVertexVisited: %s is GOOD", vid.IDShortString)
		delete(a.undefinedPastVertices, vid)
		a.validPastVertices.Insert(vid)
	} else {
		util.Assertf(!a.validPastVertices.Contains(vid), "!a.validPastVertices.Contains(vid)")
		a.undefinedPastVertices.Insert(vid)
		a.tracef("pastConeVertexVisited: %s is UNDEF", vid.IDShortString)
	}
}

func (a *attacher) isKnownVertex(vid *vertex.WrappedTx) bool {
	if a.validPastVertices.Contains(vid) {
		util.Assertf(!a.undefinedPastVertices.Contains(vid), "!a.undefinedPastVertices.Contains(vid)")
		return true
	}
	if a.undefinedPastVertices.Contains(vid) {
		util.Assertf(!a.validPastVertices.Contains(vid), "!a.validPastVertices.Contains(vid)")
		return true
	}
	return false
}

func (a *attacher) close() {
	a.closeOnce.Do(func() {
		a.pokeMutex.Lock()
		a.closed = true
		close(a.pokeChan)
		a.vid.OnPoke(nil)
		a.pokeMutex.Unlock()
	})
}

func (a *attacher) _doPoke(msg *vertex.WrappedTx) {
	a.pokeMutex.Lock()
	defer a.pokeMutex.Unlock()

	if !a.closed {
		a.pokeChan <- msg
	}
}

func (a *attacher) pokeMe(with *vertex.WrappedTx) {
	a.trace1Ahead()
	a.tracef("pokeMe with %s", with.IDShortString())
	a.env.PokeMe(a.vid, with)
}

// not thread safe
var trace = false

func SetTraceOn() {
	trace = true
}

func (a *attacher) trace1Ahead() {
	a.forceTrace1Ahead = true
}

func (a *attacher) tracef(format string, lazyArgs ...any) {
	if trace || a.forceTrace1Ahead {
		format1 := "TRACE [attacher] " + a.vid.IDShortString() + ": " + format
		a.env.Log().Infof(format1, util.EvalLazyArgs(lazyArgs...)...)
		a.forceTrace1Ahead = false
	}
}

func tracef(env AttachEnvironment, format string, lazyArgs ...any) {
	if trace {
		env.Log().Infof("TRACE "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}
