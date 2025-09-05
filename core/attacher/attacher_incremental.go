package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

const (
	TraceTagIncrementalAttacher                     = "incAttach"
	TraceTagIncrementalAttacherWithExplicitBaseline = "incAttachExplicitBL"
)

func NewIncrementalAttacher(name string, env Environment, targetTs base.LedgerTime, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	env.Assertf(ledger.ValidSequencerPace(extend.Timestamp(), targetTs), "NewIncrementalAttacher: target is closer than allowed pace (%d): %s -> %s",
		ledger.TransactionPaceSequencer(), extend.Timestamp().String, targetTs.String)

	for _, endorseVID := range endorse {
		env.Assertf(endorseVID.IsSequencerMilestone(), "NewIncrementalAttacher: endorseVID.IsSequencerTransaction()")
		env.Assertf(targetTs.Slot == endorseVID.Slot(), "NewIncrementalAttacher: targetTs.Slot() == endorseVid.Slot()")
		env.Assertf(ledger.ValidTransactionPace(endorseVID.Timestamp(), targetTs), "NewIncrementalAttacher: ledger.ValidTransactionPace(endorseVID.Timestamp(), targetTs)")
	}
	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). extend: %s, endorse: {%s}",
		name, extend.IDStringShort, func() string { return vertex.VerticesLines(endorse).Join(",") })

	var baselineDirection *vertex.WrappedTx
	if targetTs.Tick == 0 {
		// target is branch
		env.Assertf(len(endorse) == 0, "NewIncrementalAttacher: len(endorse)==0")
		if !extend.VID.IsSequencerMilestone() {
			return nil, fmt.Errorf("NewIncrementalAttacher %s: cannot extend non-sequencer transaction %s into a branch",
				name, extend.VID)
		}
		baselineDirection = extend.VID
	} else {
		// target is not branch
		if extend.Slot() != targetTs.Slot {
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
		// may happen when baselineDirection is virtualTx
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to determine valid baselineDirection branch of %s. baseline direction: %s",
			name, extend.IDStringShort(), baselineDirection.IDShortString())
	}

	ret := &IncrementalAttacher{
		attacher: newPastConeAttacher(env, nil, targetTs, name),
		endorse:  make([]*vertex.WrappedTx, 0),
		inputs:   make([]vertex.WrappedOutput, 0),
		targetTs: targetTs,
	}

	if err := ret.initIncrementalAttacher(baselineBranchID, targetTs, extend, endorse...); err != nil {
		ret.Close()
		return nil, err
	}
	if conflict := ret.CheckConflicts(); conflict != nil {
		ret.Close()
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to create incremental attacher extending  %s: double-spend (conflict) %s in the past cone",
			name, extend.IDStringShort(), conflict.IDStringShort())
	}
	return ret, nil
}

func NewIncrementalAttacherWithExplicitBaseline(name string, env Environment, targetTs base.LedgerTime, extend vertex.WrappedOutput, baselineID base.TransactionID) (*IncrementalAttacher, error) {
	env.Assertf(baselineID.IsBranchTransaction(), "baselineID.IsBranchTransaction()")
	env.Assertf(!targetTs.IsSlotBoundary(), "!targetTs.IsSlotBoundary()")
	env.Assertf(int(targetTs.Slot)-int(extend.Slot()) >= 1, "int(targetTs.Slot)(%s)-int(extend.Slot())(%s)>=1",
		targetTs.String, extend.IDStringShort)
	env.Assertf(ledger.ValidSequencerPace(extend.Timestamp(), targetTs), "NewIncrementalAttacher: target is closer than allowed pace (%d): %s -> %s",
		ledger.TransactionPaceSequencer(), extend.Timestamp().String, targetTs.String)

	env.Tracef(TraceTagIncrementalAttacherWithExplicitBaseline, "NewIncrementalAttacherWithExpliciteBaseline(%s). extend: %s, explicit baseline: %s",
		name, extend.IDStringShort, baselineID.StringShort)

	baseline := AttachTxID(baselineID, env, WithInvokedBy(name))
	if baseline.GetTxStatus() != vertex.Good {
		// may happen when baselineDirection is virtualTx
		return nil, fmt.Errorf("NewIncrementalAttacherWithExplicitBaseline %s: extend: %s, failed to attach GOOD explict baseline branch of %s",
			name, extend.IDStringShort(), baselineID.StringShort())
	}

	ret := &IncrementalAttacher{
		attacher:           newPastConeAttacher(env, nil, targetTs, name),
		endorse:            make([]*vertex.WrappedTx, 0),
		inputs:             make([]vertex.WrappedOutput, 0),
		targetTs:           targetTs,
		explicitBaselineID: util.Ref(baselineID),
	}

	if err := ret.initIncrementalAttacher(baselineID, targetTs, extend); err != nil {
		ret.Close()
		return nil, err
	}
	if conflict := ret.CheckConflicts(); conflict != nil {
		ret.Close()
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to create incremental attacher extending  %s: double-spend (conflict) %s in the past cone",
			name, extend.IDStringShort(), conflict.IDStringShort())
	}
	return ret, nil
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

