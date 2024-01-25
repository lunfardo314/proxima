package proposer_generic

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		CurrentTargetTs() ledger.LogicalTime
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		Propose(a *attacher.IncrementalAttacher) bool
		AttachTagAlongInputs(a *attacher.IncrementalAttacher) int
		ChooseExtendEndorsePair(proposerName string, targetTs ledger.LogicalTime) *attacher.IncrementalAttacher
		HeaviestBranchInTheSlot(slot ledger.Slot) *vertex.WrappedTx
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
		TargetTs         ledger.LogicalTime
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

func New(env Environment, strategy *Strategy, targetTs ledger.LogicalTime, ctx context.Context) Task {
	return strategy.Constructor(&TaskGeneric{
		Name:            fmt.Sprintf("[%s-%s]", strategy.Name, targetTs.String()),
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
		if a != nil && a.Completed() {
			t.Tracef(TraceTag, "Run: generated new proposal")
			if forceExit = t.Propose(a); forceExit {
				return
			}
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

func outputSliceString(path []vertex.WrappedOutput) string {
	ret := make([]string, 0)
	for _, wOut := range path {
		ret = append(ret, "       "+wOut.IDShortString())
	}
	return strings.Join(ret, "\n")
}
