package task

import (
	"errors"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
)

const TraceTagRunProposer = "runProposer"

func (p *proposer) run() {
	p.Tracef(TraceTagRunProposer, "START proposer %s, targetTs: %s", p.strategy.Name, p.targetTs.String())
	defer p.Tracef(TraceTagRunProposer, "END proposer %s, targetTs: %s", p.strategy.Name, p.targetTs.String())

	var a *proposal
	var forceExit bool
	var err error

	const loopDelay = 10 * time.Millisecond
	delayTimer := time.NewTimer(loopDelay)
	defer delayTimer.Stop()

	waitExit := func() bool {
		if !delayTimer.Stop() {
			select {
			case <-delayTimer.C:
			default:
			}
		}
		delayTimer.Reset(loopDelay)
		select {
		case <-p.ctx.Done():
			return true
		case <-delayTimer.C:
		}
		return false
	}
	// closing incremental attacher releases all referenced vertices.
	// it is necessary for correct purging of memDAG vertices, otherwise
	// it leaks vertices. Close nil is ok
	defer a.Close()

	for {
		a.Close()

		a, forceExit = p.strategy.GenerateProposal(p)

		//if a != nil {
		//	trackIncAttachers.RegisterPointer(a)
		//}

		if a == nil && forceExit {
			return
		}
		if a == nil || !a.Completed() {
			if waitExit() {
				// leave if it's time
				return
			}
			// attempt may be no luck. Keep trying if it is not the end yet
			continue
		}

		// attacher has been created and it is complete. Propose it
		p.Assertf(!a.IsClosed(), "%s is closed", a.IncrementalAttacher.Name)
		if err = p.propose(a); err != nil {
			p.Log().Warnf("%v", err)
			return
		}
		if forceExit {
			return
		}
		if waitExit() {
			return
		}
	}
}

func (p *proposer) propose(a *proposal) error {
	coverageDelta, frozen := a.CoverageDelta()
	ledgerCoverage := a.FinalLedgerCoverage(p.targetTs, coverageDelta)
	slotInflation := a.SlotInflation() // tip inflation is not included
	baselineSupply := a.BaselineSupply()

	tx, hrString, err := a.makeTx() // << after this call attacher is closed
	if err != nil {
		return err
	}

	slotInflation += tx.InflationAmount() // include tip inflation
	supply := baselineSupply + slotInflation

	var frozenP *uint64
	if frozen > 0 {
		frozenP = util.Ref(frozen)
	}
	_proposal := &finalProposal{
		tx:     tx,
		txSize: len(tx.Bytes()),
		txMetadata: &txmetadata.TransactionMetadata{
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
			CoverageDelta:           util.Ref(coverageDelta),
			FrozenCoverage:          frozenP,
			LedgerCoverage:          util.Ref(ledgerCoverage),
		},
		hrString:          hrString,
		coverageDelta:     coverageDelta,
		ledgerCoverage:    ledgerCoverage,
		inflation:         tx.InflationAmount(),
		attacherName:      a.IncrementalAttacher.Name(),
		strategyShortName: p.strategy.ShortName,
	}

	if tx.IsBranchTransaction() {
		_proposal.txMetadata.LedgerCoverage = util.Ref(ledgerCoverage) // not persistent
		_proposal.txMetadata.Supply = util.Ref(supply)
		_proposal.txMetadata.SlotInflation = util.Ref(slotInflation)
	}
	p.proposalChan <- _proposal
	return nil
}

const TraceTagChooseFirstExtendEndorsePair = "chooseFirstPair"

