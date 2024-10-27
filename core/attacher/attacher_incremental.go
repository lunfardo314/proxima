package attacher

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/util"
)

const TraceTagIncrementalAttacher = "incAttach"

var ErrPastConeNotSolidYet = errors.New("past cone not solid yet")

func NewIncrementalAttacher(name string, env Environment, targetTs ledger.Time, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	env.Assertf(ledger.ValidSequencerPace(extend.Timestamp(), targetTs), "NewIncrementalAttacher: target is closer than allowed pace (%d): %s -> %s",
		ledger.TransactionPaceSequencer(), extend.Timestamp().String, targetTs.String)

	for _, endorseVID := range endorse {
		env.Assertf(endorseVID.IsSequencerMilestone(), "NewIncrementalAttacher: endorseVID.IsSequencerMilestone()")
		env.Assertf(targetTs.Slot() == endorseVID.Slot(), "NewIncrementalAttacher: targetTs.Slot() == endorseVid.Slot()")
		env.Assertf(ledger.ValidTransactionPace(endorseVID.Timestamp(), targetTs), "NewIncrementalAttacher: ledger.ValidTransactionPace(endorseVID.Timestamp(), targetTs)")
	}
	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). extend: %s, endorse: {%s}",
		name, extend.IDShortString, func() string { return vertex.VerticesLines(endorse).Join(",") })

	var baselineDirection *vertex.WrappedTx
	if targetTs.Tick() == 0 {
		// target is branch
		env.Assertf(len(endorse) == 0, "NewIncrementalAttacher: len(endorse)==0")
		if !extend.VID.IsSequencerMilestone() {
			return nil, fmt.Errorf("NewIncrementalAttacher %s: cannot extend non-sequencer transaction %s into a branch",
				name, extend.VID)
		}
		baselineDirection = extend.VID
	} else {
		// target is not branch
		if extend.Slot() != targetTs.Slot() {
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
			name, extend.IDShortString())
	}
	baseline := baselineDirection.BaselineBranch()
	if baseline == nil {
		// may happen when baselineDirection is virtualTx
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to determine valid baselineDirection branch of %s. baseline direction: %s",
			name, extend.IDShortString(), baselineDirection.IDShortString())
	}

	ret := &IncrementalAttacher{
		attacher: newPastConeAttacher(env, name),
		endorse:  make([]*vertex.WrappedTx, 0),
		inputs:   make([]vertex.WrappedOutput, 0),
		targetTs: targetTs,
	}

	if err := ret.initIncrementalAttacher(baseline, targetTs, extend, endorse...); err != nil {
		ret.Close()
		return nil, err
	}
	return ret, nil
}

// Close releases all references of Vertices. Incremental attacher must be closed before disposing it,
// otherwise memDAG starts leaking Vertices. Repetitive closing has no effect
// TODO some kind of checking if it is closed after some time
func (a *IncrementalAttacher) Close() {
	if a != nil && !a.IsClosed() {
		a.pastCone.UnReferenceAll()
		a.closed = true
	}
}

func (a *IncrementalAttacher) IsClosed() bool {
	return a.closed
}

func (a *IncrementalAttacher) initIncrementalAttacher(baseline *vertex.WrappedTx, targetTs ledger.Time, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) error {
	if !a.setBaseline(baseline, targetTs) {
		return fmt.Errorf("NewIncrementalAttacher: failed to set baseline branch of %s", extend.IDShortString())
	}
	a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). baseline: %s",
		a.name, baseline.IDShortString)

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
		a.stemOutput = a.GetStemWrappedOutput(&baseline.ID)
		if a.stemOutput.VID == nil {
			return fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", baseline.IDShortString())
		}
		if err := a.insertVirtuallyConsumedOutput(a.stemOutput); err != nil {
			return err
		}
	}
	return nil
}

func (a *IncrementalAttacher) BaselineBranch() *vertex.WrappedTx {
	return a.baseline
}

func (a *IncrementalAttacher) insertVirtuallyConsumedOutput(wOut vertex.WrappedOutput) error {
	ok, defined := a.attachOutput(wOut)
	if !ok {
		util.AssertMustError(a.err)
		return a.err
	}

	if !defined {
		return ErrPastConeNotSolidYet
	}
	if conflict := a.pastCone.AddVirtuallyConsumedOutput(wOut); conflict != nil {
		return fmt.Errorf("past cone contains double-spend %s", conflict.IDShortString())
	}
	a.inputs = append(a.inputs, wOut)
	return nil
}

// saving attacher's past cone state to be able to restore in case it becomes inconsistent when
// attempting to adding conflicting outputs or endorsements

