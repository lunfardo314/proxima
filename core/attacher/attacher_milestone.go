package attacher

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

const (
	periodicCheckEach               = 1 * time.Second
	maxToleratedParasiticChainSlots = 1
)

func runMilestoneAttacher(vid *vertex.WrappedTx, metadata *txmetadata.TransactionMetadata, env Environment, ctx context.Context) (vertex.Status, *attachFinals, error) {
	a := newMilestoneAttacher(vid, env, metadata, ctx)
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
		a.tracef("past cone solidification failed. Reason: %v", a.reason)
		return vertex.Bad, nil, a.reason
	}

	a.tracef("past cone OK")

	util.AssertNoError(a.checkPastConeVerticesConsistent())

	a.finalize()
	a.vid.SetTxStatus(vertex.Good)

	// persist transaction bytes, if needed
	if a.metadata == nil || a.metadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			if !v.FlagsUp(vertex.FlagTxBytesPersisted) {
				c := a.coverageDelta
				persistentMetadata := txmetadata.TransactionMetadata{
					StateRoot:           a.finals.root,
					LedgerCoverageDelta: &c,
				}
				a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), &persistentMetadata)
				v.SetFlagUp(vertex.FlagTxBytesPersisted)
			}
		}})
	}

	a.PostEventNewGood(vid)
	return vertex.Good, a.finals, nil
}

func newMilestoneAttacher(vid *vertex.WrappedTx, env Environment, metadata *txmetadata.TransactionMetadata, ctx context.Context) *milestoneAttacher {
	ret := &milestoneAttacher{
		attacher: newPastConeAttacher(env, vid.IDShortString()),
		ctx:      ctx,
		vid:      vid,
		metadata: metadata,
		pokeChan: make(chan *vertex.WrappedTx, 1),
		finals:   &attachFinals{},
	}
	ret.attacher.pokeMe = func(vid *vertex.WrappedTx) {
		ret.pokeMe(vid)
	}
	ret.vid.OnPoke(func(withVID *vertex.WrappedTx) {
		ret._doPoke(withVID)
	})
	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret.finals.numInputs = v.Tx.NumInputs()
			ret.finals.numOutputs = v.Tx.NumProducedOutputs()
		},
		VirtualTx: func(_ *vertex.VirtualTransaction) {
			env.Log().Fatalf("unexpected virtual Tx: %s", vid.IDShortString())
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return ret
}

func (a *milestoneAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
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

func logFinalStatusString(vid *vertex.WrappedTx, finals *attachFinals) string {
	var msg string

	status := vid.GetTxStatus()
	if vid.IsBranchTransaction() {
		msg = fmt.Sprintf("-- ATTACH BRANCH (%s) %s(%d/%d)", status.String(), vid.IDShortString(), finals.numInputs, finals.numOutputs)
	} else {
		nums := ""
		if finals != nil {
			nums = fmt.Sprintf("(%d/%d)", finals.numInputs, finals.numOutputs)
		}
		msg = fmt.Sprintf("-- ATTACH SEQ TX (%s) %s%s", status.String(), vid.IDShortString(), nums)
	}
	if status == vertex.Bad {
		msg += fmt.Sprintf(" reason = '%v'", vid.GetReason())
	} else {
		bl := "<nil>"
		if finals.baseline != nil {
			bl = finals.baseline.IDShortString()
		}
		if vid.IsBranchTransaction() {
			msg += fmt.Sprintf(" baseline: %s, new tx: %d, UTXO mut +%d/-%d, cov: %s",
				bl, finals.numTransactions, finals.numCreatedOutputs, finals.numDeletedOutputs, finals.coverage.String())
		} else {
			msg += fmt.Sprintf(" baseline: %s, new tx: %d, cov: %s", bl, finals.numTransactions, finals.coverage.String())
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

func (a *milestoneAttacher) close() {
	a.closeOnce.Do(func() {
		a.pokeMutex.Lock()
		a.closed = true
		close(a.pokeChan)
		a.vid.OnPoke(nil)
		a.pokeMutex.Unlock()
	})
}

func (a *milestoneAttacher) solidifyBaselineState() vertex.Status {
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
func (a *milestoneAttacher) solidifyPastCone() vertex.Status {
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

func (a *milestoneAttacher) _doPoke(msg *vertex.WrappedTx) {
	a.pokeMutex.Lock()
	defer a.pokeMutex.Unlock()

	if !a.closed {
		a.pokeChan <- msg
	}
}

func (a *milestoneAttacher) pokeMe(with *vertex.WrappedTx) {
	//a.trace1Ahead()
	a.tracef("pokeMe with %s", with.IDShortString())
	a.PokeMe(a.vid, with)
}
