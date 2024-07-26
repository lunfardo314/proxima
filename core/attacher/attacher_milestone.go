package attacher

import (
	"fmt"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// TODO handle attaching timeout otherwise attackable

const (
	TraceTagAttachMilestone = "milestone"
	periodicCheckEach       = 100 * time.Millisecond
)

func runMilestoneAttacher(vid *vertex.WrappedTx, metadata *txmetadata.TransactionMetadata, callback func(vid *vertex.WrappedTx, err error), env Environment) {
	a := newMilestoneAttacher(vid, env, metadata)
	defer func() {
		go a.close()
	}()

	err := a.run()

	if err != nil {
		vid.SetTxStatusBad(err)
		env.Log().Warnf(a.logErrorStatusString(err))

		// panic("fail fast")

	} else {
		if vid.IsBranchTransaction() {
			env.EvidenceBranchSlot(vid.Slot())
		}
		msData := env.ParseMilestoneData(vid)
		if vid.IsBranchTransaction() {
			env.Infof0(a.logFinalStatusString(msData))
		} else {
			env.Infof1(a.logFinalStatusString(msData))
		}
		vid.SetSequencerAttachmentFinished()
	}

	env.PokeAllWith(vid)
	if metadata != nil &&
		vid.IsBranchTransaction() &&
		metadata.PortionInfo != nil &&
		metadata.PortionInfo.LastIndex > 0 &&
		metadata.PortionInfo.Index == metadata.PortionInfo.LastIndex {
		env.NotifyEndOfPortion()
	}

	// calling callback with timeout in order to detect wrong callbacks immediately
	const callbackMustFinishIn = 200 * time.Millisecond
	util.CallWithTimeout(env.Ctx(), 200*time.Millisecond,
		func() {
			callback(vid, err)
		}, func() {
			env.Log().Fatalf("AttachTransaction: internal error: %v second timeout exceeded while calling callback", callbackMustFinishIn)
		})
}

func newMilestoneAttacher(vid *vertex.WrappedTx, env Environment, metadata *txmetadata.TransactionMetadata) *milestoneAttacher {
	env.Assertf(vid.IsSequencerMilestone(), "newMilestoneAttacher: %s is not a sequencer milestone", vid.IDShortString)

	ret := &milestoneAttacher{
		attacher: newPastConeAttacher(env, vid.IDShortString()),
		vid:      vid,
		metadata: metadata,
		pokeChan: make(chan struct{}),
		finals:   attachFinals{started: time.Now()},
	}
	ret.Tracef(TraceTagCoverageAdjustment, "newMilestoneAttacher: metadata of %s: %s", vid.IDShortString, metadata.String)

	ret.attacher.pokeMe = func(vid *vertex.WrappedTx) {
		ret.pokeMe(vid)
	}
	ret.vid.OnPoke(func() {
		ret._doPoke()
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

func (a *milestoneAttacher) run() error {
	// first solidify baseline state
	status := a.solidifyBaseline()
	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "baseline solidification failed. Reason: %v", a.err)
		util.AssertMustError(a.err)
		return a.err
	}

	a.Assertf(a.baseline != nil, "a.baseline != nil")
	// then continue with the rest
	a.Tracef(TraceTagAttachMilestone, "baseline is OK <- %s", a.baseline.IDShortString)

	status = a.solidifyPastCone()

	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "past cone solidification failed. Reason: %v", a.err)
		a.AssertMustError(a.err)
		return a.err
	}
	a.Tracef(TraceTagAttachMilestone, "past cone OK")
	a.AssertNoError(a.err)

	a.AdjustCoverage()

	a.AssertNoError(a.checkConsistencyBeforeWrapUp())

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

	return nil
}

func (a *milestoneAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		// repeat until becomes defined
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.pokeChan:
			a.finals.numPokes++
			a.Tracef(TraceTagAttachMilestone, "poked")
		case <-a.Ctx().Done():
			a.setError(fmt.Errorf("attacher has been interrupted"))
			return vertex.Bad
		case <-time.After(periodicCheckEach):
			a.finals.numPeriodic++
			a.Tracef(TraceTagAttachMilestone, "periodic check")
		}
	}
}

func (a *milestoneAttacher) close() {
	a.closeOnce.Do(func() {
		a.unReferenceAllByAttacher()

		a.pokeClosingMutex.Lock()
		defer a.pokeClosingMutex.Unlock()

		a.closed = true
		close(a.pokeChan)
		a.vid.OnPoke(nil)
	})
}

