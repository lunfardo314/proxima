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
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
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

	var baseline *vertex.WrappedTx
	if targetTs.Tick() == 0 {
		// target is branch
		env.Assertf(len(endorse) == 0, "NewIncrementalAttacher: len(endorse)==0")
		if !extend.VID.IsSequencerMilestone() {
			return nil, fmt.Errorf("NewIncrementalAttacher %s: cannot extend non-sequencer transaction %s into a branch",
				name, extend.VID)
		}
		baseline = extend.VID
	} else {
		// target is not branch
		if extend.Slot() != targetTs.Slot() {
			// cross-slot, must have endorsement
			if len(endorse) > 0 {
				baseline = endorse[0]
			}
		} else {
			// same slot
			baseline = extend.VID
		}
	}
	if baseline == nil {
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to determine baseline branch of %s",
			name, extend.IDShortString())
	}
	if baseline.BaselineBranch() == nil {
		// may happen when baseline is virtualTx
		return nil, fmt.Errorf("NewIncrementalAttacher %s: failed to determine valid baseline branch of %s. BaselineBranch(%s) == nil",
			name, extend.IDShortString(), baseline.IDShortString())
	}

	ret := &IncrementalAttacher{
		attacher: newPastConeAttacher(env, name),
		endorse:  make([]*vertex.WrappedTx, 0),
		inputs:   make([]vertex.WrappedOutput, 0),
		targetTs: targetTs,
	}
	if err := ret.initIncrementalAttacher(baseline.BaselineBranch(), targetTs, extend, endorse...); err != nil {
		ret.unReferenceAllByAttacher()
		return nil, err
	}
	return ret, nil
}

func (a *IncrementalAttacher) UnReferenceAll() {
	a.unReferenceAllByAttacher()
}

func (a *IncrementalAttacher) initIncrementalAttacher(baseline *vertex.WrappedTx, targetTs ledger.Time, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) error {
	// also fetches baseline coverage
	if !a.setBaseline(baseline, targetTs) {
		return fmt.Errorf("NewIncrementalAttacher: failed to set baseline branch of %s", extend.IDShortString())
	}
	a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). baseline: %s, start with coverage: %s",
		a.name, baseline.IDShortString,
		func() string { return util.Th(a.coverage) })

	// attach endorsements
	for _, endorsement := range endorse {
		a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertEndorsement: %s", a.name, endorsement.IDShortString)
		if err := a.insertEndorsement(endorsement); err != nil {
			return err
		}
	}
	// extend input will always be at index 0
	if err := a.insertOutput(extend); err != nil {
		return err
	}

	if targetTs.Tick() == 0 {
		// stem input, if any, will be at index 1
		// for branches, include stem input
		a.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertStemInput", a.name)
		a.stemOutput = a.GetStemWrappedOutput(&baseline.ID)
		if a.stemOutput.VID == nil {
			return fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", baseline.IDShortString())
		}
		if !a.markReferencedByAttacher(a.stemOutput.VID) {
			return fmt.Errorf("NewIncrementalAttacher: failed to reference stem output %s", a.stemOutput.IDShortString())
		}
		if err := a.insertOutput(a.stemOutput); err != nil {
			return err
		}
	}
	return nil
}

func (a *IncrementalAttacher) BaselineBranch() *vertex.WrappedTx {
	return a.baseline
}

func (a *IncrementalAttacher) insertOutput(wOut vertex.WrappedOutput) error {
	if a.isKnownConsumed(wOut) {
		return fmt.Errorf("output %s is already consumed", wOut.IDShortString())
	}
	ok, defined := a.attachOutput(wOut)
	if !ok {
		util.AssertMustError(a.err)
		return a.err
	}
	if !defined {
		return fmt.Errorf("insertOutput: %w", ErrPastConeNotSolidYet)
	}
	if !a.markReferencedByAttacher(wOut.VID) {
		return fmt.Errorf("insertOutput: failed to reference output %s", wOut.IDShortString())
	}
	a.inputs = append(a.inputs, wOut)
	return nil
}

// saving attacher state to be able to restore in case it becomes inconsistent when
// attempting to adding conflicting outputs or endorsements

type _savedState struct {
	vertices map[*vertex.WrappedTx]Flags
	rooted   map[*vertex.WrappedTx]set.Set[byte]
	coverage uint64
}

func (a *IncrementalAttacher) _saveState() *_savedState {
	ret := &_savedState{
		vertices: maps.Clone(a.attacher.vertices),
		rooted:   maps.Clone(a.attacher.rooted),
		coverage: a.coverage,
	}
	// deep clone
	for vid, outputIdxSet := range ret.rooted {
		ret.rooted[vid] = outputIdxSet.Clone()
	}
	return ret
}