func (a *IncrementalAttacher) initIncrementalAttacher(baselineBranchID base.TransactionID, targetTs base.LedgerTime, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) error {
	a.setBaseline(util.Ref(baselineBranchID))
	a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). baseline: %s", a.name, baselineBranchID.StringShort)

	// attach endorsements
	for _, endorsement := range endorse {
		a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertEndorsement: %s", a.name, endorsement.IDShortString)
		if err := a.insertEndorsement(endorsement); err != nil {
			return err
		}
	}

	// extend input will always be at index 0
	if err := a.insertVirtuallyConsumedOutput(extend); err != nil {
		return err
	}

	if targetTs.IsSlotBoundary() {
		// stem input, if any, will be at index 1
		// for branches, include stem input
		a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertStemInput", a.name)
		a.stemOutput = a.GetStemWrappedOutput(baselineBranchID)
		if a.stemOutput.VID == nil {
			return fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", baselineBranchID.StringShort())
		}
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
	if conflict := a.pastCone.AddVirtuallyConsumedOutput(wOut, a.Branches().GetStateReaderForTheBranch); conflict != nil {
		return fmt.Errorf("past cone contains double-spend %s", conflict.IDStringShort())
	}
	a.inputs = append(a.inputs, wOut)
	return nil
}

func (a *IncrementalAttacher) ExplicitBaselineID() *base.TransactionID {
	return a.explicitBaselineID
}

// InsertEndorsement preserves consistency in case of failure. Assumes valid pace, otherwise crashes
func (a *IncrementalAttacher) InsertEndorsement(endorsement *vertex.WrappedTx) error {
	a.Assertf(!a.IsClosed(), "a.IsClosed()")
	if !endorsement.ValidSequencerPace(a.targetTs) {
		return fmt.Errorf("IncrementalAttacher(%s).InsertEndorsement: invalid sequencer pace in %s", a.name, endorsement.IDShortString)
	}

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

// insertEndorsement in case of error, attacher remains inconsistent
func (a *IncrementalAttacher) insertEndorsement(endorsement *vertex.WrappedTx) error {
	if !a.attachEndorsementDependency(endorsement) {
		return a.err
	}

	if conflict := a.CheckConflicts(); conflict != nil {
		return fmt.Errorf("insertEndorsement: double-spend (conflict) %s in the past cone", conflict.IDStringShort())
	}
	a.endorse = append(a.endorse, endorsement)
	return nil
}

// InsertInput inserts tag along or delegation input.
// In case of failure return false and attacher state with vertex references remains consistent
// atomicCheck callback is used to add optional additional check right before commiting delta
func (a *IncrementalAttacher) InsertInput(wOut vertex.WrappedOutput, atomicCheck func() (bool, error)) (valid bool, err error) {
	util.Assertf(!a.IsClosed(), "a.IsClosed()")
	util.AssertNoError(a.err)

	if !wOut.VID.ValidSequencerPace(a.targetTs) {
		return true, fmt.Errorf("IncrementalAttacher(%s).InsertInput: invalid sequencer pace in %s", a.name, wOut.IDStringShort())
	}

	// save state for possible rollback because in case of fail the side effect makes attacher inconsistent
	a.pastCone.BeginDelta()
	err = a.insertVirtuallyConsumedOutput(wOut)
	valid = true // it may contain conflict but this is not permanent
	if err == nil {
		valid, err = atomicCheck()
	}
	if err != nil {
		// it is either conflicting, or not solid yet
		// in either case rollback
		a.pastCone.RollbackDelta()
		err = fmt.Errorf("InsertInput: %w", err)
		a.setError(nil)
		return valid, err
	}
	util.AssertNoError(a.err)

	a.pastCone.CommitDelta()
	return true, nil
}

func (a *IncrementalAttacher) TargetTs() base.LedgerTime {
	return a.targetTs
}

func (a *IncrementalAttacher) NumInputs() int {
	return len(a.inputs) + 2
}

// Completed returns true is past cone is all solid and consistent (no conflicts)
// For incremental attacher it may happen (in theory) that some outputs need re-pull,
// if unlucky. The owner of the attacher will have to dismiss the attacher
// and try again later
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
