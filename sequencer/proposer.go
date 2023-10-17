package sequencer

import (
	"bytes"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	utangle "github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
)

type (
	proposerTask interface {
		run()
		name() string
		makeMilestone(chainIn, stemIn *utangle.WrappedOutput, feeInputs []utangle.WrappedOutput, endorse []*utangle.WrappedTx) *transaction.Transaction
		trace(format string, args ...any)
		setTraceNAhead(n int64)
	}

	proposerTaskGeneric struct {
		strategyName    string
		factory         *milestoneFactory
		targetTs        core.LogicalTime
		alreadyProposed set.Set[[32]byte]
		traceNAhead     atomic.Int64
		startTime       time.Time
	}

	proposerTaskConstructor func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask

	proposerRegistered struct {
		constructor proposerTaskConstructor
		trace       *atomic.Bool
	}
)

var allProposingStrategies = make(map[string]proposerRegistered)

// registerProposingStrategy must always be called from init
func registerProposingStrategy(strategyName string, constructor proposerTaskConstructor) {
	allProposingStrategies[strategyName] = proposerRegistered{
		constructor: constructor,
		trace:       new(atomic.Bool),
	}
}

func SetTraceProposer(name string, v bool) {
	if _, ok := allProposingStrategies[name]; ok {
		allProposingStrategies[name].trace.Store(v)
	}
}

func newProposerGeneric(mf *milestoneFactory, targetTs core.LogicalTime, strategyName string) proposerTaskGeneric {
	return proposerTaskGeneric{
		factory:         mf,
		targetTs:        targetTs,
		strategyName:    strategyName,
		alreadyProposed: set.New[[32]byte](),
	}
}

func (c *proposerTaskGeneric) name() string {
	return fmt.Sprintf("%s-%s", c.strategyName, c.targetTs.String())
}

func (c *proposerTaskGeneric) setTraceNAhead(n int64) {
	c.traceNAhead.Store(n)
}

func (c *proposerTaskGeneric) traceEnabled() bool {
	if c.traceNAhead.Dec() >= 0 {
		return true
	}
	return allProposingStrategies[c.strategyName].trace.Load()
}

func (c *proposerTaskGeneric) trace(format string, args ...any) {
	if c.traceEnabled() {
		pref := fmt.Sprintf("TRACE(%s) -- ", c.name())
		c.factory.log.Infof(pref+format, util.EvalLazyArgs(args...)...)
	}
}

func (c *proposerTaskGeneric) forceTrace(format string, args ...any) {
	c.setTraceNAhead(1)
	c.trace(format, args...)
}

func (c *proposerTaskGeneric) startProposingTime() {
	c.startTime = time.Now()
}

func (c *proposerTaskGeneric) selectFeeInputs(seqVIDs ...*utangle.WrappedTx) ([]utangle.WrappedOutput, *utangle.WrappedOutput) {
	return c.factory.selectFeeInputs(c.targetTs, seqVIDs...)
}

func (c *proposerTaskGeneric) makeMilestone(chainIn, stemIn *utangle.WrappedOutput, feeInputs []utangle.WrappedOutput, endorse []*utangle.WrappedTx) *transaction.Transaction {
	util.Assertf(chainIn != nil, "chainIn != nil")
	util.Assertf(c.targetTs.TimeTick() != 0 || len(endorse) == 0, "proposer task %s: targetTs.TimeTick() != 0 || len(endorse) == 0", c.name())
	util.Assertf(len(feeInputs) <= c.factory.maxFeeInputs, "proposer task %s: len(feeInputs) <= mf.maxFeeInputs", c.name())

	ret, err := c.factory.makeMilestone(chainIn, stemIn, feeInputs, endorse, c.targetTs)
	util.Assertf(err == nil, "error in %s: %v", c.name(), err)
	if ret == nil {
		c.trace("makeMilestone: nil")
	} else {
		c.trace("makeMilestone: %s", ret.ID().Short())
	}
	return ret
}

