package attacher

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

const (
	TraceTagIncrementalAttacher                     = "incAttach"
	TraceTagIncrementalAttacherWithExplicitBaseline = "incAttachExplicitBL"
	// conflictCheckTimeout limits how long CheckConflicts can run in incremental attachers.
	// Prevents goroutine starvation under I/O contention (BadgerDB compaction, trie reads).
	conflictCheckTimeout = 5 * time.Second
)

func (a *IncrementalAttacher) checkConflictsWithTimeout() (*vertex.WrappedOutput, error) {
	ctx, cancel := context.WithTimeout(a.Ctx(), conflictCheckTimeout)
	defer cancel()
	return a.CheckConflicts(ctx)
}

// NewIncrementalAttacher creates an IncrementalAttacher for the given target timestamp.
// Only targetTs.Slot and targetTs.IsSlotBoundary() are used for structural decisions
// (baseline direction, stem input, coverage). Pace checks are the caller's responsibility.
func NewIncrementalAttacher(name string, env Environment, targetTs base.LedgerTime, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	targetSlot := targetTs.Slot
	isBranch := targetTs.IsSlotBoundary()

	for _, endorseVID := range endorse {
		env.Assertf(endorseVID.IsSequencerTransaction(), "NewIncrementalAttacher: endorseVID.IsSequencerTransaction()")
		env.Assertf(targetSlot == endorseVID.Slot(), "NewIncrementalAttacher: targetSlot == endorseVid.Slot()")
	}
	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). extend: %s, endorse: {%s}",
		name, extend.IDStringShort, func() string { return vertex.VerticesLines(endorse).Join(",") })

	var baselineDirection *vertex.WrappedTx
	if isBranch {
		env.Assertf(len(endorse) == 0, "NewIncrementalAttacher: len(endorse)==0")
		if !extend.VID.IsSequencerTransaction() {
			return nil, fmt.Errorf("NewIncrementalAttacher %s: cannot extend non-sequencer transaction %s into a branch",
				name, extend.VID)
		}
		baselineDirection = extend.VID
	} else {
		if extend.Slot() != targetSlot {
			// cross-slot, must have endorsement
			if len(endorse) > 0 {
				baselineDirection = endorse[0]
			}
		} else {
			// same slot
			baselineDirection = extend.VID
		}
	}
	if baselineDirection == nil {
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to determine baseline direction in %s",
			name, extend.IDStringShort())
	}
	baselineBranchID, found := baselineDirection.BaselineBranch()
	if !found {
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to determine valid baselineDirection branch of %s. baseline direction: %s",
			name, extend.IDStringShort(), baselineDirection.IDShortString())
	}

	// pass base.T(targetSlot, 0) to PastCone — coverage calculation only uses the slot
	ret := &IncrementalAttacher{
		attacher:   newPastConeAttacher(env, nil, base.T(targetSlot, 0), name),
		endorse:    make([]*vertex.WrappedTx, 0),
		inputs:     make([]vertex.WrappedOutput, 0),
		targetSlot: targetSlot,
		isBranch:   isBranch,
	}
	ret.getBaselineStateReader = ret.Branches().GetVirtualStateReaderForTheBranch

	if err := ret.initIncrementalAttacher(baselineBranchID, isBranch, extend, endorse...); err != nil {
		ret.Close()
		return nil, err
	}
	if conflict, err := ret.checkConflictsWithTimeout(); err != nil {
		ret.Close()
		return nil, err
	} else if conflict != nil {
		ret.Close()
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to create incremental attacher extending  %s: double-spend (conflict) %s in the past cone",
			name, extend.IDStringShort(), conflict.IDStringShort())
	}
	return ret, nil
}

