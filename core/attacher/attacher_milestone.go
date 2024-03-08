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
	TraceTagAttachMilestone = "milestone"
	periodicCheckEach       = 1 * time.Second
)

func runMilestoneAttacher(vid *vertex.WrappedTx, metadata *txmetadata.TransactionMetadata, callback func(vid *vertex.WrappedTx, err error), env Environment) {
	env.TraceTx(&vid.ID, "runMilestoneAttacher")

	a := newMilestoneAttacher(vid, env, metadata)
	defer func() {
		go a.close()
	}()

	finals, err := a.run()
	if err != nil {
		vid.SetTxStatusBad(err)
		env.Log().Errorf("-- ATTACH %s -> BAD(%v)", vid.ID.StringShort(), err)
		//env.Log().Errorf("-- ATTACH %s -> BAD(%v)\n>>>>>>>>>>>>> failed tx <<<<<<<<<<<<<\n%s\n<<<<<<<<<<<<<<<<<<<<<<<<<<",
		//	vid.ID.StringShort(), err, vid.LinesTx("      ").String())
	} else {
		env.Assertf(finals != nil, "finals != nil")
		msData := env.ParseMilestoneData(vid)
		if msData == nil {
			env.ParseMilestoneData(vid)
		}
		env.Log().Info(logFinalStatusString(vid, finals, msData))
	}

	env.PokeAllWith(vid)

	// calling callback with timeout in order to detect wrong callbacks immediately
	ok := util.CallWithTimeout(func() {
		callback(vid, err)
	}, 100*time.Millisecond)
	if !ok {
		env.Log().Fatalf("AttachTransaction: Internal error: 10 milisec timeout exceeded while calling callback")
	}
}

func newMilestoneAttacher(vid *vertex.WrappedTx, env Environment, metadata *txmetadata.TransactionMetadata) *milestoneAttacher {
	env.Assertf(vid.IsSequencerMilestone(), "newMilestoneAttacher: %s not a sequencer milestone", vid.IDShortString)

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

func (a *milestoneAttacher) run() (*attachFinals, error) {
	// first solidify baseline state
	status := a.solidifyBaseline()
	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "baseline solidification failed. Reason: %v", a.err)
		util.AssertMustError(a.err)
		return nil, a.err
	}

	a.Assertf(a.baseline != nil, "a.baseline != nil")
	// then continue with the rest
	a.Tracef(TraceTagAttachMilestone, "baseline is OK <- %s", a.baseline.IDShortString)

	status = a.solidifyPastCone()
	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "past cone solidification failed. Reason: %v", a.err)
		util.AssertMustError(a.err)
		return nil, a.err
	}
	a.Tracef(TraceTagAttachMilestone, "past cone OK")
	util.AssertNoError(a.err)
	util.AssertNoError(a.checkConsistencyBeforeWrapUp())

	// finalizing touches
	a.wrapUpAttacher()

	if a.vid.IsBranchTransaction() {
		// branch transaction vertex is immediately converted to the virtual transaction.
		// Thus branch transaction does not reference past cone
		a.Tracef(TraceTagAttachMilestone, ">>>>>>>>>>>>>>> ConvertVertexToVirtualTx: %s", a.vid.IDShortString())

		a.vid.ConvertVertexToVirtualTx()
	}

	a.vid.SetTxStatusGood()
	a.PostEventNewGood(a.vid)
	a.SendToTippool(a.vid)

	return a.finals, nil
}

func (a *milestoneAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		// repeat until becomes defined
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.Ctx().Done():
			a.setError(fmt.Errorf("attacher has been interruped"))
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
		a.unReferenceAllByAttacher()

		a.pokeMutex.Lock()
		a.closed = true
		close(a.pokeChan)
		a.vid.OnPoke(nil)
		a.pokeMutex.Unlock()
	})
}

func (a *milestoneAttacher) solidifyBaseline() vertex.Status {
	return a.lazyRepeat(func() vertex.Status {
		ok := false
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.solidifyBaselineVertex(v)
				if ok && v.BaselineBranch != nil {
					success = a.setBaseline(v.BaselineBranch, a.vid.Timestamp())
					a.Assertf(success, "solidifyBaseline %s: failed to set baseline", a.name)
				}
			},
			VirtualTx: func(_ *vertex.VirtualTransaction) {
				a.Log().Fatalf("solidifyBaseline: unexpected virtual tx %s", a.vid.IDShortString())
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
