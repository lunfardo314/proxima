package attacher

import (
	"context"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// TODO handle attaching timeout otherwise attackable

const (
	TraceTagAttachMilestone = "milestone"
	periodicCheckEach       = 100 * time.Millisecond
)

func runMilestoneAttacher(
	vid *vertex.WrappedTx,
	metadata *txmetadata.TransactionMetadata,
	callback func(vid *vertex.WrappedTx, err error),
	env Environment,
	ctx context.Context,
) {
	a := newMilestoneAttacher(vid, env, metadata, ctx)
	var err error

	defer func() {
		go a.close()
		// it is guaranteed callback will always be called, if any
		if callback != nil {
			callback(vid, err)
		}
	}()

	if err = a.run(); err != nil {
		// FIXME
		/*
			assertion failed:: vid.GetTxStatusNoLock() != Good. SetTxStatusBadNoLock err = solidification deadline (5s) in [54265|0br]aea1f2..: dependency [54264|58sq]118998.. is not solid
				github.com/lunfardo314/proxima/util.Assertf(0xc0?, {0x146a6ae, 0x3e}, {0xc01063fe48?, 0x141f750?, 0x1455264?})
				        /home/lunfardo/go/src/github.com/lunfardo314/proxima/util/util_assert.go:33 +0x99
				github.com/lunfardo314/proxima/core/vertex.(*WrappedTx).SetTxStatusBadNoLock(0xc004fcd450, {0x1839cc0, 0xc01c2ecf80})
				        /home/lunfardo/go/src/github.com/lunfardo314/proxima/core/vertex/vid.go:134 +0xc5
				github.com/lunfardo314/proxima/core/vertex.(*WrappedTx).SetTxStatusBad(0xc004fcd450, {0x1839cc0, 0xc01c2ecf80})
				        /home/lunfardo/go/src/github.com/lunfardo314/proxima/core/vertex/vid.go:128 +0x85
				github.com/lunfardo314/proxima/core/attacher.runMilestoneAttacher(0xc004fcd450, 0xc0043fe120, 0x0, {0x1863160, 0xc0000d19e0}, {0x0?, 0x0?})
				        /home/lunfardo/go/src/github.com/lunfardo314/proxima/core/attacher/attacher_milestone.go:41 +0x118
				github.com/lunfardo314/proxima/core/attacher.AttachTransaction.func1.1()
				        /home/lunfardo/go/src/github.com/lunfardo314/proxima/core/attacher/attach.go:141 +0xcf
				created by github.com/lunfardo314/proxima/core/attacher.AttachTransaction.func1 in goroutine 17056
				        /home/lunfardo/go/src/github.com/lunfardo314/proxima/core/attacher/attach.go:137 +0x35e
		*/
		vid.SetTxStatusBad(err)
		env.Log().Warnf(a.logErrorStatusString(err))
		// panic("fail fast")
	} else {
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
}

func newMilestoneAttacher(vid *vertex.WrappedTx, env Environment, metadata *txmetadata.TransactionMetadata, providedCtx context.Context) *milestoneAttacher {
	env.Assertf(vid.IsSequencerMilestone(), "newMilestoneAttacher: %s is not a sequencer milestone", vid.IDShortString)

	ret := &milestoneAttacher{
		attacher: newPastConeAttacher(env, vid.IDShortString()),
		vid:      vid,
		metadata: metadata,
		pokeChan: make(chan struct{}),
		finals:   attachFinals{started: time.Now()},
		ctx:      providedCtx,
	}
	if ret.ctx == nil {
		ret.ctx = env.Ctx()
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

	if status := a.solidifyBaseline(); status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "baseline solidification failed. Reason: %v", a.err)
		util.AssertMustError(a.err)
		return a.err
	}

	a.Assertf(a.baseline != nil, "a.baseline != nil")
	a.Tracef(TraceTagAttachMilestone, "baseline is OK <- %s", a.baseline.IDShortString)

	// then solidify past cone

	if status := a.solidifyPastCone(); status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "past cone solidification failed. Reason: %v", a.err)
		a.Assertf(a.err != nil, "a.err!=nil")
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

// lazyRepeat repeats closure until it returns Good or Bad
func (a *milestoneAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		// repeat until becomes defined or interrupted
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.pokeChan:
			a.finals.numPokes++
			a.Tracef(TraceTagAttachMilestone, "poked")

		case <-a.ctx.Done():
			a.setError(fmt.Errorf("%w. Undefined past cone: %s", global.ErrInterrupted, a.undefinedListLines().Join(", ")))
			return vertex.Bad

		case <-time.After(periodicCheckEach):
			a.finals.numPeriodic++
			a.Tracef(TraceTagAttachMilestone, "periodic check")
		}
	}
}