func NewIncrementalAttacherWithExplicitBaseline(name string, env Environment, targetTs base.LedgerTime, extend vertex.WrappedOutput, baselineID base.TransactionID) (*IncrementalAttacher, error) {
	targetSlot := targetTs.Slot

	env.Assertf(baselineID.IsBranchTransaction(), "baselineID.IsBranchTransaction()")
	env.Assertf(!targetTs.IsSlotBoundary(), "!targetTs.IsSlotBoundary()")
	env.Assertf(int(targetSlot)-int(extend.Slot()) >= 1, "int(targetSlot)(%d)-int(extend.Slot())(%s)>=1",
		targetSlot, extend.IDStringShort)

	env.Tracef(TraceTagIncrementalAttacherWithExplicitBaseline, "NewIncrementalAttacherWithExpliciteBaseline(%s). extend: %s, explicit baseline: %s",
		name, extend.IDStringShort, baselineID.StringShort)

	baseline := AttachTxID(baselineID, env, WithInvokedBy(name))
	if baseline.GetTxStatus() != vertex.Good {
		return nil, fmt.Errorf("NewIncrementalAttacherWithExplicitBaseline %s: extend: %s, failed to attach GOOD explict baseline branch of %s",
			name, extend.IDStringShort(), baselineID.StringShort())
	}

	ret := &IncrementalAttacher{
		attacher:           newPastConeAttacher(env, nil, base.T(targetSlot, 0), name),
		endorse:            make([]*vertex.WrappedTx, 0),
		inputs:             make([]vertex.WrappedOutput, 0),
		targetSlot:         targetSlot,
		isBranch:           false,
		explicitBaselineID: util.Ref(baselineID),
	}
	ret.getBaselineStateReader = ret.Branches().GetVirtualStateReaderForTheBranch

	if err := ret.initIncrementalAttacher(baselineID, false, extend); err != nil {
		ret.Close()
		return nil, err
	}
	if conflict, err := ret.checkConflictsWithTimeout(); err != nil {
		ret.Close()
		return nil, err
	} else if conflict != nil {
		ret.Close()
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to create incremental attacher extending  %s: double-spend (conflict) %s in the past cone",
			name, extend.IDStringShort(), conflict.IDStringShort())
	}
	return ret, nil
}

// Clone creates an independent copy of the IncrementalAttacher.
// The clone shares vertex pointers (WrappedTx) but has its own mutable state
// (past cone tracking, endorsement/input lists, coverage accumulators).
// Must be called with no pending delta in the past cone (asserted inside PastCone.Clone).
func (a *IncrementalAttacher) Clone(name string) *IncrementalAttacher {
	util.Assertf(!a.IsClosed(), "IncrementalAttacher.Clone: attacher is closed")

	ret := &IncrementalAttacher{
		attacher: attacher{
			Environment: a.Environment,
			Library:     a.Library,
			pastCone:    a.pastCone.Clone(name),
			name:        name,
		},
		endorse:         slices.Clone(a.endorse),
		inputs:          slices.Clone(a.inputs),
		targetSlot:      a.targetSlot,
		isBranch:        a.isBranch,
		stemOutput:      a.stemOutput,
		inflationAmount: a.inflationAmount,
	}
	if a.explicitBaselineID != nil {
		id := *a.explicitBaselineID
		ret.explicitBaselineID = &id
	}
	ret.getBaselineStateReader = ret.Branches().GetVirtualStateReaderForTheBranch
	return ret
}

// Close releases all references of vertices. Repetitive closing has no effect
func (a *IncrementalAttacher) Close() {
	if a != nil && !a.IsClosed() {
		a.endorse = nil
		a.inputs = nil
		a.pastCone.Dispose()
		a.pastCone = nil
		a.closed = true
	}
}

func (a *IncrementalAttacher) IsClosed() bool {
	return a.closed
}

func (a *IncrementalAttacher) BaselineBranch() *base.TransactionID {
	return a.pastCone.GetBaseline()
}

