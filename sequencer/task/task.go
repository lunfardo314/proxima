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
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/delegationpool"
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
		DelegationPoolSnapshot(currentSlot uint32) ([]delegationpool.Candidate, map[uint32]uint64)
		IsConsumedInThePastPath(oid base.OutputID, ms *vertex.WrappedTx, getStateReader func() multistate.SugaredStateReader) bool
		AddOwnMilestone(vid *vertex.WrappedTx)
		FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs base.LedgerTime) []vertex.WrappedOutput
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		EvidenceEndorsementCount(numEndorsements int)
		SkeletonFactory() *factory.Factory
		// TagAlongBudgetNumerator returns the tag-along budget numerator scaled by sequencer pressure.
		// Full budget = 2 (2/3 of consensus). Under pressure, reduced to 1 or 0.
		TagAlongBudgetNumerator() int
		// MaxTagAlongInputs returns the configured max tag-along inputs per milestone.
		MaxTagAlongInputs() int
		// SuppressHealthEnforcement returns true when the sequencer is allowed to
		// issue branches below the health threshold (see ConfigOptions).
		SuppressHealthEnforcement() bool
		// SuppressCoverageContributionLowerBound returns true when the sequencer is allowed to
		// issue branches below the per-sequencer coverage lower bound.
		SuppressCoverageContributionLowerBound() bool
		// SuppressCoverageSeeking returns true when the sequencer should stop folding in
		// other sequencers' coverage via endorsements (no-branch mode, own milestone already
		// healthy) — build tag-along / delegation milestones only (extend-only base proposer).
		SuppressCoverageSeeking() bool
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
		tx               *transaction.Transaction
		txMetadata       *txmetadata.TransactionMetadata
		txSize           int
		hrString         string
		coverageDelta    uint64
		ledgerCoverage   uint64
		inflation        uint64
		attacherName     string
		source           string        // which proposer produced this ("boot", "branch", "factory", "base")
		predecessorTs    base.LedgerTime // timestamp of the extended predecessor
		attachmentCost   int
	}
)

const TraceRunTagTask = "runTask"

// BuildBudget is the wall-clock time task.Run has to build a proposal.
// Decoupled from the target timestamp offset: the target can be close to "now" for
// fast milestone pace, while the builder has enough time for I/O-heavy operations
// (lazy branch commit, state trie reads, coverage delta computation).
const BuildBudget = 2 * time.Second

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
// Timing model:
// The transaction's timestamp (targetTs) is a logical clock, not a wall-clock deadline.
// Nodes do not enforce strict synchronicity between ledger time and wall clock:
// a transaction with timestamp TS is valid whether built slightly before or after ClockTime(TS).
// The real validity constraints are sequencer pace and slot boundaries — both checked in
// ledger time, not wall clock.
//
// The build budget (BuildBudget) is a wall-clock duration decoupled from the target timestamp.
// This allows close-to-"now" targets (small targetOffsetTicks → high milestone rate) while
// giving the builder enough time for I/O-heavy operations (lazy branch commit, state trie reads).
//
// Returns the best proposal or ErrNoProposals/ErrNotGoodEnough.
func Run(env environment, targetTs base.LedgerTime, slotData *SlotData) (*transaction.Transaction, *txmetadata.TransactionMetadata, uint64, string, error) {
	deadline := time.Now().Add(BuildBudget)
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

	// 3+4. Non-branch: build both extend-only and extend+endorse, pick by coverage.
	// Extend-only (base) is the selfish default; extend+endorse (factory) is only
	// preferred when it produces strictly better coverage.
	if result == nil && !targetTs.IsSlotBoundary() {
		baseResult := task.tryBaseExtendProposal()
		if task.SuppressCoverageSeeking() {
			// no-branch mode, already safely included: don't fold in other sequencers'
			// coverage via endorsements — service tag-along / delegation only (base extend).
			result = baseResult
		} else {
			factoryResult := task.tryFactoryProposal()

			switch {
			case baseResult == nil && factoryResult == nil:
				// neither available
			case baseResult == nil:
				result = factoryResult
			case factoryResult == nil:
				result = baseResult
			default:
				result = betterProposal(baseResult, factoryResult)
			}
		}
	}

	if result == nil {
		return nil, nil, 0, "", ErrNoProposals
	}

	// validate: coverage must be strictly better than previous non-branch milestone on this slot
	ownLatest := env.OwnLatestMilestoneOutput().VID
	if !ownLatest.IsBranchTransaction() && ownLatest.Slot() == targetTs.Slot && result.ledgerCoverage <= ownLatest.GetLedgerCoverage() {
		return nil, nil, 0, "", fmt.Errorf("%w (res: %s, best: %s, %s)",
			ErrNotGoodEnough, util.Th(result.ledgerCoverage), ownLatest.IDShortString(), util.Th(ownLatest.GetLedgerCoverage()))
	}
	task.EvidenceEndorsementCount(result.tx.NumEndorsements())
	return result.tx, result.txMetadata, result.ledgerCoverage, result.hrString, nil
}

// betterProposal picks the better of two non-nil proposals.
// 1. Higher coverage wins
// 2. On equal coverage: younger (later) predecessor wins
// 3. On equal predecessor: smaller attachment cost wins
func betterProposal(a, b *finalProposal) *finalProposal {
	switch {
	case a.ledgerCoverage > b.ledgerCoverage:
		return a
	case b.ledgerCoverage > a.ledgerCoverage:
		return b
	case a.predecessorTs.After(b.predecessorTs):
		return a
	case b.predecessorTs.After(a.predecessorTs):
		return b
	case a.attachmentCost <= b.attachmentCost:
		return a
	default:
		return b
	}
}

func (fp *finalProposal) String() string {
	return fp.hrString
}