func (a *milestoneAttacher) close() {
	a.closeOnce.Do(func() {
		a.referenced.unReferenceAll()

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
		finalSuccess := false
		util.Assertf(a.vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted), "AttachmentStarted flag must be up")
		util.Assertf(!a.vid.FlagsUp(vertex.FlagVertexTxAttachmentFinished), "AttachmentFinished flag must be down")

		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				a.Assertf(a.vid.GetTxStatusNoLock() == vertex.Undefined, "a.vid.GetTxStatusNoLock() == vertex.Undefined:\nflags: %s", a.vid.FlagsNoLock().String)

				if err := checkSolidificationDeadline(v); err != nil {
					a.setError(err)
					ok = false
					return
				}

				ok = a.solidifyBaselineVertex(v)
				if ok && v.BaselineBranch != nil {
					finalSuccess = a.setBaseline(v.BaselineBranch, a.vid.Timestamp())
					a.Assertf(finalSuccess, "solidifyBaseline %s: failed to set baseline", a.name)
				}
			},
			VirtualTx: func(_ *vertex.VirtualTransaction) {
				a.Log().Fatalf("solidifyBaseline: unexpected virtual tx %s", a.vid.IDShortString())
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

// solidifyPastCone solidifies and validates sequencer transaction in the context of known baseline state
func (a *milestoneAttacher) solidifyPastCone() vertex.Status {
	return a.lazyRepeat(func() (status vertex.Status) {
		ok := false
		finalSuccess := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				a.Assertf(a.vid.GetTxStatusNoLock() == vertex.Undefined, "a.vid.GetTxStatusNoLock() == vertex.Undefined")

				if err := checkSolidificationDeadline(v); err != nil {
					a.setError(err)
					ok = false
					return
				}
				if ok = a.attachVertexUnwrapped(v, a.vid); !ok {
					a.Assertf(a.err != nil, "a.err != nil")
					return
				}
				if ok, finalSuccess = a.validateSequencerTxUnwrapped(v); !ok {
					a.Assertf(a.err != nil, "a.err != nil")
					// dispose vertex
					v.UnReferenceDependencies()
				}
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case finalSuccess:
			util.Assertf(!a.containsUndefinedExcept(a.vid),
				"inconsistency: attacher %s is 'finalSuccess' but still contains undefined vertices. Lines:\n%s",
				a.name, a.dumpLinesString)
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

func (a *milestoneAttacher) validateSequencerTxUnwrapped(v *vertex.Vertex) (ok, finalSuccess bool) {
	if a.containsUndefinedExcept(a.vid) {
		return true, false
	}
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
		msg = fmt.Sprintf("ATTACH BRANCH%s %s(in %d/out %d, new tx: %d), ci=%s/bi=%s, %s, lnow: %s",
			msDataStr, a.vid.IDShortString(), a.finals.numInputs, a.finals.numOutputs, a.finals.numNewTransactions,
			inflChainStr, inflBranchStr, global.IsHealthyCoverageString(a.finals.coverage, a.finals.supply, global.FractionHealthyBranch), ledger.TimeNow().String())
	} else {
		msg = fmt.Sprintf("ATTACH SEQ TX%s %s(in %d/out %d, new tx: %d), ci=%s/bi=%s, lnow: %s",
			msDataStr, a.vid.IDShortString(), a.finals.numInputs, a.finals.numOutputs, a.finals.numNewTransactions, inflChainStr, inflBranchStr, ledger.TimeNow().String())
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
	return msg
}

func (a *milestoneAttacher) logErrorStatusString(err error) string {
	return fmt.Sprintf("ATTACH %s -> BAD(%v)", a.vid.ID.StringShort(), err)
}

func (a *milestoneAttacher) AdjustCoverage() {
	a.adjustCoverage()
	if a.coverageAdjustment > 0 {
		a.Tracef(TraceTagCoverageAdjustment, " milestoneAttacher: accumulatedCoverage has been adjusted by %s, ms: %s, baseline: %s",
			func() string { return util.Th(a.coverageAdjustment) }, a.vid.IDShortString, a.baseline.IDShortString)
	}
}

func checkSolidificationDeadline(v *vertex.Vertex) error {
	if notSolid, baselineNotSolid := v.DependencySolidificationDeadlineIsDue(); notSolid != nil {
		depStr := "dependency"
		if baselineNotSolid {
			depStr = "baseline"
		}
		return fmt.Errorf("%w (%v) in %s: %s %s is not solid",
			ErrSolidificationDeadline, PullTimeout, v.Tx.IDShortString(), depStr, notSolid.StringShort())
	}
	return nil
}
