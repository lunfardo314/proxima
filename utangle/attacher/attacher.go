package attacher

import (
	"context"
	"fmt"
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
		OnChangeNotify(onChange, notify *vertex.WrappedTx)
		Notify(changed *vertex.WrappedTx)
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
		closeMutex            sync.RWMutex
		inChan                chan *vertex.WrappedTx
		ctx                   context.Context
		flags                 uint8
		closed                bool
	}
	_attacherOptions struct {
		ctx                  context.Context
		finalizationCallback func(vid *vertex.WrappedTx)
	}
	Option func(*_attacherOptions)
)

const (
	periodicCheckEach               = 1 * time.Second
	maxToleratedParasiticChainSlots = 1
)

// AttachTxID ensures the txid is on the utangle_old. Must be called from globally locked environment
func AttachTxID(txid core.TransactionID, env AttachEnvironment, pullNonBranchIfNeeded bool) (vid *vertex.WrappedTx) {
	tracef(env, "AttachTxID: %s", txid.StringShort)
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			return
		}
		// it is new
		if !txid.IsBranchTransaction() {
			// if not branch -> just place the empty virtualTx on the utangle_old, no further action
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			if pullNonBranchIfNeeded {
				env.Pull(txid)
			}
			return
		}
		// it is a branch transaction. Look up for the corresponding state
		if bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid); branchAvailable {
			// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
			vid = vertex.NewVirtualBranchTx(&bd).WrapWithID(txid)
			env.AddVertexNoLock(vid)
			vid.SetTxStatus(vertex.Good)
			vid.SetLedgerCoverage(bd.LedgerCoverage)
			env.AddBranchNoLock(vid) // <<<< will be reading branch data twice. Not big problem
			tracef(env, "branch fetched from the state: %s", txid.StringShort)
		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle_old -> pull it
			// the puller will trigger further solidification
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid) // always pull new branch. This will spin off sync process on the node
		}
	})
	return
}

func WithContext(ctx context.Context) Option {
	return func(options *_attacherOptions) {
		options.ctx = ctx
	}
}

func WithFinalizationCallback(fun func(vid *vertex.WrappedTx)) Option {
	return func(options *_attacherOptions) {
		options.finalizationCallback = fun
	}
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts attacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, opts ...Option) (vid *vertex.WrappedTx) {
	tracef(env, "AttachTransaction: %s", tx.IDShortString)

	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if tx.IsBranchTransaction() {
		env.EvidenceIncomingBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
	env.WithGlobalWriteLock(func() {
		// look up for the txid
		vid = env.GetVertexNoLock(tx.ID())
		if vid == nil {
			// it is new. Create a new wrapped tx and put it to the utangle_old
			vid = vertex.New(tx).Wrap()
			env.AddVertexNoLock(vid)
		} else {
			if !vid.IsVirtualTx() {
				return
			}
			// it is existing. Must virtualTx -> replace virtual tx with the full transaction
			vid.ConvertVirtualTxToVertex(vertex.New(tx))
		}
		if vid.IsSequencerMilestone() {
			// starts attacher goroutine for each sequencer transaction
			ctx := options.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			callback := options.finalizationCallback
			if callback == nil {
				callback = func(_ *vertex.WrappedTx) {}
			}
			const forTesting = false
			if forTesting {
				go func() {
					status, err := runAttacher(vid, env, ctx)
					vid.SetTxStatus(status)
					vid.SetReason(err)
					callback(vid)
				}()
			} else {
				util.RunWrappedRoutine(vid.IDShortString(), func() {
					status, err := runAttacher(vid, env, ctx)
					vid.SetTxStatus(status)
					vid.SetReason(err)
					callback(vid)
				}, nil, common.ErrDBUnavailable)
			}
		}
	})
	return
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
	vid := AttachTxID(txid, env, false)
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
		inChan:                make(chan *vertex.WrappedTx, 1),
		rooted:                make(map[*vertex.WrappedTx]set.Set[byte]),
		validPastVertices:     set.New[*vertex.WrappedTx](),
		undefinedPastVertices: set.New[*vertex.WrappedTx](),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) (vertex.Status, error) {
	a := newAttacher(vid, env, ctx)
	defer a.close()

	a.tracef(">>>>>>>>>>>>> START")
	defer a.tracef("<<<<<<<<<<<<< EXIT")

	// first solidify baseline state
	status := a.solidifyBaselineState()
	if status == vertex.Bad {
		a.tracef("baseline solidification failed. Reason: %v", a.vid.GetReason())
		return vertex.Bad, a.reason
	}

	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")

	// then continue with the rest
	a.tracef("baseline is OK -> %s", a.baselineBranch.IDShortString())

	status = a.solidifyPastCone()
	if status != vertex.Good {
		a.tracef("past cone solidification failed. Reason: %v", a.vid.GetReason())
		return vertex.Bad, a.reason
	}

	a.tracef("past cone OK")

	util.AssertNoError(a.checkPastConeVerticesConsistent())

	a.finalize()
	a.vid.SetTxStatus(vertex.Good)
	a.pastConeVertexVisited(a.vid, true)
	return vertex.Good, nil
}

func (a *attacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.ctx.Done():
			return vertex.Undefined
		case <-a.inChan:
		case <-time.After(periodicCheckEach):
			a.tracef("periodic check")
		}
	}
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

// not thread safe
var trace = false

func SetTraceOn() {
	trace = true
}

func (a *attacher) tracef(format string, lazyArgs ...any) {
	if trace {
		format1 := "TRACE [attacher] " + a.vid.IDShortString() + ": " + format
		a.env.Log().Infof(format1, util.EvalLazyArgs(lazyArgs...)...)
	}
}

func tracef(env AttachEnvironment, format string, lazyArgs ...any) {
	if trace {
		env.Log().Infof("TRACE "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}
