package attacher

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/util"
)

const (
	periodicCheckEach               = 1 * time.Second
	maxToleratedParasiticChainSlots = 5
)

const TraceTagAttachMilestone = "milestone"

func runMilestoneAttacher(vid *vertex.WrappedTx, metadata *txmetadata.TransactionMetadata, env Environment, ctx context.Context) (vertex.Status, *attachFinals, error) {
	a := newMilestoneAttacher(vid, env, metadata, ctx)
	defer func() {
		go a.close()
	}()

	a.Tracef(TraceTagAttachMilestone, ">>>>>>>>>>>>> START")
	defer a.Tracef(TraceTagAttachMilestone, "<<<<<<<<<<<<< EXIT")

	// first solidify baseline state
	status := a.solidifyBaseline()
	if status == vertex.Bad {
		a.Tracef(TraceTagAttachMilestone, "baseline solidification failed. Reason: %v", a.vid.GetError)
		return vertex.Bad, nil, a.err
	}

	util.Assertf(a.baseline != nil, "a.baseline != nil")

	// then continue with the rest
	a.Tracef(TraceTagAttachMilestone, "baseline is OK <- %s", a.baseline.IDShortString)

	status = a.solidifyPastCone()
	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "past cone solidification failed. Reason: %v", a.err)
		return vertex.Bad, nil, a.err
	}

	a.Tracef(TraceTagAttachMilestone, "past cone OK")

	a.markVertexDefined(a.vid)

	util.AssertNoError(a.checkConsistencyBeforeFinalization())

	a.finalize()
	a.vid.SetTxStatusGood()

	// persist transaction bytes, if needed
	if a.metadata == nil || a.metadata.SourceTypeNonPersistent != txmetadata.SourceTypeTxStore {
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			flags := vid.FlagsNoLock()
			if !flags.FlagsUp(vertex.FlagTxBytesPersisted) {
				c := a.coverage.LatestDelta()
				persistentMetadata := txmetadata.TransactionMetadata{
					StateRoot:           a.finals.root,
					LedgerCoverageDelta: &c,
				}
				a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), &persistentMetadata)
				vid.SetFlagsUpNoLock(vertex.FlagTxBytesPersisted)
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
				a.Tracef(TraceTagAttachMilestone, "poked with %s", withVID.IDShortString)
			}
		case <-time.After(periodicCheckEach):
			a.Tracef(TraceTagAttachMilestone, "periodic check")
		}
	}
}

func logFinalStatusString(vid *vertex.WrappedTx, finals *attachFinals, msData *txbuilder.MilestoneData) string {
	var msg string

	msDataStr := ""
	if msData != nil {
		msDataStr = fmt.Sprintf(" (%s %d/%d)", msData.Name, msData.BranchHeight, msData.ChainHeight)
	}
	if vid.IsBranchTransaction() {
		msg = fmt.Sprintf("-- ATTACH BRANCH%s %s(in %d/out %d)", msDataStr, vid.IDShortString(), finals.numInputs, finals.numOutputs)
	} else {
		nums := ""
		if finals != nil {
			nums = fmt.Sprintf("(in %d/out %d)", finals.numInputs, finals.numOutputs)
		}
		msg = fmt.Sprintf("-- ATTACH SEQ TX%s %s%s", msDataStr, vid.IDShortString(), nums)
	}
	if vid.GetTxStatus() == vertex.Bad {
		msg += fmt.Sprintf("BAD: err = '%v'", vid.GetError())
	} else {
		bl := "<nil>"
		if finals != nil && finals.baseline != nil {
			bl = finals.baseline.IDShortString()
		}
		if vid.IsBranchTransaction() {
			msg += fmt.Sprintf(" base: %s, new tx: %d, UTXO mut +%d/-%d, cov: %s",
				bl, finals.numTransactions, finals.numCreatedOutputs, finals.numDeletedOutputs, finals.coverage.String())
		} else {
			msg += fmt.Sprintf(" base: %s, new tx: %d, cov: %s", bl, finals.numTransactions, finals.coverage.String())
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

func (a *milestoneAttacher) solidifyBaseline() vertex.Status {
	return a.lazyRepeat(func() vertex.Status {
		var ok bool
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			ok = a.solidifyBaselineVertex(v)
			if ok && v.BaselineBranch != nil {
				a.setBaseline(v.BaselineBranch, a.vid.Timestamp())
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
				ok = a.attachVertexUnwrapped(v, a.vid, ledger.NilLogicalTime)
				if ok {
					success = a.flags(a.vid).FlagsUp(FlagDefined) && a.err == nil

					// correct assertion but not necessary because it is checked before finalization
					//if success {
					//	util.AssertNoError(a.allEndorsementsDefined(v))
					//	util.AssertNoError(a.allInputsDefined(v))
					//}
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
	a.Tracef(TraceTagAttachMilestone, "pokeMe with %s", with.IDShortString())
	a.PokeMe(a.vid, with)
}
