package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

const (
	periodicCheckEach = 1 * time.Second
)

const TraceTagAttachMilestone = "milestone"

func runMilestoneAttacher(vid *vertex.WrappedTx, metadata *txmetadata.TransactionMetadata, env Environment) (vertex.Status, *attachFinals, error) {
	a := newMilestoneAttacher(vid, env, metadata)
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

	util.AssertNoError(a.checkConsistencyBeforeWrapUp())

	a.wrapUpAttacher()

	a.vid.SetTxStatusGood()
	a.PostEventNewGood(vid)
	a.SendToTippool(vid)
	return vertex.Good, a.finals, nil
}

func newMilestoneAttacher(vid *vertex.WrappedTx, env Environment, metadata *txmetadata.TransactionMetadata) *milestoneAttacher {
	ret := &milestoneAttacher{
		attacher: newPastConeAttacher(env, vid.IDShortString()),
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
	ret.markVertexUndefined(vid)
	return ret
}

func (a *milestoneAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.Ctx().Done():
			return vertex.Bad
		case withVID := <-a.pokeChan:
			if withVID != nil {
				a.Tracef(TraceTagAttachMilestone, "poked with %s", withVID.IDShortString)
			}
		case <-time.After(periodicCheckEach):
			a.Tracef(TraceTagAttachMilestone, "periodic check")
		}
	}
}

func logFinalStatusString(vid *vertex.WrappedTx, finals *attachFinals, msData *ledger.MilestoneData) string {
	var msg string

	msDataStr := " (n/a)"
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
			bl = finals.baseline.StringShort()
		}
		if vid.IsBranchTransaction() {
			msg += fmt.Sprintf(" base: %s, new tx: %d, UTXO mut +%d/-%d, cov: %s, inflation: %s, supply: %s",
				bl, finals.numTransactions, finals.numCreatedOutputs, finals.numDeletedOutputs, finals.coverage.String(),
				util.GoTh(finals.slotInflation), util.GoTh(finals.supply))
		} else {
			msg += fmt.Sprintf(" base: %s, new tx: %d, cov: %s, inflation: %s",
				bl, finals.numTransactions, finals.coverage.String(), util.GoTh(finals.slotInflation))
		}
	}
	return msg
}

func (a *milestoneAttacher) close() {
	a.closeOnce.Do(func() {
		a.unReferenceAll()

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
				success = a.setBaseline(v.BaselineBranch, a.vid.Timestamp())
				util.Assertf(success, "solidifyBaseline %s: failed to set baseline", a.name)
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
		finalSuccess := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				if ok = a.attachVertexUnwrapped(v, a.vid, ledger.NilLedgerTime); !ok {
					util.AssertMustError(a.err)
					return
				}
				if ok, finalSuccess = a.validateSequencerTx(v, a.vid); !ok {
					util.AssertMustError(a.err)
					v.UnReferenceDependencies()
				}
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case finalSuccess:
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