func (a *milestoneAttacher) solidifyBaseline() vertex.Status {
	return a.lazyRepeat(func() vertex.Status {
		ok := false
		success := false
		util.Assertf(a.vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted), "AttachmentStarted flag must be up")
		util.Assertf(!a.vid.FlagsUp(vertex.FlagVertexTxAttachmentFinished), "AttachmentFinished flag must be down")

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
				if ok = a.attachVertexUnwrapped(v, a.vid); !ok {
					util.AssertMustError(a.err)
					return
				}
				if ok, finalSuccess = a.validateSequencerTxUnwrapped(v); !ok {
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

func (a *milestoneAttacher) validateSequencerTxUnwrapped(v *vertex.Vertex) (ok, finalSuccess bool) {
	flags := a.flags(a.vid)
	if !flags.FlagsUp(FlagAttachedVertexEndorsementsSolid) || !flags.FlagsUp(FlagAttachedVertexInputsSolid) {
		return true, false
	}
	// inputs solid
	glbFlags := a.vid.FlagsNoLock()
	a.Assertf(!glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid), "%s: !glbFlags.FlagsUp(vertex.FlagConstraintsValid) in %s", a.name, a.vid.IDShortString)

	if err := v.ValidateConstraints(); err != nil {
		a.setError(err)
		a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", a.vid.IDShortString, err)
		return false, false
	}
	a.vid.SetFlagsUpNoLock(vertex.FlagVertexConstraintsValid)
	a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.Tx.IDShortString)
	return true, true
}

func (a *milestoneAttacher) _doPoke() {
	a.pokeClosingMutex.RLock()
	defer a.pokeClosingMutex.RUnlock()

	// must be non-blocking, otherwise deadlocks when syncing or high TPS
	if !a.closed {
		select {
		case a.pokeChan <- struct{}{}:
			//a.Log().Warnf(">>>>>> poked ok %s", a.name)
		default:
			// poke is lost when blocked but that is ok because there's pull from the attacher's side
			//a.Log().Warnf(">>>>>> missed poke in %s", a.name)
			a.finals.numMissedPokes.Add(1)
		}
	}
}

func (a *milestoneAttacher) pokeMe(with *vertex.WrappedTx) {
	flags := a.flags(with)
	util.Assertf(flags.FlagsUp(FlagAttachedVertexKnown), "must be marked known %s", with.IDShortString)
	if !flags.FlagsUp(FlagAttachedVertexAskedForPoke) {
		a.Tracef(TraceTagAttachMilestone, "pokeMe with %s", with.IDShortString())
		a.PokeMe(a.vid, with)
		a.setFlagsUp(with, FlagAttachedVertexAskedForPoke)
	}
}

func (a *milestoneAttacher) logFinalStatusString(msData *ledger.MilestoneData) string {
	var msg string

	msDataStr := " (n/a)"
	if msData != nil {
		msDataStr = fmt.Sprintf(" (%s %d/%d)", msData.Name, msData.BranchHeight, msData.ChainHeight)
	}
	inflChainStr := "-"
	inflBranchStr := "-"
	if inflationConstraint := a.vid.InflationConstraintOnSequencerOutput(); inflationConstraint != nil {
		inflChainStr = util.Th(inflationConstraint.ChainInflation)
		inflBranchStr = util.Th(ledger.L().BranchInflationBonusFromRandomnessProof(inflationConstraint.VRFProof))
	}

	if a.vid.IsBranchTransaction() {
		msg = fmt.Sprintf("-- ATTACH BRANCH%s %s(in %d/out %d), ci=%s/bi=%s",
			msDataStr, a.vid.IDShortString(), a.finals.numInputs, a.finals.numOutputs, inflChainStr, inflBranchStr)
	} else {
		msg = fmt.Sprintf("-- ATTACH SEQ TX%s %s(in %d/out %d), ci=%s/bi=%s",
			msDataStr, a.vid.IDShortString(), a.finals.numInputs, a.finals.numOutputs, inflChainStr, inflBranchStr)
	}
	if a.vid.GetTxStatus() == vertex.Bad {
		msg += fmt.Sprintf("BAD: err = '%v'", a.vid.GetError())
	} else {
		bl := "<nil>"
		if a.finals.baseline != nil {
			bl = a.finals.baseline.StringShort()
		}
		msg += fmt.Sprintf(", base: %s, cov: %s", bl, util.Th(a.finals.coverage))
		if a.VerbosityLevel() > 0 {
			if a.vid.IsBranchTransaction() {
				msg += fmt.Sprintf(", slot inflation: %s, supply: %s", util.Th(a.finals.slotInflation), util.Th(a.finals.supply))
			} else {
				msg += fmt.Sprintf(", slot inflation: %s", util.Th(a.finals.slotInflation))
			}
		}
	}
	if a.LogAttacherStats() {
		msg += "\n          " + a.logStatsString()
	}
	return msg
}

func (a *milestoneAttacher) logErrorStatusString(err error) string {
	msg := fmt.Sprintf("-- ATTACH %s -> BAD(%v)", a.vid.ID.StringShort(), err)
	if a.LogAttacherStats() {
		msg = msg + "\n          " + a.logStatsString()
	}
	return msg
}

func (a *milestoneAttacher) logStatsString() string {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memStr := fmt.Sprintf("Mem. alloc: %.1f MB, GC: %d, GoRt: %d, ",
		float32(memStats.Alloc*10/(1024*1024))/10,
		memStats.NumGC,
		runtime.NumGoroutine(),
	)

	utxoInOut := ""
	if a.vid.IsBranchTransaction() {
		utxoInOut = fmt.Sprintf("UTXO mut +%d/-%d, ", a.finals.numCreatedOutputs, a.finals.numDeletedOutputs)
	}
	return fmt.Sprintf("stats %s: new tx: %d, %spoked/missed: %d/%d, periodic: %d, duration: %s. %s",
		a.vid.IDShortString(),
		a.finals.numTransactions,
		utxoInOut,
		a.finals.numPokes,
		a.finals.numMissedPokes.Load(),
		a.finals.numPeriodic,
		time.Since(a.finals.started),
		memStr,
	)
}

func (a *milestoneAttacher) AdjustCoverage() {
	a.adjustCoverage()
	if a.coverageAdjustment > 0 {
		a.Tracef(TraceTagCoverageAdjustment, " milestoneAttacher: coverage has been adjusted by %s, ms: %s, baseline: %s",
			func() string { return util.Th(a.coverageAdjustment) }, a.vid.IDShortString, a.baseline.IDShortString)
	}
}
