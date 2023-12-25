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
	"go.uber.org/zap"
)

type (
	DAGAccess interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranch(branch *vertex.WrappedTx)
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
		baselineBranch        *vertex.WrappedTx
		goodPastVertices      set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rooted                map[*vertex.WrappedTx]set.Set[byte]
		pendingOutputs        map[vertex.WrappedOutput]core.LogicalTime
		closeMutex            sync.RWMutex
		inChan                chan *vertex.WrappedTx
		ctx                   context.Context
		closed                bool
		endorsementsOk        bool
	}
	_attacherOptions struct {
		ctx                  context.Context
		finalizationCallback func(vid *vertex.WrappedTx)
	}
	Option func(*_attacherOptions)
)

const (
	periodicCheckEach               = 500 * time.Millisecond
	maxToleratedParasiticChainTicks = core.TimeTicksPerSlot
)

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
		} else {
			if !vid.IsVirtualTx() {
				return
			}
			// it is existing. Must virtualTx -> replace virtual tx with the full transaction
			vid.ConvertVirtualTxToVertex(vertex.New(tx))
		}
		env.AddVertexNoLock(vid)
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
			go func() {
				status, err := runAttacher(vid, env, ctx)
				vid.SetTxStatus(status)
				vid.SetReason(err)
				callback(vid)
			}()
		}
	})
	return
}

// AttachTransactionFromBytes used for testing
func AttachTransactionFromBytes(txBytes []byte, env AttachEnvironment, onFinalize ...func(vid *vertex.WrappedTx)) (*vertex.WrappedTx, error) {
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	callback := func(_ *vertex.WrappedTx) {}
	if len(onFinalize) > 0 {
		callback = onFinalize[0]
	}
	return AttachTransaction(tx, env, WithFinalizationCallback(callback)), nil
}

// AttachTxID ensures the txid is on the utangle_old. Must be called from globally locked environment
func AttachTxID(txid core.TransactionID, env AttachEnvironment, pullNonBranchIfNeeded bool) (vid *vertex.WrappedTx) {
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
			vid = vertex.NewVirtualBranchTx(&bd).Wrap()
			env.AddVertexNoLock(vid)
			//env.AddBranchNoLock(vid, &bd)
			vid.SetTxStatus(vertex.Good)
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

func newAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:              ctx,
		vid:              vid,
		env:              env,
		inChan:           make(chan *vertex.WrappedTx, 1),
		rooted:           make(map[*vertex.WrappedTx]set.Set[byte]),
		goodPastVertices: set.New[*vertex.WrappedTx](),
		pendingOutputs:   make(map[vertex.WrappedOutput]core.LogicalTime),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) (vertex.Status, error) {
	a := newAttacher(vid, env, ctx)
	defer a.close()

	// first solidify baseline state
	status := a.solidifyBaselineState()
	if status != vertex.Good {
		return vertex.Bad, fmt.Errorf("baseline state solidification failed")
	}

	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")

	// then continue with the rest
	status = a.solidifyPastCone()
	if status != vertex.Good {
		return vertex.Bad, fmt.Errorf("past cone solidification failed")
	}

	a.finalize()
	return vertex.Good, nil
}

func (a *attacher) lazyRepeat(fun func() vertex.Status) (status vertex.Status) {
	for {
		if fun() != vertex.Undefined {
			return
		}
		select {
		case <-a.ctx.Done():
			return
		case <-a.inChan:
		case <-time.After(periodicCheckEach):
		}
	}
}

func (a *attacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.env.GetStateReaderForTheBranch(a.baselineBranch))
}
