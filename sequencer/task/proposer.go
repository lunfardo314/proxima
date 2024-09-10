package task

import (
	"errors"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer/commands"
	"github.com/lunfardo314/proxima/util"
)

func (p *Proposer) run() {
	defer p.proposersWG.Done()

	var a *attacher.IncrementalAttacher
	var forceExit bool
	var err error

	const loopDelay = 10 * time.Millisecond
	waitExit := func() bool {
		select {
		case <-p.ctx.Done():
			return true
		case <-time.After(loopDelay):
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
		if a == nil || !a.Completed() {
			if waitExit() {
				// leave if its time
				return
			}
			// attempt may be no luck. Keep trying if it is not the end yet
			continue
		}
		// attacher has been created and it is complete. Propose it
		p.Assertf(a.IsCoverageAdjusted(), "coverage must be adjusted")
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

func (p *Proposer) propose(a *attacher.IncrementalAttacher) error {
	util.Assertf(a.TargetTs() == p.targetTs, "a.targetTs() == p.task.targetTs")

	tx, err := p.makeTxProposal(a)
	util.Assertf(a.IsClosed(), "a.IsClosed()")

	if err != nil {
		return err
	}
	coverage := a.AccumulatedCoverage()
	_proposal := &proposal{
		tx: tx,
		txMetadata: &txmetadata.TransactionMetadata{
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
			LedgerCoverage:          util.Ref(coverage),
		},
		extended:     a.Extending(),
		endorsing:    a.Endorsing(),
		coverage:     coverage,
		attacherName: a.Name(),
		strategyName: p.strategy.ShortName,
	}

	if p.targetTs.IsSlotBoundary() {
		_proposal.txMetadata.LedgerCoverage = util.Ref(coverage)
		_proposal.txMetadata.Supply = util.Ref(a.FinalSupply())
		_proposal.txMetadata.SlotInflation = util.Ref(a.SlotInflation())
	}
	p.proposalChan <- _proposal
	return nil
}

func (p *Proposer) makeTxProposal(a *attacher.IncrementalAttacher) (*transaction.Transaction, error) {
	cmdParser := commands.NewCommandParser(ledger.AddressED25519FromPrivateKey(p.ControllerPrivateKey()))
	nm := p.strategy.ShortName + "." + p.environment.SequencerName()
	tx, err := a.MakeSequencerTransaction(nm, p.ControllerPrivateKey(), cmdParser)
	// attacher and references not needed anymore, should be released
	a.Close()
	return tx, err
}

func (p *Proposer) ChooseExtendEndorsePair() *attacher.IncrementalAttacher {
	p.Assertf(!p.targetTs.IsSlotBoundary(), "!p.targetTs.IsSlotBoundary()")
	endorseCandidates := p.Backlog().CandidatesToEndorseSorted(p.targetTs)

	seqID := p.SequencerID()
	var ret *attacher.IncrementalAttacher
	for _, endorse := range endorseCandidates {
		select {
		case <-p.ctx.Done():
			return nil
		default:
		}
		if !ledger.ValidTransactionPace(endorse.Timestamp(), p.targetTs) {
			// cannot endorse candidate because of ledger time constraint
			p.Tracef(TraceTagTask, ">>>>>>>>>>>>>>> !ledger.ValidTransactionPace")
			continue
		}
		rdr := multistate.MakeSugared(p.GetStateReaderForTheBranch(&endorse.BaselineBranch().ID))
		seqOut, err := rdr.GetChainOutput(&seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			p.Tracef(TraceTagTask, ">>>>>>>>>>>>>>> GetChainOutput not found")
			continue
		}
		p.AssertNoError(err)
		extendRoot := attacher.AttachOutputID(seqOut.ID, p.Task)

		p.AddOwnMilestone(extendRoot.VID) // to ensure it is in the pool of own milestones
		futureConeMilestones := p.FutureConeOwnMilestonesOrdered(extendRoot, p.targetTs)

		p.Tracef(TraceTagTask, ">>>>>>>>>>>>>>> check endorsement candidate %s against future cone of extension candidates {%s}",
			endorse.IDShortString, func() string { return vertex.WrappedOutputsShortLines(futureConeMilestones).Join(", ") })

		if ret = p.chooseEndorseExtendPairAttacher(endorse, futureConeMilestones); ret != nil {
			p.Tracef(TraceTagTask, ">>>>>>>>>>>>>>> chooseEndorseExtendPairAttacher return %s", ret.Name)
			return ret
		}
	}
	p.Tracef(TraceTagTask, ">>>>>>>>>>>>>>> chooseEndorseExtendPairAttacher nil")
	return nil
}

// ChooseEndorseExtendPairAttacher traverses all known extension options and check each of it with the endorsement target
// Returns consistent incremental attacher with the biggest ledger coverage
func (p *Proposer) chooseEndorseExtendPairAttacher(endorse *vertex.WrappedTx, extendCandidates []vertex.WrappedOutput) *attacher.IncrementalAttacher {
	var ret, a *attacher.IncrementalAttacher
	var err error
	for _, extend := range extendCandidates {
		a, err = attacher.NewIncrementalAttacher(p.Name, p, p.targetTs, extend, endorse)
		if err != nil {
			p.Tracef(TraceTagTask, "%s can't extend %s and endorse %s: %v", p.targetTs.String, extend.IDShortString, endorse.IDShortString, err)
			continue
		}
		// we must carefully dispose unused references, otherwise pruning does not work
		// we dispose all attachers with their references, except the one with the biggest coverage
		switch {
		case !a.Completed():
			a.Close()
		case ret == nil:
			ret = a
		case a.AccumulatedCoverage() > ret.AccumulatedCoverage():
			ret.Close()
			ret = a
		default:
			a.Close()
		}
	}
	return ret
}