func (a *IncrementalAttacher) initIncrementalAttacher(baselineBranchID base.TransactionID, isBranch bool, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) error {
	a.setBaseline(util.Ref(baselineBranchID))
	a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). baseline: %s", a.name, baselineBranchID.StringShort)

	for _, endorsement := range endorse {
		a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertEndorsement: %s", a.name, endorsement.IDShortString)
		if err := a.insertEndorsement(endorsement); err != nil {
			return err
		}
	}

	if err := a.insertVirtuallyConsumedOutput(extend); err != nil {
		return err
	}

	if isBranch {
		// The branch being built must stem-consume the CURRENT baseline's stem output.
		// a.pastCone.GetBaseline() may have been upgraded during the endorsement/extend
		// past-cone walks above (via MergePastCone when a stem-descendant branch is
		// reached), so it can differ from the baselineBranchID we were constructed with.
		// Using the original argument here would compute the stem of a superseded
		// ancestor, and that stem is already consumed in the current baseline's state
		// → the "already consumed" liveness halt observed on 2026-04-24.
		//
		// Ledger invariant: a branch's stem predecessor IS its baseline (enforced on
		// consumption). Assertion below catches any future regression where the past
		// cone's baseline drifts away from what should be the stem predecessor.
		effectiveBaseline := *a.pastCone.GetBaseline()
		a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertStemInput from %s", a.name, effectiveBaseline.StringShort)
		// Ensure the baseline branch is in the memDAG. It may have been GC'd if the node
		// fell far behind. AttachTxID fetches it from the state DB if needed.
		AttachTxID(effectiveBaseline, a.Environment, WithInvokedBy("stemInput"))
		a.stemOutput = a.GetStemWrappedOutput(effectiveBaseline)
		if a.stemOutput.VID == nil {
			return fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", effectiveBaseline.StringShort())
		}
		a.Assertf(a.stemOutput.VID.ID() == effectiveBaseline,
			"stem predecessor invariant: stemOutput.VID (%s) must be the current baseline (%s)",
			a.stemOutput.VID.IDShortString, effectiveBaseline.StringShort)
		if err := a.insertVirtuallyConsumedOutput(a.stemOutput); err != nil {
			return err
		}
	}
	return nil
}

func (a *IncrementalAttacher) Stem() vertex.WrappedOutput {
	return a.stemOutput
}

func (a *IncrementalAttacher) insertVirtuallyConsumedOutput(wOut vertex.WrappedOutput) error {
	a.Assertf(wOut.ValidID(), "wOut.ValidID()")

	if !a.refreshDependencyStatus(wOut.VID) {
		return a.err
	}
	if !a.attachOutput(wOut) {
		return a.err
	}
	if !a.pastCone.IsKnownDefined(wOut.VID) {
		return fmt.Errorf("output %s not solid yet", wOut.IDStringShort())
	}
	ctx, cancel := context.WithTimeout(a.Ctx(), conflictCheckTimeout)
	defer cancel()
	if conflict, err := a.pastCone.AddVirtuallyConsumedOutput(ctx, wOut, a.getBaselineStateReader); err != nil {
		return err
	} else if conflict != nil {
		return fmt.Errorf("past cone contains double-spend %s", conflict.IDStringShort())
	}
	a.inputs = append(a.inputs, wOut)
	return nil
}

func (a *IncrementalAttacher) ExplicitBaselineID() *base.TransactionID {
	return a.explicitBaselineID
}

// InsertEndorsement preserves consistency in case of failure.
// Pace checks are the caller's responsibility.
func (a *IncrementalAttacher) InsertEndorsement(endorsement *vertex.WrappedTx) error {
	a.Assertf(!a.IsClosed(), "a.IsClosed()")

	if a.pastCone.IsKnown(endorsement) {
		return fmt.Errorf("endorsing makes no sense: %s is already in the past cone", endorsement.IDShortString())
	}

	a.pastCone.BeginDelta()
	if err := a.insertEndorsement(endorsement); err != nil {
		a.pastCone.RollbackDelta()
		a.setError(nil)
		return err
	}
	a.pastCone.CommitDelta()
	return nil
}

func (a *IncrementalAttacher) insertEndorsement(endorsement *vertex.WrappedTx) error {
	if !a.attachEndorsementDependency(endorsement) {
		return a.err
	}

	if conflict, err := a.checkConflictsWithTimeout(); err != nil {
		return err
	} else if conflict != nil {
		return fmt.Errorf("insertEndorsement: double-spend (conflict) %s in the past cone", conflict.IDStringShort())
	}
	a.endorse = append(a.endorse, endorsement)
	return nil
}

