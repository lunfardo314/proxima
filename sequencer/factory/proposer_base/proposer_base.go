package proposer_base

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const BaseProposerName = "base"

type BaseProposer struct {
	proposer.TaskGeneric
}

func Strategy() *proposer.Strategy {
	return &proposer.Strategy{
		Name: BaseProposerName,
		Constructor: func(generic *proposer.TaskGeneric) proposer.Task {
			return &BaseProposer{TaskGeneric: *generic}
		},
	}
}

func (b *BaseProposer) Run() {
	startTime := time.Now()
	var tx *transaction.Transaction
	var forceExit bool
	for b.ContinueCandidateProposing(b.TargetTs) {
		latestMs := b.GetLatestOwnMilestone()

		b.RunAndIgnoreDeletedVerticesException(func() {
			if tx, forceExit = b.proposeBase(latestMs); forceExit {
				return
			}
			if tx != nil {
				b.trace("generated %s", func() any { return tx.IDShortString() })
				if forceExit = b.assessAndAcceptProposal(tx, latestMs, startTime, b.name()); forceExit {
					return
				}
			}
		})
		if forceExit {
			break
		}
		b.storeProposalDuration()
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *BaseProposer) proposeBase(extend vertex.WrappedOutput) (*attacher.IncrementalAttacher, bool) {
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		if extend.TimeSlot() != b.TargetTs.Slot() {
			// on startup or cross-slot will only produce branches
			b.Tracef("proposer", "proposeBase.force exit: cross-slot %s", extend.IDShortString)
			return nil, true
		}
		if !extend.VID.IsSequencerMilestone() {
			// not cross-slot, but predecessor must be sequencer tx
			b.Env.Tracef("proposer", "proposeBase.force exit: not-sequencer %s", extend.IDShortString)
			return nil, true
		}
	}

	if !b.TargetTs.IsSlotBoundary() && extend.TimeSlot() != b.TargetTs.Slot() {
		// on startup or cross-slot will only produce branches
		b.Env.Tracef("proposer", "proposeBase.force exit: cross-slot %s", extend.IDShortString)
		return nil, true
	}

	if b.TargetTs.Tick() == 0 {
		b.Env.Tracef("proposer", "making branch, extending %s", extend.IDShortString)
		// generate branch, no fee outputs are consumed
		attacher.NewIncrementalAttacher(b.Name(), b.TargetTs, extend)

		baseStem := extend.VID.BaseStemOutput(b.factory.utangle)
		if baseStem == nil {
			// base stem is not available for a milestone which is virtual and non-branch
			b.factory.log.Warnf("proposeBase.force exit: stem not available, %s cannot be extended to a branch", extend.IDShort())
			return nil, true
		}
		// create branch
		return b.makeMilestone(&extend, baseStem, nil, nil), false
	}
	// non-branch

	//b.setTraceNAhead(1)
	b.trace("non-branch")

	feeOutputsToConsume, conflict := b.selectInputs(extend)
	util.Assertf(conflict == nil, "unexpected conflict")

	b.trace("making ordinary milestone")
	return b.makeMilestone(&extend, nil, feeOutputsToConsume, nil), false
}
