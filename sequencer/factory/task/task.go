package task

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/sequencer/factory/commands"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
)

// Task to generate proposals for the target ledger time. The task is interrupted
// by the context with deadline
type (
	Environment interface {
		global.NodeGlobal
		BestCoverageInTheSlot(targetTs ledger.Time) uint64
		ControllerPrivateKey() ed25519.PrivateKey
		SequencerName() string
	}

	Task struct {
		Environment
		TargetTs     ledger.Time
		Ctx          context.Context
		ProposersWG  sync.WaitGroup
		ProposalChan chan *proposal
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
	// nil and forceExit flag = true
	ProposalGenerator func() (*attacher.IncrementalAttacher, bool)

	Strategy struct {
		Name             string
		ShortName        string
		GenerateProposal ProposalGenerator
	}
)

var _allProposingStrategies = make(map[string]*Strategy)

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
	env.Tracef(factory.TraceTag, "RunTask: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		return nil, nil, fmt.Errorf("target %s is in the past by %v: impossible to generate milestone",
			targetTs.String(), nowis.Sub(deadline))
	}

	task := &Task{
		Environment:  env,
		TargetTs:     targetTs,
		Ctx:          nil,
		ProposalChan: make(chan *proposal),
		proposals:    make([]*proposal, 0),
	}

	// start proposers
	var cancel func()
	task.Ctx, cancel = context.WithDeadline(env.Ctx(), deadline)
	defer cancel() // to prevent context leak

	// starts one goroutine for each known strategy
	task.startProposers()

	// reads all proposals from proposers into the slice
	// stops reading when all goroutines exit
	// Proposer will always exist because of deadline or because of global cancel
	go func() {
		for p := range task.ProposalChan {
			task.proposals = append(task.proposals, p)
		}
	}()

	task.ProposersWG.Wait()
	close(task.ProposalChan)

	// will return nil if wasn't able to generate transaction
	return task.getBestProposal()
}

func (t *Task) getBestProposal() (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	bestCoverageInSlot := t.BestCoverageInTheSlot(t.TargetTs)
	t.Tracef(factory.TraceTag, "best coverage in slot: %s", func() string { return util.Th(bestCoverageInSlot) })
	maxIdx := -1
	for i := range t.proposals {
		c := t.proposals[i].coverage
		if c > bestCoverageInSlot {
			bestCoverageInSlot = c
			maxIdx = i
		}
	}
	if maxIdx < 0 {
		t.Tracef(factory.TraceTag, "getBestProposal: NONE, target: %s", t.TargetTs.String)
		return nil, nil, factory.ErrNoProposals
	}
	p := t.proposals[maxIdx]
	t.Tracef(factory.TraceTag, "getBestProposal: %s, target: %s, attacher %s: coverage %s",
		p.tx.IDShortString, t.TargetTs.String, p.attacherName, func() string { return util.Th(p.coverage) })

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

func (p *Proposer) Run() {
	defer p.ProposersWG.Done()

	var a *attacher.IncrementalAttacher
	var forceExit bool
	var err error

	const loopDelay = 10 * time.Millisecond
	waitExit := func() bool {
		select {
		case <-p.Ctx.Done():
			return true
		case <-time.After(loopDelay):
		}
		return false
	}
	defer a.Close()

	for {
		// closing incremental attacher releases all referenced vertices.
		// it is necessary for correct purging of memDAG vertices, otherwise
		// it leaks vertices
		a.Close()

		if a, forceExit = p.strategy.GenerateProposal(); forceExit {
			return
		}
		if a == nil || !a.Completed() {
			if waitExit() {
				return
			}
			continue
		}
		p.Assertf(a.IsCoverageAdjusted(), "coverage must be adjusted")
		if err = p.propose(a); err != nil {
			p.Log().Warnf("%v", err)
			return
		}
		if waitExit() {
			return
		}
	}
}

func (p *Proposer) propose(a *attacher.IncrementalAttacher) error {
	util.Assertf(a.TargetTs() == p.TargetTs, "a.TargetTs() == p.task.TargetTs")

	tx, err := p.makeTxProposal(a)
	util.Assertf(a.IsClosed(), "a.IsClosed()")

	if err != nil {
		return err
	}
	coverage := a.LedgerCoverage()
	p.ProposalChan <- &proposal{
		tx: tx,
		txMetadata: &txmetadata.TransactionMetadata{
			StateRoot:               nil,
			LedgerCoverage:          util.Ref(coverage),
			SlotInflation:           util.Ref(a.SlotInflation()),
			IsResponseToPull:        false,
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
		},
		extended:     a.Extending(),
		coverage:     coverage,
		attacherName: a.Name(),
	}
	return nil
}

func (p *Proposer) makeTxProposal(a *attacher.IncrementalAttacher) (*transaction.Transaction, error) {
	cmdParser := commands.NewCommandParser(ledger.AddressED25519FromPrivateKey(p.ControllerPrivateKey()))
	nm := p.strategy.ShortName + "." + p.Environment.SequencerName()
	tx, err := a.MakeSequencerTransaction(nm, p.ControllerPrivateKey(), cmdParser)
	// attacher and references not needed anymore, should be released
	a.Close()
	return tx, err
}