func (a *IncrementalAttacher) PastConeAttachmentCost() int {
	return a.pastCone.AttachmentCost()
}

// InsertInput inserts tag along or delegation input.
// In case of failure returns false and attacher state with vertex references remains consistent.
// Pace checks are the caller's responsibility.
func (a *IncrementalAttacher) InsertInput(wOut vertex.WrappedOutput, atomicCheck func() (bool, error)) (valid bool, err error) {
	util.Assertf(!a.IsClosed(), "a.IsClosed()")
	util.AssertNoError(a.err)

	a.pastCone.BeginDelta()
	err = a.insertVirtuallyConsumedOutput(wOut)
	valid = true
	if err == nil {
		valid, err = atomicCheck()
	}
	if err != nil {
		a.pastCone.RollbackDelta()
		err = fmt.Errorf("InsertInput(%s): %w", a.name, err)
		a.setError(nil)
		return valid, err
	}
	util.AssertNoError(a.err)
	a.pastCone.CommitDelta()
	return true, nil
}

// TargetTs returns a synthetic LedgerTime for backward compatibility with callers
// that need a timestamp (e.g. proposal building). Uses TimestampLowerBound() for non-branch.
// For branches, returns (targetSlot, 0).
func (a *IncrementalAttacher) TargetTs() base.LedgerTime {
	if a.isBranch {
		return base.T(a.targetSlot, 0)
	}
	return a.TimestampLowerBound()
}

// TargetSlot returns the target slot of the attacher.
func (a *IncrementalAttacher) TargetSlot() uint32 {
	return a.targetSlot
}

// IsBranchTarget returns true if the attacher targets a branch transaction.
func (a *IncrementalAttacher) IsBranchTarget() bool {
	return a.isBranch
}

// TimestampLowerBound returns the earliest valid target timestamp for a sequencer transaction
// built from this attacher's current state.
// For branches, returns (targetSlot, 0).
// For non-branches, returns max(input/endorsement timestamps) + sequencer pace,
// adjusted for post-branch consolidation.
func (a *IncrementalAttacher) TimestampLowerBound() base.LedgerTime {
	if a.isBranch {
		return base.T(a.targetSlot, 0)
	}

	pace := int64(a.Library.TransactionPaceSequencer)

	var maxTicks int64
	for _, wOut := range a.inputs {
		if t := wOut.Timestamp().TicksSinceGenesis(); t > maxTicks {
			maxTicks = t
		}
	}
	for _, vid := range a.endorse {
		if t := vid.Timestamp().TicksSinceGenesis(); t > maxTicks {
			maxTicks = t
		}
	}

	lower, err := base.LedgerTimeFromTicksSinceGenesis(maxTicks + pace)
	if err != nil {
		return base.T(a.targetSlot, a.Library.PostBranchConsolidationTicks)
	}

	if lower.Tick > 0 && lower.Tick < a.Library.PostBranchConsolidationTicks {
		lower = base.T(lower.Slot, a.Library.PostBranchConsolidationTicks)
	}

	return lower
}

func (a *IncrementalAttacher) NumInputs() int {
	return len(a.inputs) + 2
}

// Completed returns true is past cone is all solid and consistent (no conflicts)
func (a *IncrementalAttacher) Completed() bool {
	return a.pastCone.IsComplete()
}

func (a *IncrementalAttacher) Extending() vertex.WrappedOutput {
	a.Assertf(!a.IsClosed(), "!a.IsClosed() -- %s", a.name)
	return a.inputs[0]
}

func (a *IncrementalAttacher) Endorsing() []*vertex.WrappedTx {
	a.Assertf(!a.IsClosed(), "!a.IsClosed() -- %s", a.name)
	return a.endorse
}

func (a *IncrementalAttacher) ExtendEndorseLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("extend: %s", a.inputs[0].IDStringShort())
	for _, vid := range a.endorse {
		ret.Add("-> %s", vid.IDShortString())
	}
	return ret
}
