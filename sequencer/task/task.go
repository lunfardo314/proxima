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
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"golang.org/x/exp/maps"
)

// Task to generate proposals for the target ledger time. The taskData is interrupted
// by the context with deadline
type (
	environment interface {
		global.NodeGlobal
		attacher.Environment
		SequencerName() string
		SequencerID() base.ChainID
		ControllerPrivateKey() ed25519.PrivateKey
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		Backlog() *backlog.TagAlongBacklog
		IsConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool
		AddOwnMilestone(vid *vertex.WrappedTx)
		FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs base.LedgerTime) []vertex.WrappedOutput
		MaxInputs() (int, int)
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		EvidenceProposal(strategyShortName string)
		EvidenceBestProposalForTheTarget(strategyShortName string)
	}

	taskData struct {
		environment
		targetTs     base.LedgerTime
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
		txSize            int
		hrString          string
		coverageDelta     uint64
		ledgerCoverage    uint64
		inflation         uint64
		attacherName      string
		strategyShortName string
	}

	proposer struct {
		*taskData
		strategy *proposerStrategy
		Name     string
		Msg      string // how proposer ended. For debugging
	}

	// ProposalGenerator returns incremental attacher as draft transaction or
	// otherwise nil and forceExit flag = true
	ProposalGenerator func(p *proposer) (*attacher.IncrementalAttacher, bool)

	proposerStrategy struct {
		Name             string
		ShortName        string
		GenerateProposal ProposalGenerator
	}
)

const TraceTagTask = "taskData"

var (
	AllProposingStrategies = make(map[string]*proposerStrategy)
	ErrNoProposals         = errors.New("no proposals were generated")
	ErrNotGoodEnough       = errors.New("proposals aren't good enough")
)

func registerProposerStrategy(s *proposerStrategy) {
	AllProposingStrategies[s.Name] = s
}

func allProposingStrategies() []*proposerStrategy {
	ret := make([]*proposerStrategy, 0)
	for _, s := range AllProposingStrategies {
		if !viper.GetBool("sequencer.disable_proposer." + s.ShortName) {
			ret = append(ret, s)
		}
	}
	return ret
}

// Run starts taskData with the aim to generate sequencer transaction for the target ledger time.
// The proposer taskData consists of several proposers (goroutines)
// Each proposer generates proposals and writes it to the channel of the taskData.
// The best proposal is selected and returned. Function only returns transaction which is better
// than others in the tippool for the current slot. Otherwise, returns nil
func Run(env environment, targetTs base.LedgerTime, slotData *SlotData) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	//startTask := time.Now()
	//defer func(start time.Time) {
	//	runTaskDurationGauge.Set(float64(time.Since(start)) / float64(time.Millisecond))
	//}(startTask)
	//
	//registerGCMetricsOnce(env)

	deadline := ledger.ClockTime(targetTs)
	nowis := time.Now()
	env.Tracef(TraceTagTask, "RunTask: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	task := &taskData{
		environment:  env,
		targetTs:     targetTs,
		ctx:          nil,
		proposalChan: make(chan *proposal),
		slotData:     slotData,
		Name:         fmt.Sprintf("%s[%s]", env.SequencerName(), targetTs.String()),
	}

	//trackTasks.RegisterPointer(task)

	// start proposers
	var cancel func()
	task.ctx, cancel = context.WithDeadline(env.Ctx(), deadline)
	defer cancel() // to prevent context leak

	// starts one goroutine for each known strategy
	task.startProposers()

	// reads all proposals from proposers into the slice
	// stops reading when all goroutines exit

	// chanel is needed to make sure the reading loop has ended
	readStop := make(chan struct{})

	proposals := make(map[base.TransactionID]*proposal)

	go func() {
		for p := range task.proposalChan {
			proposals[p.tx.ID()] = p
			task.slotData.ProposalSubmitted(p.strategyShortName)
			task.EvidenceProposal(p.strategyShortName)
		}
		close(readStop)
	}()

	task.proposersWG.Wait()
	close(task.proposalChan)
	<-readStop

	//numProposalsTask.Set(float64(len(proposals)))

	if len(proposals) == 0 {
		return nil, nil, ErrNoProposals
	}

	proposalsSlice := maps.Values(proposals)
	best := util.Maximum(proposalsSlice, func(p1, p2 *proposal) bool {
		switch {
		case p1.ledgerCoverage < p2.ledgerCoverage:
			return true
		case p1.ledgerCoverage == p2.ledgerCoverage:
			// out of two with equal coverage, we select the one with less size
			return p1.txSize > p2.txSize
		}
		return false
	})

	// check if the newly generated non-branch transaction has coverage strongly bigger than the previously generated
	// non-branch transaction on the same slot
	ownLatest := env.OwnLatestMilestoneOutput().VID
	if !ownLatest.IsBranchTransaction() && ownLatest.Slot() == targetTs.Slot && best.ledgerCoverage <= ownLatest.GetLedgerCoverage() {
		return nil, nil, fmt.Errorf("%w (res: %s, best: %s, %s)",
			ErrNotGoodEnough, util.Th(best.ledgerCoverage), ownLatest.IDShortString(), util.Th(ownLatest.GetLedgerCoverage()))
	}
	task.EvidenceBestProposalForTheTarget(best.strategyShortName)
	return best.tx, best.txMetadata, nil
}