func (a *IncrementalAttacher) _restoreState(saved *_savedState) {
	a.attacher.vertices = saved.vertices
	a.attacher.rooted = saved.rooted
	a.coverage = saved.coverage
}

// InsertEndorsement preserves consistency in case of failure
func (a *IncrementalAttacher) InsertEndorsement(endorsement *vertex.WrappedTx) error {
	saved := a._saveState()
	if err := a.insertEndorsement(endorsement); err != nil {
		a._restoreState(saved)
		a.setError(nil)
		return err
	}
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
		a.markVertexDefined(endorsement)
		a.mustMarkVertexRooted(endorsement)
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
	if !a.markReferencedByAttacher(endorsement) {
		return fmt.Errorf("insertEndorsement: failed to reference endorsement %s", endorsement.IDShortString())
	}
	a.endorse = append(a.endorse, endorsement)
	return nil
}

// InsertTagAlongInput inserts tag along input.
// In case of failure return false and attacher state remains consistent
func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput) (bool, error) {
	util.AssertNoError(a.err)

	// too verbose even for tracing
	//a.TraceTx(&wOut.VID.ID, "%s::InsertTagAlongInput #%d", a.name, wOut.Index)

	// save state for possible rollback because in case of fail the side effect makes attacher inconsistent
	// TODO a better way than cloning potentially big maps with each new input?
	saved := a._saveState()
	//saveVertices := maps.Clone(a.attacher.vertices)
	//saveRooted := maps.Clone(a.attacher.rooted)
	//for vid, outputIdxSet := range saveRooted {
	//	saveRooted[vid] = outputIdxSet.Clone()
	//}
	//saveCoverageDelta := a.coverage

	ok, defined := a.attachOutput(wOut)
	if !ok || !defined {
		// it is either conflicting, or not solid yet
		// in either case rollback
		a._restoreState(saved)
		//a.attacher.vertices = saveVertices
		//a.attacher.rooted = saveRooted
		//a.coverage = saveCoverageDelta
		var retErr error
		if !ok {
			retErr = a.err
		} else if !defined {
			retErr = fmt.Errorf("InsertTagAlongInput: %w", ErrPastConeNotSolidYet)
		}

		//{ // very verbose even for tracing
		//	a.TraceTx(&wOut.VID.ID, "%s::InsertTagAlongInput failed to insert #%d: %v", a.name, wOut.Index, retErr)
		//}

		a.setError(nil)
		return false, retErr
	}
	a.inputs = append(a.inputs, wOut)
	util.AssertNoError(a.err)
	//a.TraceTx(&wOut.VID.ID, "%s::InsertTagAlongInput #%d: success", a.name, wOut.Index)
	return true, nil
}

// MakeSequencerTransaction creates sequencer transaction from the incremental attacher.
// Increments slotInflation by the amount inflated in the transaction
func (a *IncrementalAttacher) MakeSequencerTransaction(seqName string, privateKey ed25519.PrivateKey, cmdParser SequencerCommandParser) (*transaction.Transaction, error) {
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
	a.calculateSlotInflation()
	// in the incremental attacher we must add inflation on the branch
	a.slotInflation += tx.InflationAmount()

	//a.Log().Infof("\n>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\n%s\n<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<", a.dumpLines().String())
	return tx, nil
}

func (a *IncrementalAttacher) AdjustCoverage() {
	a.adjustCoverage()
	if a.coverageAdjustment > 0 {
		ext := a.Extending()
		a.Tracef(TraceTagCoverageAdjustment, " IncrementalAttacher: coverage has been adjusted by %s, extending: %s, baseline: %s",
			func() string { return util.Th(a.coverageAdjustment) }, ext.IDShortString, a.baseline.IDShortString)
	}
}

func (a *IncrementalAttacher) LedgerCoverage() uint64 {
	return a.coverage
}

func (a *IncrementalAttacher) TargetTs() ledger.Time {
	return a.targetTs
}

func (a *IncrementalAttacher) NumInputs() int {
	return len(a.inputs) + 2
}

// Completed returns true is past cone all solid and consistent (no conflicts)
func (a *IncrementalAttacher) Completed() bool {
	return !a.containsUndefinedExcept(nil) && len(a.rooted) > 0
}

func (a *IncrementalAttacher) Extending() vertex.WrappedOutput {
	return a.inputs[0]
}

func (a *IncrementalAttacher) Endorsing() []*vertex.WrappedTx {
	return a.endorse
}