// ChooseFirstExtendEndorsePair returns incremental attacher which corresponds to the first
// extend-endorse pair encountered while traversing endorse candidates.
// Endorse candidates are either sorted descending by coverage, or randomly shuffled
// Pairs are filtered before checking. This way repeating pairs can be filtered out
func (p *proposer) ChooseFirstExtendEndorsePair(shuffleEndorseCandidates bool, pairFilter func(extend vertex.WrappedOutput, endorse *vertex.WrappedTx) bool) *attacher.IncrementalAttacher {
	p.Tracef(TraceTagChooseFirstExtendEndorsePair, "IN %s", p.Name)

	p.Assertf(!p.targetTs.IsSlotBoundary(), "!p.targetTs.IsSlotBoundary()")
	var endorseCandidates []*vertex.WrappedTx
	if shuffleEndorseCandidates {
		endorseCandidates = p.Backlog().CandidatesToEndorseShuffled(p.targetTs)
	} else {
		endorseCandidates = p.Backlog().CandidatesToEndorseSorted(p.targetTs)
	}
	p.Tracef(TraceTagChooseFirstExtendEndorsePair, "endorse candidates: %d -- %s", len(endorseCandidates), p.Name)

	seqID := p.SequencerID()
	var ret *attacher.IncrementalAttacher
	for _, endorse := range endorseCandidates {
		p.Tracef(TraceTagChooseFirstExtendEndorsePair, "check endorse candidate: %s -- %s", endorse.IDShortString, p.Name)

		select {
		case <-p.ctx.Done():
			return nil
		default:
		}

		if !ledger.ValidTransactionPace(endorse.Timestamp(), p.targetTs) {
			// cannot endorse the candidate because of ledger time constraint
			p.Tracef(TraceTagChooseFirstExtendEndorsePair, ">>>>>>>>>>>>>>> !ledger.ValidTransactionPace target %s -> endorse %s",
				endorse.Timestamp().String(), p.targetTs.String())
			continue
		}
		baselineBranchID, ok := endorse.BaselineBranch()
		p.Assertf(ok, "baselineBranchID not found in %s", endorse.IDShortString)

		seqOut, err := p.Branches().GetChainOutputFromBranch(baselineBranchID, seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			p.Tracef(TraceTagChooseFirstExtendEndorsePair, ">>>>>>>>>>>>>>> GetChainOutputWithID not found -- %s", p.Name)
			continue
		}
		p.AssertNoError(err)
		extendRoot := attacher.AttachOutputID(seqOut.ID, p.taskData)

		p.AddOwnMilestone(extendRoot.VID) // to ensure it is in the pool of own milestones
		futureConeMilestones := p.FutureConeOwnMilestonesOrdered(extendRoot, p.targetTs)

		p.Tracef(TraceTagChooseFirstExtendEndorsePair, ">>>>>>>>>>>>>>> check endorsement candidate %s against future cone of extension candidates {%s}",
			endorse.IDShortString, func() string { return vertex.WrappedOutputsShortLines(futureConeMilestones).Join(", ") })

		if ret = p.chooseEndorseExtendPairAttacher(endorse, futureConeMilestones, pairFilter); ret != nil {
			p.Tracef(TraceTagChooseFirstExtendEndorsePair, ">>>>>>>>>>>>>>> chooseEndorseExtendPairAttacher return %s", ret.Name)
			return ret
		}
	}
	p.Tracef(TraceTagChooseFirstExtendEndorsePair, ">>>>>>>>>>>>>>> chooseEndorseExtendPairAttacher nil")
	return nil
}

// chooseEndorseExtendPairAttacher traverses all known extension options and check each of it with the endorsement target
// Returns consistent incremental attacher with the biggest ledger coverage
func (p *proposer) chooseEndorseExtendPairAttacher(endorse *vertex.WrappedTx, extendCandidates []vertex.WrappedOutput, pairFilter func(extend vertex.WrappedOutput, endorse *vertex.WrappedTx) bool) *attacher.IncrementalAttacher {
	if pairFilter == nil {
		pairFilter = func(_ vertex.WrappedOutput, _ *vertex.WrappedTx) bool { return true }
	}
	var ret, a *attacher.IncrementalAttacher
	var err error
	for _, extend := range extendCandidates {
		if !pairFilter(extend, endorse) {
			p.Tracef(TraceTagChooseFirstExtendEndorsePair, "%s filtered out: extend %s, endorse %s: err=%v", p.targetTs.String, extend.IDStringShort, endorse.IDShortString, err)
			continue
		}
		p.Tracef(TraceTagChooseFirstExtendEndorsePair, "%s create incremental attacher: extend %s, endorse %s: err=%v",
			p.targetTs.String, extend.IDStringShort, endorse.IDShortString, err)
		a, err = attacher.NewIncrementalAttacher(p.Name, p, p.targetTs, extend, endorse)
		if err != nil {
			p.taskData.slotData.markCombinationChecked(false, extend, endorse)
			p.Tracef(TraceTagChooseFirstExtendEndorsePair, "%s can't extend %s and endorse %s: err=%v", p.targetTs.String, extend.IDStringShort, endorse.IDShortString, err)
			continue
		}
		// we must carefully dispose unused references, otherwise pruning does not work
		// we dispose all attachers with their references, except the one with the biggest coverage
		switch {
		case !a.Completed():
			a.Close()
		case ret == nil:
			ret = a
		case a.FinalLedgerCoverage(p.targetTs) > ret.FinalLedgerCoverage(p.targetTs):
			ret.Close()
			ret = a
		default:
			a.Close()
		}
		p.taskData.slotData.markCombinationChecked(true, extend, endorse)
	}
	return ret
}
