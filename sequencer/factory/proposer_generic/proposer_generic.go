package proposer_generic

import (
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		ContinueCandidateProposing(ts ledger.LogicalTime) bool
		OwnLatestMilestone() *vertex.WrappedTx
		Propose(a *attacher.IncrementalAttacher) bool
		AttachTagAlongInputs(a *attacher.IncrementalAttacher)
		ChooseExtendEndorsePair(proposerName string, targetTs ledger.LogicalTime) *attacher.IncrementalAttacher
	}

	Task interface {
		Run()
		GetName() string
	}

	TaskGeneric struct {
		Environment
		Name            string
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
		Name:            fmt.Sprintf("[%s-%s]", strategy.Name, targetTs.String()),
		Environment:     env,
		Strategy:        strategy,
		TargetTs:        targetTs,
		alreadyProposed: make(set.Set[[32]byte]),
	})
}

func outputSliceString(path []vertex.WrappedOutput) string {
	ret := make([]string, 0)
	for _, wOut := range path {
		ret = append(ret, "       "+wOut.IDShortString())
	}
	return strings.Join(ret, "\n")
}

func (t *TaskGeneric) GetName() string {
	return t.Name
}

func (t *TaskGeneric) TraceLocal(format string, args ...any) {
	t.Environment.Tracef("proposer", t.Name+": "+format, args...)
}
