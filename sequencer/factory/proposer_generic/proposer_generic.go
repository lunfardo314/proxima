package proposer_generic

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		ContinueCandidateProposing(ts ledger.LogicalTime) bool
		OwnLatestMilestone() vertex.WrappedOutput
		Propose(a *attacher.IncrementalAttacher) bool
		AttachTagAlongInputs(a *attacher.IncrementalAttacher)
	}

	Task interface {
		Name() string
		Run()
	}

	TaskGeneric struct {
		Environment
		Strategy        *Strategy
		TargetTs        ledger.LogicalTime
		alreadyProposed set.Set[[32]byte]
	}

	TaskConstructor func(generic *TaskGeneric) Task

	Strategy struct {
		Name        string
		Constructor TaskConstructor
	}
)

func New(env Environment, strategy *Strategy, targetTs ledger.LogicalTime) Task {
	return strategy.Constructor(&TaskGeneric{
		Environment:     env,
		Strategy:        strategy,
		TargetTs:        targetTs,
		alreadyProposed: make(set.Set[[32]byte]),
	})
}

func (t *TaskGeneric) Name() string {
	return fmt.Sprintf("%s-%s", t.Strategy.Name, t.TargetTs.String())
}

//
//func (c *TaskGeneric) setTraceNAhead(n int64) {
//	c.traceNAhead.Store(n)
//}
//
//func (c *TaskGeneric) traceEnabled() bool {
//	reg, registered := allProposingStrategies[c.strategyName]
//	if !registered {
//		return false
//	}
//	if c.traceNAhead.Dec() >= 0 {
//		return true
//	}
//	return reg.Trace.Load()
//}
//
//func (c *TaskGeneric) Trace(format string, args ...any) {
//	if c.traceEnabled() {
//		pref := fmt.Sprintf("TRACE(%s) -- ", c.Name())
//		c.factory.log.Infof(pref+format, util.EvalLazyArgs(args...)...)
//	}
//}
//
//func (c *TaskGeneric) forceTrace(format string, args ...any) {
//	c.setTraceNAhead(1)
//	c.Trace(format, args...)
//}

