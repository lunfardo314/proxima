package factory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
)

// Task to generate proposals for the target ledger time. The task is interrupted
// by the context with deadline
type Task struct {
	*MilestoneFactory
	TargetTs     ledger.Time
	Ctx          context.Context
	ProposersWG  sync.WaitGroup
	ProposalChan chan *proposal
	proposals    []*proposal
}

func (mf *MilestoneFactory) StartTask(targetTs ledger.Time) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	deadline := targetTs.Time()
	nowis := time.Now()
	mf.Tracef(TraceTag, "StartTask: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		return nil, nil, fmt.Errorf("target %s is in the past by %v: impossible to generate milestone",
			targetTs.String(), nowis.Sub(deadline))
	}

	task := &Task{
		MilestoneFactory: mf,
		TargetTs:         targetTs,
		Ctx:              nil,
		ProposalChan:     make(chan *proposal),
		proposals:        make([]*proposal, 0),
	}

	// start worker(s)
	var cancel func()
	task.Ctx, cancel = context.WithDeadline(mf.Ctx(), deadline)
	defer cancel() // to prevent context leak
	task.startProposerWorkers()

	go func() {
		for p := range task.ProposalChan {
			task.proposals = append(task.proposals, p)
		}
	}()

	task.ProposersWG.Wait()
	close(task.ProposalChan)

	return task.getBestProposal() // will return nil if wasn't able to generate transaction
}

func (t *Task) getBestProposal() (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	bestCoverageInSlot := t.BestCoverageInTheSlot(t.TargetTs)
	t.Tracef(TraceTag, "best coverage in slot: %s", func() string { return util.Th(bestCoverageInSlot) })
	maxIdx := -1
	for i := range t.proposals {
		c := t.proposals[i].coverage
		if c > bestCoverageInSlot {
			bestCoverageInSlot = c
			maxIdx = i
		}
	}
	if maxIdx < 0 {
		t.Tracef(TraceTag, "getBestProposal: NONE, target: %s", t.TargetTs.String)
		return nil, nil, ErrNoProposals
	}
	p := t.proposals[maxIdx]
	t.Tracef(TraceTag, "getBestProposal: %s, target: %s, attacher %s: coverage %s",
		p.tx.IDShortString, t.TargetTs.String, p.attacherName, func() string { return util.Th(p.coverage) })

	return p.tx, p.txMetadata, nil
}

func (t *Task) startProposerWorkers() {
	for _, s := range allProposingStrategies() {
		t.runProposer(s)
	}
}

func (t *Task) runProposer(s *proposer_generic.Strategy) {

}
