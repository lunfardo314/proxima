package proposer_generic

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		ControllerPrivateKey() ed25519.PrivateKey
		CurrentTargetTs() ledger.Time
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		AttachTagAlongInputs(a *attacher.IncrementalAttacher) int
		ChooseExtendEndorsePair(proposerName string, targetTs ledger.Time) *attacher.IncrementalAttacher
		BestCoverageInTheSlot(targetTs ledger.Time) uint64
		SequencerName() string
		Propose(a *attacher.IncrementalAttacher, strategyName string) error
		Backlog() *backlog.InputBacklog
	}

	Task interface {
		Run()
		GetName() string
	}

	TaskGeneric struct {
		Environment
		Name             string
		ctx              context.Context
		Strategy         *Strategy
		TargetTs         ledger.Time
		alreadyProposed  set.Set[[32]byte]
		generateProposal func() (*attacher.IncrementalAttacher, bool)
	}

	TaskConstructor func(generic *TaskGeneric) Task

	Strategy struct {
		Name        string
		ShortName   string
		Constructor TaskConstructor
	}
)

const (
	TraceTag = "propose-generic"
)

func New(env Environment, strategy *Strategy, targetTs ledger.Time, ctx context.Context) Task {
	return strategy.Constructor(&TaskGeneric{
		Name:            fmt.Sprintf("[%s-%s-%s]", env.SequencerName(), strategy.Name, targetTs.String()),
		ctx:             ctx,
		Environment:     env,
		Strategy:        strategy,
		TargetTs:        targetTs,
		alreadyProposed: make(set.Set[[32]byte]),
		generateProposal: func() (*attacher.IncrementalAttacher, bool) {
			panic("not implemented")
		},
	})
}

func (t *TaskGeneric) WithProposalGenerator(fun func() (*attacher.IncrementalAttacher, bool)) {
	t.generateProposal = fun
}

func (t *TaskGeneric) GetName() string {
	return t.Name
}

func (t *TaskGeneric) Run() {
	var a *attacher.IncrementalAttacher
	var forceExit bool
	var err error

	const loopDelay = 10 * time.Millisecond
	waitExit := func() bool {
		select {
		case <-t.ctx.Done():
			return true
		case <-time.After(loopDelay):
		}
		if t.CurrentTargetTs() != t.TargetTs {
			return true
		}
		return false
	}

	for {
		if a, forceExit = t.generateProposal(); forceExit {
			return
		}
		if a == nil || !a.Completed() {
			if waitExit() {
				return
			}
			continue
		}
		t.Assertf(a.IsCoverageAdjusted(), "coverage must be adjusted")
		if err = t.Propose(a, t.Strategy.ShortName); err != nil {
			t.Tracef(TraceTag, "Run: %v", err)
			return
		}
		if waitExit() {
			return
		}
	}
}

func (t *TaskGeneric) waitExit(d time.Duration) bool {
	select {
	case <-t.ctx.Done():
		return true
	case <-time.After(d):
	}
	if t.CurrentTargetTs() != t.TargetTs {
		return true
	}
	return false
}
