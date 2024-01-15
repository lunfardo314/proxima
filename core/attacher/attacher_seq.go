package attacher

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	sequencerAttacher struct {
		pastConeAttacher
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
	a := newSequencerAttacher(vid, env, ctx)
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
	a.PostEventNewGood(vid)
	a.stats.baseline = a.baselineBranch
	return vertex.Good, a.stats, nil
}

func newSequencerAttacher(vid *vertex.WrappedTx, env Environment, ctx context.Context) *sequencerAttacher {
	ret := &sequencerAttacher{
		pastConeAttacher: newPastConeAttacher(env, vid.IDShortString()),
		ctx:              ctx,
		vid:              vid,
		pokeChan:         make(chan *vertex.WrappedTx, 1),
		stats:            &attachStats{},
	}
	ret.pastConeAttacher.pokeMe = func(vid *vertex.WrappedTx) {
		ret.pokeMe(vid)
	}
	ret.vid.OnPoke(func(withVID *vertex.WrappedTx) {
		ret._doPoke(withVID)
	})
	return ret
}

func (a *sequencerAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
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

func (a *sequencerAttacher) close() {
	a.closeOnce.Do(func() {
		a.pokeMutex.Lock()
		a.closed = true
		close(a.pokeChan)
		a.vid.OnPoke(nil)
		a.pokeMutex.Unlock()
	})
}

func (a *sequencerAttacher) solidifyBaselineState() vertex.Status {
	return a.lazyRepeat(func() vertex.Status {
		var ok bool
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			ok = a.solidifyBaseline(v)
			if ok && v.FlagsUp(vertex.FlagBaselineSolid) {
				a.setBaselineBranch(v.BaselineBranch)
				success = true
			}
		}})
		switch {
		case !ok:
			return vertex.Bad
		case success:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

// solidifyPastCone solidifies and validates sequencer transaction in the context of known baseline state
func (a *sequencerAttacher) solidifyPastCone() vertex.Status {
	return a.lazyRepeat(func() (status vertex.Status) {
		ok := true
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.attachVertex(v, a.vid, ledger.NilLogicalTime, set.New[*vertex.WrappedTx]())
				if ok {
					success = v.FlagsUp(vertex.FlagsSequencerVertexCompleted)
				}
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case success:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

func (a *sequencerAttacher) _doPoke(msg *vertex.WrappedTx) {
	a.pokeMutex.Lock()
	defer a.pokeMutex.Unlock()

	if !a.closed {
		a.pokeChan <- msg
	}
}

func (a *sequencerAttacher) pokeMe(with *vertex.WrappedTx) {
	//a.trace1Ahead()
	a.tracef("pokeMe with %s", with.IDShortString())
	a.PokeMe(a.vid, with)
}
