package proposer_generic

import (
	"context"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		CurrentTargetTs() ledger.Time
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		Propose(a *attacher.IncrementalAttacher) bool
		AttachTagAlongInputs(a *attacher.IncrementalAttacher) int
		ChooseExtendEndorsePair(proposerName string, targetTs ledger.Time) *attacher.IncrementalAttacher
		BestMilestoneInTheSlot(slot ledger.Slot) *vertex.WrappedTx
		SequencerName() string
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
		Constructor TaskConstructor
	}
)

const TraceTag = "propose-generic"

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

	for {
		if a, forceExit = t.generateProposal(); forceExit {
			return
		}
		if a != nil {
			func() {
				if a.Completed() {
					t.Tracef(TraceTag, "Run: generated new proposal")
					if forceExit = t.Propose(a); forceExit {
						return
					}
				}
			}()
		}
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
		if t.CurrentTargetTs() != t.TargetTs {
			return
		}
	}
}