//func (c *TaskGeneric) startProposingTime() {
//	c.startTime = time.Now()
//}
//
//
//
//// assessAndAcceptProposal returns reject reason of empty string, if accepted
//func (c *TaskGeneric) assessAndAcceptProposal(tx *transaction.Transaction, extend utangle.WrappedOutput, startTime time.Time, taskName string) bool {
//	c.trace("inside assessAndAcceptProposal: %s", tx.IDShortString())
//
//	// prevent repeating transactions with same consumedInThePastPath
//	hashOfProposal := tx.HashInputsAndEndorsements()
//	if c.alreadyProposed.Contains(hashOfProposal) {
//		c.trace("repeating proposal in '%s', wait 10ms %s", c.name(), tx.IDShortString())
//		time.Sleep(10 * time.Millisecond)
//		return false
//	}
//	c.alreadyProposed.Insert(hashOfProposal)
//
//	coverage, err := c.factory.utangle.LedgerCoverageFromTransaction(tx)
//	if err != nil {
//		c.factory.log.Warnf("assessAndAcceptProposal::LedgerCoverageFromTransaction (%s, %s): %v", tx.Timestamp(), taskName, err)
//	}
//
//	//c.setTraceNAhead(1)
//	//c.Trace("LedgerCoverageFromTransaction %s = %d", tx.IDShortString(), coverage)
//
//	msData := &proposedMilestoneWithData{
//		tx:         tx,
//		extended:   extend,
//		coverage:   coverage,
//		elapsed:    time.Since(startTime),
//		proposedBy: taskName,
//	}
//	rejectReason, forceExit := c.placeProposalIfRelevant(msData)
//	if rejectReason != "" {
//		//c.setTraceNAhead(1)
//		c.trace(rejectReason)
//
//	}
//	return forceExit
//}
//
//func (c *TaskGeneric) placeProposalIfRelevant(mdProposed *proposedMilestoneWithData) (string, bool) {
//	c.factory.proposal.mutex.Lock()
//	defer c.factory.proposal.mutex.Unlock()
//
//	//c.setTraceNAhead(1)
//	c.trace("proposed %s: coverage: %s (base %s), numIN: %d, elapsed: %v",
//		mdProposed.proposedBy, util.GoThousands(mdProposed.coverage), util.GoThousands(c.factory.proposal.bestSoFarCoverage),
//		mdProposed.tx.NumInputs(), mdProposed.elapsed)
//
//	if c.factory.proposal.targetTs == ledger.NilLogicalTime {
//		return fmt.Sprintf("%s SKIPPED: target is nil", mdProposed.tx.IDShortString()), false
//	}
//
//	// decide if it is not lagging behind the target
//	if mdProposed.tx.Timestamp() != c.factory.proposal.targetTs {
//		c.factory.log.Warnf("%s: proposed milestone timestamp %s is behind current target %s. Generation duration: %v",
//			mdProposed.proposedBy, mdProposed.tx.Timestamp().String(), c.factory.proposal.targetTs.String(), mdProposed.elapsed)
//		return fmt.Sprintf("%s SKIPPED: task is behind target", mdProposed.tx.IDShortString()), true
//	}
//
//	if c.factory.proposal.current != nil && *c.factory.proposal.current.ID() == *mdProposed.tx.ID() {
//		return fmt.Sprintf("%s SKIPPED: repeating", mdProposed.tx.IDShortString()), false
//	}
//
//	baselineCoverage := c.factory.proposal.bestSoFarCoverage
//
//	if !mdProposed.tx.IsBranchTransaction() {
//		if mdProposed.coverage <= baselineCoverage {
//			return fmt.Sprintf("%s SKIPPED: no increase in coverage %s <- %s)",
//				mdProposed.tx.IDShortString(), util.GoThousands(mdProposed.coverage), util.GoThousands(c.factory.proposal.bestSoFarCoverage)), false
//		}
//	}
//
//	// branch proposals always accepted
//	c.factory.proposal.bestSoFarCoverage = mdProposed.coverage
//	c.factory.proposal.current = mdProposed.tx
//	c.factory.proposal.currentExtended = mdProposed.extended
//
//	//c.setTraceNAhead(1)
//	c.trace("(%s): ACCEPTED %s, coverage: %s (base: %s), elapsed: %v, inputs: %d, tipPool: %d",
//		mdProposed.proposedBy,
//		mdProposed.tx.IDShortString(),
//		util.GoThousands(mdProposed.coverage),
//		util.GoThousands(baselineCoverage),
//		mdProposed.elapsed,
//		mdProposed.tx.NumInputs(),
//		c.factory.tipPool.numOutputsInBuffer(),
//	)
//	return "", false
//}
//
//// extensionChoicesInEndorsementTargetPastCone sorted by coverage descending
//// excludes those pairs which are marked already visited
//func (c *TaskGeneric) extensionChoicesInEndorsementTargetPastCone(endorsementTarget *utangle.WrappedTx) []utangle.WrappedOutput {
//	stateRdr := c.factory.utangle.MustGetBaselineState(endorsementTarget)
//	rdr := multistate.MakeSugared(stateRdr)
//
//	anotherSeqID := endorsementTarget.MustSequencerID()
//	rootOutput, err := rdr.GetChainOutput(&c.factory.tipPool.chainID)
//	if errors.Is(err, multistate.ErrNotFound) {
//		// cannot find own seqID in the state of anotherSeqID. The tree is empty
//		c.trace("cannot find own seqID %s in the state of another seq %s (%s). The tree is empty",
//			c.factory.tipPool.chainID.StringVeryShort(), endorsementTarget.IDShort(), anotherSeqID.StringVeryShort())
//		return nil
//	}
//	util.AssertNoError(err)
//	c.trace("found own seqID %s in the state of another seq %s (%s)",
//		c.factory.tipPool.chainID.StringVeryShort(), endorsementTarget.IDShort(), anotherSeqID.StringVeryShort())
//
//	rootWrapped, ok, _ := c.factory.utangle.GetWrappedOutput(&rootOutput.ID, rdr)
//	if !ok {
//		c.trace("cannot fetch wrapped root output %s", rootOutput.IDShort())
//		return nil
//	}
//	c.factory.addOwnMilestone(rootWrapped) // to ensure it is among own milestones
//
//	cone := c.futureConeMilestonesOrdered(rootWrapped.VID)
//
//	return util.FilterSlice(cone, func(extensionChoice utangle.WrappedOutput) bool {
//		return !c.alreadyVisited(extensionChoice.VID, endorsementTarget)
//	})
//}
//
//func (c *TaskGeneric) futureConeMilestonesOrdered(rootVID *utangle.WrappedTx) []utangle.WrappedOutput {
//	c.factory.cleanOwnMilestonesIfNecessary()
//
//	c.factory.mutex.RLock()
//	defer c.factory.mutex.RUnlock()
//
//	//p.setTraceNAhead(1)
//	c.trace("futureConeMilestonesOrdered for root %s. Total %d own milestones", rootVID.LazyIDShort(), len(c.factory.ownMilestones))
//
//	om, ok := c.factory.ownMilestones[rootVID]
//	util.Assertf(ok, "futureConeMilestonesOrdered: milestone %s of chain %s is expected to be among set of own milestones (%d)",
//		rootVID.LazyIDShort(),
//		func() any { return c.factory.tipPool.chainID.StringShort() },
//		len(c.factory.ownMilestones))
//
//	rootOut := om.WrappedOutput
//	ordered := util.SortKeys(c.factory.ownMilestones, func(vid1, vid2 *utangle.WrappedTx) bool {
//		// by timestamp -> equivalent to topological order, ascending, i.e. older first
//		return vid1.Timestamp().Before(vid2.Timestamp())
//	})
//
//	visited := set.New[*utangle.WrappedTx](rootVID)
//	ret := append(make([]utangle.WrappedOutput, 0, len(ordered)), rootOut)
//	for _, vid := range ordered {
//		if !vid.IsDeleted() &&
//			vid.IsSequencerMilestone() &&
//			visited.Contains(vid.SequencerPredecessor()) &&
//			ledger.ValidTimePace(vid.Timestamp(), c.TargetTs) {
//			visited.Insert(vid)
//			ret = append(ret, c.factory.ownMilestones[vid].WrappedOutput)
//		}
//	}
//	return ret
//}
//
//// betterMilestone returns if vid1 is strongly better than vid2
//func isPreferredMilestoneAgainstTheOther(ut *utangle.UTXOTangle, vid1, vid2 *utangle.WrappedTx) bool {
//	util.Assertf(vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone(), "vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone()")
//
//	if vid1 == vid2 {
//		return false
//	}
//	if vid2 == nil {
//		return true
//	}
//
//	coverage1 := ut.LedgerCoverage(vid1)
//	coverage2 := ut.LedgerCoverage(vid2)
//	switch {
//	case coverage1 > coverage2:
//		// main preference is by ledger coverage
//		return true
//	case coverage1 == coverage2:
//		// in case of equal coverage hash will be used
//		return bytes.Compare(vid1.ID()[:], vid2.ID()[:]) > 0
//	default:
//		return false
//	}
//}
//
//func milestoneSliceString(path []utangle.WrappedOutput) string {
//	ret := make([]string, 0)
//	for _, md := range path {
//		ret = append(ret, "       "+md.IDShort())
//	}
//	return strings.Join(ret, "\n")
//}
