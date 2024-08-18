package task

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
)

// Task to generate proposals for the target ledger time. The task is interrupted
// by the context with deadline
type (
	Environment interface {
		global.NodeGlobal
		attacher.Environment
		backlog.Environment
		BestCoverageInTheSlot(targetTs ledger.Time) uint64
		ControllerPrivateKey() ed25519.PrivateKey
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		AttachTagAlongInputs(a *attacher.IncrementalAttacher) int
		Backlog() *backlog.InputBacklog
		AddOwnMilestone(vid *vertex.WrappedTx)
		FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs ledger.Time) []vertex.WrappedOutput
	}

	Task struct {
		Environment
		targetTs     ledger.Time
		ctx          context.Context
		proposersWG  sync.WaitGroup
		proposalChan chan *proposal
		proposals    []*proposal
	}

	proposal struct {
		tx           *transaction.Transaction
		txMetadata   *txmetadata.TransactionMetadata
		extended     vertex.WrappedOutput
		coverage     uint64
		attacherName string
	}

	Proposer struct {
		*Task
		strategy        *Strategy
		alreadyProposed set.Set[[32]byte]
	}

	// ProposalGenerator returns incremental attacher as draft transaction or
	// otherwise nil and forceExit flag = true
	ProposalGenerator func(p *Proposer) (*attacher.IncrementalAttacher, bool)

	Strategy struct {
		Name             string
		ShortName        string
		GenerateProposal ProposalGenerator
	}
)

const TraceTagTask = "task"

var _allProposingStrategies = make(map[string]*Strategy)

var ErrNoProposals = errors.New("no proposals was generated")

func registerProposerStrategy(s *Strategy) {
	_allProposingStrategies[s.Name] = s
}

func init() {
	//registerProposerStrategy(proposer_base.Strategy())
	//registerProposerStrategy(proposer_endorse1.Strategy())
	//registerProposerStrategy(proposer_endorse2.Strategy())
}

func allProposingStrategies() []*Strategy {
	ret := make([]*Strategy, 0)
	for _, s := range _allProposingStrategies {
		if !viper.GetBool("sequencers.disable_proposer." + s.ShortName) {
			ret = append(ret, s)
		}
	}
	return ret
}

// Run starts task with the aim to generate sequencer transaction for the target ledger time.
// The proposer task consist of several proposers (goroutines)
// Each proposer generates proposals and writes it to the channel of the task.
// The best proposal is selected and returned. Function only returns transaction which is better
// than others in the tippool for the current slot. Otherwise, returns nil
func Run(env Environment, targetTs ledger.Time) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	deadline := targetTs.Time()
	nowis := time.Now()
	env.Tracef(TraceTagTask, "RunTask: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		return nil, nil, fmt.Errorf("target %s is in the past by %v: impossible to generate milestone",
			targetTs.String(), nowis.Sub(deadline))
	}

	task := &Task{
		Environment:  env,
		targetTs:     targetTs,
		ctx:          nil,
		proposalChan: make(chan *proposal),
		proposals:    make([]*proposal, 0),
	}

	// start proposers
	var cancel func()
	task.ctx, cancel = context.WithDeadline(env.Ctx(), deadline)
	defer cancel() // to prevent context leak

	// starts one goroutine for each known strategy
	task.startProposers()

	// reads all proposals from proposers into the slice
	// stops reading when all goroutines exit
	// Proposer will always exist because of deadline or because of global cancel
	go func() {
		for p := range task.proposalChan {
			task.proposals = append(task.proposals, p)
		}
	}()

	task.proposersWG.Wait()
	close(task.proposalChan)

	// will return nil if wasn't able to generate transaction
	return task.getBestProposal()
}

func (t *Task) getBestProposal() (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	bestCoverageInSlot := t.BestCoverageInTheSlot(t.targetTs)
	t.Tracef(TraceTagTask, "best coverage in slot: %s", func() string { return util.Th(bestCoverageInSlot) })
	maxIdx := -1
	for i := range t.proposals {
		c := t.proposals[i].coverage
		if c > bestCoverageInSlot {
			bestCoverageInSlot = c
			maxIdx = i
		}
	}
	if maxIdx < 0 {
		t.Tracef(TraceTagTask, "getBestProposal: NONE, target: %s", t.targetTs.String)
		return nil, nil, ErrNoProposals
	}
	p := t.proposals[maxIdx]
	t.Tracef(TraceTagTask, "getBestProposal: %s, target: %s, attacher %s: coverage %s",
		p.tx.IDShortString, t.targetTs.String, p.attacherName, func() string { return util.Th(p.coverage) })

	return p.tx, p.txMetadata, nil
}

func (t *Task) startProposers() {
	for _, s := range allProposingStrategies() {
		p := &Proposer{
			Task:            t,
			strategy:        s,
			alreadyProposed: set.New[[32]byte](),
		}
		go p.Run()
	}
}

func (t *Task) Name() string {
	return fmt.Sprintf("[%s]", t.targetTs.String())
}