func (p *proposal) String() string {
	return p.hrString
}

func (t *taskData) newProposer(s *proposerStrategy) *proposer {
	ret := &proposer{
		taskData: t,
		strategy: s,
		Name:     t.Name + "-" + s.Name,
	}

	//trackProposers.RegisterPointer(ret)

	return ret
}

func (t *taskData) startProposers() {
	for _, s := range allProposingStrategies() {
		p := t.newProposer(s)
		t.proposersWG.Add(1)
		go func() {
			t.IncCounter("prop")

			p.run()

			t.proposersWG.Done()
			t.DecCounter("prop")
		}()
	}
}

const TraceTagInsertInputs = "insertInputs"

func (t *taskData) insertInputs(a *attacher.IncrementalAttacher, outs []vertex.WrappedOutput, maxInputs int) (numInserted int) {
	for _, wOut := range outs {
		select {
		case <-t.ctx.Done():
			return
		default:
		}
		if success, err := a.InsertInput(wOut); success {
			numInserted++
			t.Tracef(TraceTagInsertInputs, "%s. Inserted %s", a.Name, wOut.IDStringShort)
		} else {
			t.Tracef(TraceTagInsertInputs, "%s. Failed to insert %s: '%v'", a.Name, wOut.IDStringShort, err)
		}
		if a.NumInputs() >= maxInputs {
			return
		}
	}
	return
}

// InsertTagAlongInputs includes filtered outputs from the backlog into attacher
func (t *taskData) InsertTagAlongInputs(a *attacher.IncrementalAttacher, maxInputs int) (numInserted int) {
	preSelected := t.Backlog().FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		t.Assertf(wOut.LockName() == ledger.ChainLockName, "wOut.LockName() == ledger.ChainLockName")

		if !ledger.ValidSequencerPace(wOut.Timestamp(), a.TargetTs()) {
			return false
		}
		// fast filtering out already consumed outputs in the predecessor milestone context
		if t.IsConsumedInThePastPath(wOut, a.Extending().VID) {
			return false
		}
		return true
	})
	return t.insertInputs(a, preSelected, maxInputs)
}

func (t *taskData) InsertDelegationInputs(a *attacher.IncrementalAttacher, maxInputs int) (numInserted int) {
	t.Tracef(TraceTagInsertInputs, "IN InsertDelegationInputs: %s, maxInputs: %d", a.Name, maxInputs)
	// TODO --
	return
	//rdr := a.BaselineSugaredStateReader()
	//seqID := t.SequencerID()
	//preSelected := make([]vertex.WrappedOutput, 0, maxInputs-a.NumInputs())
	//
	//rdr.IterateDelegatedOutputs(ledger.ChainLockFromChainID(seqID), func(oid base.OutputID, o *ledger.Output, dLock *ledger.DelegationLock) bool {
	//	wOut := attacher.AttachOutputWithID(ledger.OutputWithID{ChainID: oid, Output: o}, a)
	//	if !ledger.ValidDelegationPace(wOut.Timestamp(), a.TargetTs()) {
	//		return false
	//	}
	//	if t.IsConsumedInThePastPath(wOut, a.Extending().VID) {
	//		return true
	//	}
	//	delegationID, ok := ledger.ExtractChainID(o, oid)
	//	if !ok {
	//		return true
	//	}
	//	if !ledger.IsOpenDelegationSlot(delegationID, a.TargetTs().Slot) {
	//		return true
	//	}
	//	if ledger.L().CalcChainInflationAmount(oid.Timestamp(), a.TargetTs(), o.TokenBalance()) == 0 {
	//		return true
	//	}
	//	preSelected = append(preSelected, wOut)
	//	return true
	//})
	//return t.insertInputs(a, preSelected, maxInputs)
}
