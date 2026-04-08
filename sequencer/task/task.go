package task

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
)

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
		EvidenceEndorsementCount(numEndorsements int)
		SkeletonFactory() *factory.Factory
		// TagAlongBudgetNumerator returns the tag-along budget numerator scaled by sequencer pressure.
		// Full budget = 2 (2/3 of consensus). Under pressure, reduced to 1 or 0.
		TagAlongBudgetNumerator() int
	}

	taskData struct {
		environment
		targetTs base.LedgerTime
		ctx      context.Context
		slotData *SlotData
		Name     string
	}

	proposal struct {
		*taskData
		*attacher.IncrementalAttacher
		*txbuilder_seq.SeqTxBuilder
		attachmentCost uint16
		effectiveTs    base.LedgerTime // overrides targetTs when set (used by factory proposer)
	}

	finalProposal struct {
		tx             *transaction.Transaction
		txMetadata     *txmetadata.TransactionMetadata
		txSize         int
		hrString       string
		coverageDelta  uint64
		ledgerCoverage uint64
		inflation      uint64
		attacherName   string
		source         string // which proposer produced this ("boot", "branch", "factory", "base")
	}
)

const TraceRunTagTask = "runTask"

var (
	ErrNoProposals   = errors.New("no proposals were generated")
	ErrNotGoodEnough = errors.New("proposals aren't good enough")
)

// Run generates a sequencer transaction for the target ledger time.
// Proposal sources are tried sequentially:
//  1. Boot proposer (only when own milestone is stale — bootstrap/recovery)
//  2. Branch proposer (only for slot boundary targets)
//  3. Factory proposer (consumes pre-built skeleton with endorsements)
//  4. Base extend proposer (fallback: extend own latest milestone without endorsements)
//
// Returns the best proposal or ErrNoProposals/ErrNotGoodEnough.
func Run(env environment, targetTs base.LedgerTime, slotData *SlotData) (*transaction.Transaction, *txmetadata.TransactionMetadata, string, error) {
	deadline := ledger.ClockTime(targetTs)
	nowis := time.Now()

	env.Tracef(TraceRunTagTask, "START: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))
	defer env.Tracef(TraceRunTagTask, "END: target: %s", targetTs.String)

	var cancel func()
	task := &taskData{
		environment: env,
		targetTs:    targetTs,
		slotData:    slotData,
		Name:        fmt.Sprintf("%s[%s]", env.SequencerName(), targetTs.String()),
	}
	task.ctx, cancel = context.WithDeadline(env.Ctx(), deadline)
	defer cancel()

	var result *finalProposal

	// 1. Boot proposer: only fires when own milestone is stale (>1 slot behind)
	if fp := task.tryBootProposal(); fp != nil {
		result = fp
	}

	// 2. Branch target: use base proposer for branch generation
	if result == nil && targetTs.IsSlotBoundary() {
		if fp := task.tryBranchProposal(); fp != nil {
			result = fp
		}
	}

	// 3. Factory proposer: consume best pre-built skeleton
	if result == nil && !targetTs.IsSlotBoundary() {
		if fp := task.tryFactoryProposal(); fp != nil {
			result = fp
		}
	}

	// 4. Base extend fallback: extend own latest milestone without endorsements
	if result == nil && !targetTs.IsSlotBoundary() {
		if fp := task.tryBaseExtendProposal(); fp != nil {
			result = fp
		}
	}

	if result == nil {
		return nil, nil, "", ErrNoProposals
	}

	// validate: coverage must be strictly better than previous non-branch milestone on this slot
	ownLatest := env.OwnLatestMilestoneOutput().VID
	if !ownLatest.IsBranchTransaction() && ownLatest.Slot() == targetTs.Slot && result.ledgerCoverage <= ownLatest.GetLedgerCoverage() {
		return nil, nil, "", fmt.Errorf("%w (res: %s, best: %s, %s)",
			ErrNotGoodEnough, util.Th(result.ledgerCoverage), ownLatest.IDShortString(), util.Th(ownLatest.GetLedgerCoverage()))
	}
	task.EvidenceEndorsementCount(result.tx.NumEndorsements())
	return result.tx, result.txMetadata, result.hrString, nil
}

func (fp *finalProposal) String() string {
	return fp.hrString
}