// assessAndAcceptProposal returns reject reason of empty string, if accepted
func (c *proposerTaskGeneric) assessAndAcceptProposal(tx *transaction.Transaction, startTime time.Time, taskName string) {
	c.trace("inside assessAndAcceptProposal: %s", tx.IDShort())

	// prevent repeating transactions with same inputs
	hashOfProposal := tx.HashInputsAndEndorsements()
	if c.alreadyProposed.Contains(hashOfProposal) {
		c.trace("repeating proposal in '%s', wait 10ms %s", c.name(), tx.IDShort())
		time.Sleep(10 * time.Millisecond)
		return
	}
	c.alreadyProposed.Insert(hashOfProposal)

	makeVertexStartTime := time.Now()
	draftVertex, err := c.factory.tangle.SolidifyInputs(tx)
	if err != nil {
		c.factory.log.Errorf("assessAndAcceptProposal (%s, %s)::SolidifyInputs: %v", tx.Timestamp(), taskName, err)
		return
	}
	vid, err := c.factory.tangle.MakeVertex(draftVertex, true)

	const panicOnConflict = true
	{ // ----------- for testing only. Conflicts are possible at this point, no need to panic
		if err != nil && panicOnConflict {
			utangle.SaveGraphPastCone(vid, "makevertex")
			//vid.SaveTransactionsPastCone("makevertex")

			util.Panicf("assessAndAcceptProposal: (%s): '%v'\n========= Failed transaction ======\n%s",
				taskName, err, vid.String())
		}
	}

	if err != nil {
		//c.factory.log.Warnf("assessAndAcceptProposal::MakeVertex (%s, %s): %v", tx.Timestamp(), taskName, err)
		//c.factory.log.Errorf("assessAndAcceptProposal::MakeVertex (%s, %s): %v\nEndorsements: [%s]\n",
		//	vid.Timestamp(), taskName, err, draftVertex.Tx.EndorsementsVeryShort())
		//mStr := "mutations = nil"
		//if mut != nil {
		//	mStr = mut.String()
		//}
		//testutil.LogToFile("test.log", "----- %s\n===== mutations: %s\n===== transaction: %s\n",
		//	draftVertex.Tx.IDShort(), mStr, draftVertex.String())
		return
	}
	msData := &milestoneWithData{
		WrappedOutput:     *vid.MustSequencerOutput(),
		elapsed:           time.Since(startTime),
		makeVertexElapsed: time.Since(makeVertexStartTime),
		proposedBy:        taskName,
	}
	if rejectReason := c.placeProposalIfRelevant(msData); rejectReason != "" {
		c.trace(rejectReason)
	}
}

func (c *proposerTaskGeneric) storeProposalDuration() {
	c.factory.storeProposalDuration(time.Since(c.startTime))
}

func (c *proposerTaskGeneric) placeProposalIfRelevant(mdProposed *milestoneWithData) string {
	c.factory.proposal.mutex.Lock()
	defer c.factory.proposal.mutex.Unlock()

	if c.factory.proposal.targetTs == core.NilLogicalTime {
		return fmt.Sprintf("%s SKIPPED: target is nil", mdProposed.IDShort())
	}

	// decide if it is not lagging behind the target
	if mdProposed.Timestamp() != c.factory.proposal.targetTs {
		c.factory.log.Warnf("%s: proposed milestone (%s) is lagging behind target %s. Generation duration: %v/%v",
			mdProposed.proposedBy, mdProposed.Timestamp().String(), c.factory.proposal.targetTs.String(), mdProposed.elapsed, mdProposed.makeVertexElapsed)
		return fmt.Sprintf("%s SKIPPED: task is behind target", mdProposed.IDShort())
	}

	if c.factory.proposal.bestSoFar != nil && *c.factory.proposal.bestSoFar == mdProposed.WrappedOutput {
		return fmt.Sprintf("%s SKIPPED: repeating", mdProposed.IDShort())
	}

	if !mdProposed.VID.IsBranchTransaction() {
		// if not branch, check if it increases coverage
		if c.factory.proposal.bestSoFar != nil {
			proposedCoverage := c.factory.tangle.LedgerCoverage(mdProposed.VID)
			baselineCoverage := c.factory.tangle.LedgerCoverage(c.factory.proposal.bestSoFar.VID)

			if proposedCoverage <= baselineCoverage {
				return fmt.Sprintf("%s SKIPPED: no increase in coverage %s <- %s of %s)",
					mdProposed.IDShort(), util.GoThousands(proposedCoverage),
					util.GoThousands(baselineCoverage), c.factory.proposal.bestSoFar.VID.IDShort())
			}
		}
	}

	// branch proposals always accepted

	var baselineCoverage uint64
	if c.factory.proposal.bestSoFar != nil {
		baselineCoverage = c.factory.tangle.LedgerCoverage(c.factory.proposal.bestSoFar.VID)
	}
	c.factory.proposal.current = &mdProposed.WrappedOutput
	c.factory.proposal.bestSoFar = c.factory.proposal.current
	ledgerCoverageProposed := c.factory.tangle.LedgerCoverage(c.factory.proposal.current.VID)

	c.trace("(%s): ACCEPTED %s, coverage: %s (base: %s), elapsed: %v/%v, inputs: %d, tipPool: %d",
		mdProposed.proposedBy,
		mdProposed.IDShort(),
		util.GoThousands(ledgerCoverageProposed),
		util.GoThousands(baselineCoverage),
		mdProposed.elapsed,
		mdProposed.makeVertexElapsed,
		mdProposed.VID.NumInputs(),
		c.factory.tipPool.numOutputsInBuffer(),
	)
	return ""
}

// betterMilestone returns if vid1 is strongly better than vid2
func isPreferredMilestoneAgainstTheOther(ut *utangle.UTXOTangle, vid1, vid2 *utangle.WrappedTx) bool {
	util.Assertf(vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone(), "vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone()")

	if vid1 == vid2 {
		return false
	}
	if vid2 == nil {
		return true
	}

	coverage1 := ut.LedgerCoverage(vid1)
	coverage2 := ut.LedgerCoverage(vid2)
	switch {
	case coverage1 > coverage2:
		// main preference is by ledger coverage
		return true
	case coverage1 == coverage2:
		// in case of equal coverage hash will be used
		return bytes.Compare(vid1.ID()[:], vid2.ID()[:]) > 0
	default:
		return false
	}
}
