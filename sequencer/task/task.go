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
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
)

// Task to generate proposals for the target ledger time. The task is interrupted
// by the context with deadline
type (
	environment interface {
		global.NodeGlobal
		attacher.Environment
		SequencerName() string
		SequencerID() ledger.ChainID
		ControllerPrivateKey() ed25519.PrivateKey
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		Backlog() *backlog.InputBacklog
		IsConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool
		AddOwnMilestone(vid *vertex.WrappedTx)
		FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs ledger.Time) []vertex.WrappedOutput
		MaxTagAlongInputs() int
		LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
	}

	Task struct {
		environment
		targetTs     ledger.Time
		ctx          context.Context
		proposersWG  sync.WaitGroup
		proposalChan chan *proposal
		// proposals    []*proposal
		Name string
	}

	proposal struct {
		tx           *transaction.Transaction
		txMetadata   *txmetadata.TransactionMetadata
		extended     vertex.WrappedOutput
		coverage     uint64
		attacherName string
		strategyName string
	}

	Proposer struct {
		*Task
		strategy *Strategy
		Name     string
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

var (
	_allProposingStrategies = make(map[string]*Strategy)
	ErrNoProposals          = errors.New("no proposals was generated")
)

func registerProposerStrategy(s *Strategy) {
	_allProposingStrategies[s.Name] = s
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
func Run(env environment, targetTs ledger.Time) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	deadline := targetTs.Time()
	nowis := time.Now()
	env.Tracef(TraceTagTask, "RunTask: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		return nil, nil, fmt.Errorf("target %s is in the past by %v: impossible to generate milestone",
			targetTs.String(), nowis.Sub(deadline))
	}

	task := &Task{
		environment:  env,
		targetTs:     targetTs,
		ctx:          nil,
		proposalChan: make(chan *proposal),
		// proposals:    make([]*proposal, 0),
		Name: fmt.Sprintf("%s[%s]", env.SequencerName(), targetTs.String()),
	}

	// start proposers
	var cancel func()
	task.ctx, cancel = context.WithDeadline(env.Ctx(), deadline)
	defer cancel() // to prevent context leak

	// starts one goroutine for each known strategy
	task.startProposers()

	// reads all proposals from proposers into the slice
	// stops reading when all goroutines exit

	// channel is needed to make sure reading loop has ended
	readStop := make(chan struct{})

	proposals := make([]*proposal, 0)

	go func() {
		for p := range task.proposalChan {
			proposals = append(proposals, p)
		}
		close(readStop)
	}()

	task.proposersWG.Wait()
	close(task.proposalChan)
	<-readStop

	if len(proposals) == 0 {
		return nil, nil, ErrNoProposals
	}

	best := util.Maximum(proposals, func(p1, p2 *proposal) bool {
		return p1.coverage < p2.coverage
	})

	ownLatest := env.OwnLatestMilestoneOutput().VID

	if !targetTs.IsSlotBoundary() && best.coverage <= ownLatest.GetLedgerCoverage() {
		return nil, nil, ErrNoProposals
	}

	if ownLatest.IsBranchTransaction() {
		env.Log().Infof(">>>>>>>>>>>> task %s selected from: {%s}",
			task.Name, lines.SliceToLines(proposals).Join(", "))
	}

	return best.tx, best.txMetadata, nil
	// will return nil if wasn't able to generate transaction
	//return task.chooseTheBestProposal()
}

func (t *Task) bestCoverageInTheSlot(slot ledger.Slot) uint64 {
	all := t.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == slot
	})
	if len(all) == 0 {
		return 0
	}
	return all[0].GetLedgerCoverage()
}

func (p *proposal) String() string {
	return fmt.Sprintf("%s[%s -- %s]", p.strategyName, p.extended.IDShortString(), util.Th(p.coverage))
}

// chooseTheBestProposal may return nil if no suitable proposal was generated
//func (t *Task) chooseTheBestProposal() (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
//	pMax := util.Maximum(t.proposals, func(p1, p2 *proposal) bool {
//		return p1.coverage < p2.coverage
//	})
//	if pMax == nil {
//		return nil, nil, ErrNoProposals
//	}
//	best := t.bestCoverageInTheSlot(t.targetTs.Slot())
//	if !t.targetTs.IsSlotBoundary() {
//		// not branch
//		if pMax.coverage > best {
//			return pMax.tx, pMax.txMetadata, nil
//		}
//		// there are better, makes no sense to issue a new one with smaller coverage
//		return nil, nil, nil
//	}
//	// branch is issued if it is healthy and the biggest in the slot, OR
//	// it is not healthy but previous slot has no branches. The latter is needed for bootstrap when
//	// it is just starting and no branches are issued in the network yet
//	if global.IsHealthyCoverage(*pMax.txMetadata.LedgerCoverage, *pMax.txMetadata.Supply, global.FractionHealthyBranch) {
//		if pMax.coverage > best {
//			return pMax.tx, pMax.txMetadata, nil
//		}
//	} else {
//		// not healthy is issued only when previous slot has no branches
//		if t.bestCoverageInTheSlot(t.targetTs.Slot()-1) == 0 {
//			return pMax.tx, pMax.txMetadata, nil
//		}
//	}
//	return nil, nil, nil
//}

func (t *Task) startProposers() {
	for _, s := range allProposingStrategies() {
		p := &Proposer{
			Task:     t,
			strategy: s,
			Name:     t.Name + "-" + s.Name,
		}
		t.proposersWG.Add(1)
		go p.run()
	}
}

// InsertTagAlongInputs includes tag-along outputs from the backlog into attacher
func (t *Task) InsertTagAlongInputs(a *attacher.IncrementalAttacher) (numInserted int) {
	t.Tracef(TraceTagTask, "InsertTagAlongInputs: %s", a.Name)

	if ledger.L().ID.IsPreBranchConsolidationTimestamp(a.TargetTs()) {
		// skipping tagging-along in pre-branch consolidation zone
		t.Tracef(TraceTagTask, "InsertTagAlongInputs: %s. No tag-along in the pre-branch consolidation zone of ticks", a.Name())
		return 0
	}

	preSelected := t.Backlog().FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidSequencerPace(wOut.Timestamp(), a.TargetTs()) {
			t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs:#%d  not valid pace -> not pre-selected (target %s)", wOut.Index, a.TargetTs().String)
			return false
		}
		// fast filtering out already consumed outputs in the predecessor milestone context
		already := t.IsConsumedInThePastPath(wOut, a.Extending().VID)
		if already {
			t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs: #%d already consumed in the past path -> not pre-selected", wOut.Index)
		}
		return !already
	})
	t.Tracef(TraceTagTask, "InsertTagAlongInputs %s. Pre-selected: %d", a.Name, len(preSelected))

	for _, wOut := range preSelected {
		select {
		case <-t.ctx.Done():
			return
		default:
		}
		t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs: pre-selected #%d", wOut.Index)
		if success, err := a.InsertTagAlongInput(wOut); success {
			numInserted++
			t.Tracef(TraceTagTask, "InsertTagAlongInputs %s. Inserted %s", a.Name, wOut.IDShortString)
			t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs %s. Inserted #%d", a.Name, wOut.Index)
		} else {
			t.Tracef(TraceTagTask, "InsertTagAlongInputs %s. Failed to insert %s: '%v'", a.Name, wOut.IDShortString, err)
			t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs %s. Failed to insert #%d: '%v'", a.Name, wOut.Index, err)
		}
		if a.NumInputs() >= t.MaxTagAlongInputs() {
			return
		}
	}
	return
}
