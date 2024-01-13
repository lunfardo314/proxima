package attacher

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
)

type (
	DAGAccessEnvironment interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx
		GetVertex(txid *ledger.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx)
		EvidenceIncomingBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
		EvidenceBookedBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
	}

	PullEnvironment interface {
		Pull(txid ledger.TransactionID)
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
	}
	PostEventEnvironment interface {
		PostEventNewGood(vid *vertex.WrappedTx)
		PostEventNewValidated(vid *vertex.WrappedTx)
	}

	Environment interface {
		DAGAccessEnvironment
		PullEnvironment
		PostEventEnvironment
		Log() *zap.SugaredLogger
	}

	inputAttacher struct {
		env                   Environment
		name                  string
		reason                error
		baselineBranch        *vertex.WrappedTx
		validPastVertices     set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rooted                map[*vertex.WrappedTx]set.Set[byte]
		pokeMe                func(vid *vertex.WrappedTx)
		forceTrace1Ahead      bool
	}

	attacher struct {
		inputAttacher
		vid       *vertex.WrappedTx
		ctx       context.Context
		closeOnce sync.Once
		pokeChan  chan *vertex.WrappedTx
		pokeMutex sync.Mutex
		stats     *attachStats
		closed    bool
	}
	_attacherOptions struct {
		ctx                context.Context
		attachmentCallback func(vid *vertex.WrappedTx)
		pullNonBranch      bool
		doNotLoadBranch    bool
		calledBy           string
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

func runAttacher(vid *vertex.WrappedTx, env Environment, ctx context.Context) (vertex.Status, *attachStats, error) {
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
	a.env.PostEventNewGood(vid)
	a.stats.baseline = a.baselineBranch
	return vertex.Good, a.stats, nil
}

// AttachTransactionFromBytes used for testing
func AttachTransactionFromBytes(txBytes []byte, env Environment, opts ...Option) (*vertex.WrappedTx, error) {
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	return AttachTransaction(tx, env, opts...), nil
}

const maxTimeout = 10 * time.Minute

func EnsureBranch(txid ledger.TransactionID, env Environment, timeout ...time.Duration) (*vertex.WrappedTx, error) {
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

func EnsureLatestBranches(env Environment) error {
	branchTxIDs := multistate.FetchLatestBranchTransactionIDs(env.StateStore())
	for _, branchID := range branchTxIDs {
		if _, err := EnsureBranch(branchID, env); err != nil {
			return err
		}
	}
	return nil
}

func newAttacher(vid *vertex.WrappedTx, env Environment, ctx context.Context) *attacher {
	ret := &attacher{
		inputAttacher: inputAttacher{
			name:                  vid.IDShortString(),
			env:                   env,
			rooted:                make(map[*vertex.WrappedTx]set.Set[byte]),
			validPastVertices:     set.New[*vertex.WrappedTx](),
			undefinedPastVertices: set.New[*vertex.WrappedTx](),
		},
		ctx:      ctx,
		vid:      vid,
		pokeChan: make(chan *vertex.WrappedTx, 1),
		stats:    &attachStats{},
	}
	ret.inputAttacher.pokeMe = func(vid *vertex.WrappedTx) {
		ret.pokeMe(vid)
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
				//a.trace1Ahead()
				a.tracef("poked with %s", withVID.IDShortString)
			}
		case <-time.After(periodicCheckEach):
			//a.trace1Ahead()
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
	//a.trace1Ahead()
	a.tracef("pokeMe with %s", with.IDShortString())
	a.env.PokeMe(a.vid, with)
}
