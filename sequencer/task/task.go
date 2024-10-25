package task

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
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
	"github.com/spf13/viper"
	"golang.org/x/exp/maps"
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
		EvidenceProposal(strategyShortName string)
		EvidenceBestProposalForTheTarget(strategyShortName string)
	}

	Task struct {
		environment
		targetTs     ledger.Time
		ctx          context.Context
		proposersWG  sync.WaitGroup
		proposalChan chan *proposal
		slotData     *SlotData
		// proposals    []*proposal
		Name string
	}

	proposal struct {
		tx                *transaction.Transaction
		txMetadata        *txmetadata.TransactionMetadata
		extended          vertex.WrappedOutput
		endorsing         []*vertex.WrappedTx
		coverage          uint64
		attacherName      string
		strategyShortName string
		pastConeForDebug  *vertex.PastCone
	}

	Proposer struct {
		*Task
		strategy *Strategy
		Name     string
		Msg      string // how proposer ended. For debugging
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
	AllProposingStrategies = make(map[string]*Strategy)
	ErrNoProposals         = errors.New("no proposals were generated")
	ErrNotGoodEnough       = errors.New("proposals aren't good enough")
)

func registerProposerStrategy(s *Strategy) {
	AllProposingStrategies[s.Name] = s
}

func allProposingStrategies() []*Strategy {
	ret := make([]*Strategy, 0)
	for _, s := range AllProposingStrategies {
		if !viper.GetBool("sequencer.disable_proposer." + s.ShortName) {
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
func Run(env environment, targetTs ledger.Time, slotData *SlotData) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
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
		slotData:     slotData,
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

	proposals := make(map[ledger.TransactionID]*proposal)

	go func() {
		for p := range task.proposalChan {
			proposals[*p.tx.ID()] = p
			task.slotData.ProposalSubmitted(p.strategyShortName)
			task.EvidenceProposal(p.strategyShortName)
		}
		close(readStop)
	}()

	task.proposersWG.Wait()
	close(task.proposalChan)
	<-readStop

	if len(proposals) == 0 {
		return nil, nil, ErrNoProposals
	}

	proposalsSlice := maps.Values(proposals)
	best := util.Maximum(proposalsSlice, func(p1, p2 *proposal) bool {
		return p1.coverage < p2.coverage
	})

	const printBestProposal = false
	if printBestProposal {
		task.Log().Infof(">>>>>> best proposal past cone:\n%s\n    Ledger coverage: %s",
			best.pastConeForDebug.Lines("      ").Join("\n"), util.Th(best.pastConeForDebug.LedgerCoverage(task.targetTs)))
	}

	// check if newly generated non-branch transaction has coverage strongly bigger than previously generated
	// non-branch transaction on the same slot
	ownLatest := env.OwnLatestMilestoneOutput().VID
	if !ownLatest.IsBranchTransaction() && ownLatest.Slot() == targetTs.Slot() && best.coverage <= ownLatest.GetLedgerCoverage() {
		return nil, nil, fmt.Errorf("%w (res: %s, best: %s, %s)",
			ErrNotGoodEnough, util.Th(best.coverage), ownLatest.IDShortString(), util.Th(ownLatest.GetLedgerCoverage()))
	}
	task.EvidenceBestProposalForTheTarget(best.strategyShortName)
	return best.tx, best.txMetadata, nil
}

func (p *proposal) String() string {
	endorse := make([]string, 0, len(p.endorsing))
	for _, vid := range p.endorsing {
		endorse = append(endorse, vid.IDShortString())
	}
	return fmt.Sprintf("%s(%s -- %s -> [%s])",
		p.strategyShortName, p.extended.IDShortString(), util.Th(p.coverage), strings.Join(endorse, ", "))
}

func (t *Task) startProposers() {
	for _, s := range allProposingStrategies() {
		p := &Proposer{
			Task:     t,
			strategy: s,
			Name:     t.Name + "-" + s.Name,
		}
		t.proposersWG.Add(1)
		go func() {
			p.IncCounter("prop")
			defer p.DecCounter("prop")

			p.run()
		}()
	}
}

const TraceTagInsertTagAlongInputs = "InsertTagAlongInputs"

// InsertTagAlongInputs includes tag-along outputs from the backlog into attacher
func (t *Task) InsertTagAlongInputs(a *attacher.IncrementalAttacher) (numInserted int) {
	t.Tracef(TraceTagInsertTagAlongInputs, "IN: %s", a.Name)

	if ledger.L().ID.IsPreBranchConsolidationTimestamp(a.TargetTs()) {
		// skipping tagging-along in pre-branch consolidation zone
		t.Tracef(TraceTagInsertTagAlongInputs, "%s. No tag-along in the pre-branch consolidation zone of ticks", a.Name())
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
	t.Tracef(TraceTagInsertTagAlongInputs, "%s. Pre-selected: %d", a.Name, len(preSelected))

	for _, wOut := range preSelected {
		select {
		case <-t.ctx.Done():
			return
		default:
		}
		t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs: pre-selected #%d", wOut.Index)
		if success, err := a.InsertTagAlongInput(wOut); success {
			numInserted++
			t.Tracef(TraceTagInsertTagAlongInputs, "%s. Inserted %s", a.Name, wOut.IDShortString)
			t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs %s. Inserted #%d", a.Name, wOut.Index)
		} else {
			t.Tracef(TraceTagInsertTagAlongInputs, "%s. Failed to insert %s: '%v'", a.Name, wOut.IDShortString, err)
			t.TraceTx(&wOut.VID.ID, "InsertTagAlongInputs %s. Failed to insert #%d: '%v'", a.Name, wOut.Index, err)
		}
		if a.NumInputs() >= t.MaxTagAlongInputs() {
			return
		}
	}
	return
}
