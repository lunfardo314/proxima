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

func (c *proposerTaskGeneric) selectInputs(ownMs utangle.WrappedOutput, seqVIDs ...*utangle.WrappedTx) ([]utangle.WrappedOutput, *utangle.WrappedOutput) {
	return c.factory.selectInputs(c.targetTs, ownMs, seqVIDs...)
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
func (c *proposerTaskGeneric) assessAndAcceptProposal(tx *transaction.Transaction, extend utangle.WrappedOutput, startTime time.Time, taskName string) {
	c.trace("inside assessAndAcceptProposal: %s", tx.IDShort())

	// prevent repeating transactions with same consumedInThePastPath
	hashOfProposal := tx.HashInputsAndEndorsements()
	if c.alreadyProposed.Contains(hashOfProposal) {
		c.trace("repeating proposal in '%s', wait 10ms %s", c.name(), tx.IDShort())
		time.Sleep(10 * time.Millisecond)
		return
	}
	c.alreadyProposed.Insert(hashOfProposal)

	coverage, err := c.factory.tangle.LedgerCoverageFromTransaction(tx)
	if err != nil {
		c.factory.log.Warnf("assessAndAcceptProposal::LedgerCoverageFromTransaction (%s, %s): %v", tx.Timestamp(), taskName, err)
	}

	//c.setTraceNAhead(1)
	//c.trace("LedgerCoverageFromTransaction %s = %d", tx.IDShort(), coverage)

	msData := &proposedMilestoneWithData{
		tx:         tx,
		extended:   extend,
		coverage:   coverage,
		elapsed:    time.Since(startTime),
		proposedBy: taskName,
	}
	if rejectReason := c.placeProposalIfRelevant(msData); rejectReason != "" {
		//c.setTraceNAhead(1)
		c.trace(rejectReason)
	}
}

func (c *proposerTaskGeneric) storeProposalDuration() {
	c.factory.storeProposalDuration(time.Since(c.startTime))
}

func (c *proposerTaskGeneric) placeProposalIfRelevant(mdProposed *proposedMilestoneWithData) string {
	c.factory.proposal.mutex.Lock()
	defer c.factory.proposal.mutex.Unlock()

	//c.setTraceNAhead(1)
	c.trace("proposed %s: coverage: %s (base %s), numIN: %d, elapsed: %v",
		mdProposed.proposedBy, util.GoThousands(mdProposed.coverage), util.GoThousands(c.factory.proposal.bestSoFarCoverage),
		mdProposed.tx.NumInputs(), mdProposed.elapsed)

	if c.factory.proposal.targetTs == core.NilLogicalTime {
		return fmt.Sprintf("%s SKIPPED: target is nil", mdProposed.tx.IDShort())
	}

	// decide if it is not lagging behind the target
	if mdProposed.tx.Timestamp() != c.factory.proposal.targetTs {
		c.factory.log.Warnf("%s: proposed milestone (%s) is lagging behind target %s. Generation duration: %v",
			mdProposed.proposedBy, mdProposed.tx.Timestamp().String(), c.factory.proposal.targetTs.String(), mdProposed.elapsed)
		return fmt.Sprintf("%s SKIPPED: task is behind target", mdProposed.tx.IDShort())
	}

	if c.factory.proposal.current != nil && *c.factory.proposal.current.ID() == *mdProposed.tx.ID() {
		return fmt.Sprintf("%s SKIPPED: repeating", mdProposed.tx.IDShort())
	}

	baselineCoverage := c.factory.proposal.bestSoFarCoverage

	if !mdProposed.tx.IsBranchTransaction() {
		if mdProposed.coverage <= baselineCoverage {
			return fmt.Sprintf("%s SKIPPED: no increase in coverage %s <- %s)",
				mdProposed.tx.IDShort(), util.GoThousands(mdProposed.coverage), util.GoThousands(c.factory.proposal.bestSoFarCoverage))
		}
	}

	// branch proposals always accepted
	c.factory.proposal.bestSoFarCoverage = mdProposed.coverage
	c.factory.proposal.current = mdProposed.tx
	c.factory.proposal.currentExtended = mdProposed.extended

	//c.setTraceNAhead(1)
	c.trace("(%s): ACCEPTED %s, coverage: %s (base: %s), elapsed: %v, inputs: %d, tipPool: %d",
		mdProposed.proposedBy,
		mdProposed.tx.IDShort(),
		util.GoThousands(mdProposed.coverage),
		util.GoThousands(baselineCoverage),
		mdProposed.elapsed,
		mdProposed.tx.NumInputs(),
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
