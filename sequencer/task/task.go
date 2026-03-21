package task

import (
	"context"
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
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/sequencer/factory"
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
		ControllerKeys() (byte, []byte, []byte) // sig type, private key, public key
		OwnLatestMilestoneOutput() vertex.WrappedOutput
		Backlog() *backlog.TagAlongBacklog
		IsConsumedInThePastPath(oid base.OutputID, ms *vertex.WrappedTx, getStateReader func() multistate.SugaredStateReader) bool
		AddOwnMilestone(vid *vertex.WrappedTx)
		FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs base.LedgerTime) []vertex.WrappedOutput
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		EvidenceProposal(strategyShortName string)
		EvidenceBestProposalForTheTarget(strategyShortName string)
		SkeletonFactory() *factory.Factory
	}

	taskData struct {
		environment
		targetTs     base.LedgerTime
		ctx          context.Context
		proposersWG  sync.WaitGroup
		proposalChan chan *finalProposal
		slotData     *SlotData
		// proposals    []*proposal
		Name string
	}

	proposer struct {
		*taskData
		strategy *proposerStrategy
		Name     string
		Msg      string // how proposer ended. For debugging
	}

	proposal struct {
		*proposer
		*attacher.IncrementalAttacher
		*txbuilder_seq.SeqTxBuilder
		attachmentCost uint16
		effectiveTs    base.LedgerTime // overrides p.targetTs when set (used by f0)
	}

	finalProposal struct {
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

	// ProposalGenerator returns proposal as draft transaction or otherwise nil and forceExit flag = true
	ProposalGenerator func(p *proposer) (*proposal, bool)

	proposerStrategy struct {
		Name             string
		ShortName        string
		GenerateProposal ProposalGenerator
	}
)

const TraceRunTagTask = "runTask"

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
func Run(env environment, targetTs base.LedgerTime, slotData *SlotData) (*transaction.Transaction, *txmetadata.TransactionMetadata, string, error) {
	deadline := ledger.ClockTime(targetTs)
	nowis := time.Now()

	env.Tracef(TraceRunTagTask, "START: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))
	defer env.Tracef(TraceRunTagTask, "END: target: %s", targetTs.String)

	task := &taskData{
		environment:  env,
		targetTs:     targetTs,
		ctx:          nil,
		proposalChan: make(chan *finalProposal),
		slotData:     slotData,
		Name:         fmt.Sprintf("%s[%s]", env.SequencerName(), targetTs.String()),
	}

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

	proposals := make(map[base.TransactionID]*finalProposal)

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

	if len(proposals) == 0 {
		return nil, nil, "", ErrNoProposals
	}

	proposalsSlice := maps.Values(proposals)
	best := util.Maximum(proposalsSlice, func(p1, p2 *finalProposal) bool {
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
		return nil, nil, "", fmt.Errorf("%w (res: %s, best: %s, %s)",
			ErrNotGoodEnough, util.Th(best.ledgerCoverage), ownLatest.IDShortString(), util.Th(ownLatest.GetLedgerCoverage()))
	}
	task.EvidenceBestProposalForTheTarget(best.strategyShortName)
	return best.tx, best.txMetadata, best.hrString, nil
}

func (p *finalProposal) String() string {
	return p.hrString
}

func (t *taskData) newProposer(s *proposerStrategy) *proposer {
	ret := &proposer{
		taskData: t,
		strategy: s,
		Name:     t.Name + "-" + s.Name,
	}

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