// InsertEndorsement preserves consistency in case of failure
func (a *IncrementalAttacher) InsertEndorsement(endorsement *vertex.WrappedTx) error {
	util.Assertf(!a.IsClosed(), "a.IsClosed()")
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
	if endorsement.IsBadOrDeleted() {
		return fmt.Errorf("NewIncrementalAttacher: can't endorse %s. Reason: '%s'", endorsement.IDShortString(), endorsement.GetError())
	}
	endBaseline := endorsement.BaselineBranch()
	if !a.branchesCompatible(&a.baseline.ID, &endBaseline.ID) {
		return fmt.Errorf("baseline branch %s of the endorsement branch %s is incompatible with the baseline %s",
			endBaseline.IDShortString, endorsement.IDShortString(), a.baseline.IDShortString())
	}
	if endorsement.IsBranchTransaction() {
		// branch is compatible with the baseline
		a.pastCone.MustMarkVertexInTheState(endorsement)
	} else {
		ok, defined := a.attachVertexNonBranch(endorsement)
		a.Assertf(ok || a.err != nil, "ok || a.err != nil")
		if !ok {
			a.Assertf(a.err != nil, "a.err != nil")
			return a.err
		}
		a.Assertf(a.err == nil, "a.err == nil")
		if !defined {
			return fmt.Errorf("insertEndorsement: %w", ErrPastConeNotSolidYet)
		}
	}
	a.endorse = append(a.endorse, endorsement)
	return nil
}

// InsertTagAlongInput inserts tag along input.
// In case of failure return false and attacher state with vertex references remains consistent
func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput) (bool, error) {
	util.Assertf(!a.IsClosed(), "a.IsClosed()")
	util.AssertNoError(a.err)

	// save state for possible rollback because in case of fail the side effect makes attacher inconsistent
	a.pastCone.BeginDelta()
	err := a.insertVirtuallyConsumedOutput(wOut)
	if err != nil {
		// it is either conflicting, or not solid yet
		// in either case rollback
		a.pastCone.RollbackDelta()
		err = fmt.Errorf("InsertTagAlongInput: %w", err)

		a.setError(nil)
		return false, err
	}
	a.inputs = append(a.inputs, wOut)
	util.AssertNoError(a.err)

	a.pastCone.CommitDelta()
	return true, nil
}

// MakeSequencerTransaction creates sequencer transaction from the incremental attacher.
// Increments slotInflation by the amount inflated in the transaction
func (a *IncrementalAttacher) MakeSequencerTransaction(seqName string, privateKey ed25519.PrivateKey, cmdParser SequencerCommandParser) (*transaction.Transaction, error) {
	util.Assertf(!a.IsClosed(), "!a.IsDisposed()")
	otherInputs := make([]*ledger.OutputWithID, 0, len(a.inputs))

	var chainIn ledger.OutputWithID
	var stemIn *ledger.OutputWithID
	var err error

	additionalOutputs := make([]*ledger.Output, 0)
	for i, wOut := range a.inputs {
		switch {
		case i == 0:
			if chainIn, err = wOut.VID.OutputWithIDAt(a.inputs[0].Index); err != nil {
				return nil, err
			}
		case i == 1 && a.targetTs.Tick() == 0:
			var stemInTmp ledger.OutputWithID
			if stemInTmp, err = a.stemOutput.VID.OutputWithIDAt(a.stemOutput.Index); err != nil {
				return nil, err
			}
			stemIn = &stemInTmp
		default:
			o, err := wOut.VID.OutputWithIDAt(wOut.Index)
			if err != nil {
				return nil, err
			}
			otherInputs = append(otherInputs, &o)
			outputs, err := cmdParser.ParseSequencerCommandToOutput(&o)
			if err != nil {
				a.Tracef(TraceTagIncrementalAttacher, "error while parsing input: %v", err)
			} else {
				additionalOutputs = append(additionalOutputs, outputs...)
			}
		}
	}

	endorsements := make([]*ledger.TransactionID, len(a.endorse))
	for i, vid := range a.endorse {
		endorsements[i] = &vid.ID
	}
	txBytes, inputLoader, err := txbuilder.MakeSequencerTransactionWithInputLoader(txbuilder.MakeSequencerTransactionParams{
		SeqName:           seqName,
		ChainInput:        chainIn.MustAsChainOutput(),
		StemInput:         stemIn,
		Timestamp:         a.targetTs,
		AdditionalInputs:  otherInputs,
		AdditionalOutputs: additionalOutputs,
		Endorsements:      endorsements,
		PrivateKey:        privateKey,
		PutInflation:      true,
		ReturnInputLoader: true,
	})
	if err != nil {
		return nil, err
	}
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		if tx != nil {
			err = fmt.Errorf("%w:\n%s", err, tx.ToStringWithInputLoaderByIndex(inputLoader))
		}
		a.Log().Fatalf("IncrementalAttacher.MakeSequencerTransaction: %v", err) // should produce correct transaction
		//return nil, err
	}
	a.slotInflation = a.pastCone.CalculateSlotInflation()
	// in the incremental attacher we must add inflation on the branch
	a.slotInflation += tx.InflationAmount()

	//a.Log().Infof("\n>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\n%s\n<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<", a.dumpLines().String())
	return tx, nil
}

func (a *IncrementalAttacher) TargetTs() ledger.Time {
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
	return a.inputs[0]
}

func (a *IncrementalAttacher) Endorsing() []*vertex.WrappedTx {
	return a.endorse
}
