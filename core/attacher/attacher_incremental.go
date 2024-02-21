package attacher

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/exp/maps"
)

const TraceTagIncrementalAttacher = "incAttach"

var ErrPastConeNotSolidYet = errors.New("past cone not solid yet")

func NewIncrementalAttacher(name string, env Environment, targetTs ledger.Time, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	util.Assertf(ledger.ValidSequencerPace(extend.Timestamp(), targetTs), "NewIncrementalAttacher: target is closer than allowed pace (%d): %s -> %s",
		ledger.TransactionPaceSequencer(), extend.Timestamp().String, targetTs.String)

	for _, endorseVID := range endorse {
		util.Assertf(endorseVID.IsSequencerMilestone(), "NewIncrementalAttacher: endorseVID.IsSequencerMilestone()")
		util.Assertf(targetTs.Slot() == endorseVID.Slot(), "NewIncrementalAttacher: targetTs.Slot() == endorseVid.Slot()")
		util.Assertf(ledger.ValidTransactionPace(endorseVID.Timestamp(), targetTs), "NewIncrementalAttacher: ledger.ValidTransactionPace(endorseVID.Timestamp(), targetTs)")
	}
	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). extend: %s, endorse: {%s}",
		name, extend.IDShortString, func() string { return vertex.VerticesLines(endorse).Join(",") })

	var baseline *vertex.WrappedTx

	if targetTs.Tick() == 0 {
		// target is branch
		util.Assertf(len(endorse) == 0, "NewIncrementalAttacher: len(endorse)==0")
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
		return nil, fmt.Errorf("NewIncrementalAttacher: failed to determine the baseline branch of %s", extend.IDShortString())
	}

	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). baseline: %s", name, baseline.IDShortString)

	ret := &IncrementalAttacher{
		attacher: newPastConeAttacher(env, name),
		endorse:  make([]*vertex.WrappedTx, 0),
		inputs:   make([]vertex.WrappedOutput, 0),
		targetTs: targetTs,
	}

	ret.setBaseline(baseline, targetTs) // also fetches baseline coverage

	// attach endorsements
	for _, endorsement := range endorse {
		env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertEndorsement: %s", name, endorsement.IDShortString)
		if err := ret.insertEndorsement(endorsement); err != nil {
			return nil, err
		}
	}
	// extend input will always be at index 0
	if err := ret.insertOutput(extend); err != nil {
		return nil, err
	}

	if targetTs.Tick() == 0 {
		// stem input, if any, will be at index 1
		// for branches, include stem input
		env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertStemInput", name)
		ret.stemOutput = env.GetStemWrappedOutput(&baseline.ID)
		if ret.stemOutput.VID == nil {
			return nil, fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", baseline.IDShortString())
		}
		if err := ret.insertOutput(ret.stemOutput); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (a *IncrementalAttacher) BaselineBranch() *vertex.WrappedTx {
	return a.baseline
}

func (a *IncrementalAttacher) insertOutput(wOut vertex.WrappedOutput) error {
	if a.isKnownConsumed(wOut) {
		return fmt.Errorf("output %s is already consumed", wOut.IDShortString())
	}
	ok, defined := a.attachOutput(wOut, ledger.NilLedgerTime)
	if !ok {
		util.AssertMustError(a.err)
		return a.err
	}
	if !defined {
		return ErrPastConeNotSolidYet
	}
	a.inputs = append(a.inputs, wOut)
	return nil
}

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
		a.markVertexRooted(endorsement)
	} else {
		ok, defined := a.attachVertexNonBranch(endorsement, ledger.NilLedgerTime)
		util.Assertf(ok || a.err != nil, "ok || a.err != nil")
		if !ok {
			util.Assertf(a.err != nil, "a.err != nil")
			return a.err
		}
		util.Assertf(a.err == nil, "a.err == nil")
		if !defined {
			return ErrPastConeNotSolidYet
		}
	}
	a.endorse = append(a.endorse, endorsement)
	return nil
}

// InsertTagAlongInput inserts tag along input.
// In case of failure return false and attacher state consistent
func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput) (bool, error) {
	// save state for possible rollback because in case of fail the side effect makes attacher inconsistent
	// TODO a better way than cloning potentially big maps with each new input?
	util.AssertNoError(a.err)

	saveVertices := maps.Clone(a.attacher.vertices)
	saveRooted := maps.Clone(a.attacher.rooted)
	for vid, outputIdxSet := range saveRooted {
		saveRooted[vid] = outputIdxSet.Clone()
	}
	saveCoverageDelta := a.coverage

	ok, defined := a.attachOutput(wOut, ledger.NilLedgerTime)
	if !ok || !defined {
		// it is either conflicting, or not solid yet
		// in either case rollback
		a.attacher.vertices = saveVertices
		a.attacher.rooted = saveRooted
		a.coverage = saveCoverageDelta
		var retErr error
		if !ok {
			retErr = a.err
		} else {
			if !defined {
				retErr = ErrPastConeNotSolidYet
			}
		}
		a.setError(nil)
		return false, retErr
	}
	a.inputs = append(a.inputs, wOut)
	util.AssertNoError(a.err)
	return true, nil
}

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
	//a.Log().Infof("\n>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\n%s\n<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<", a.dumpLines().String())
	return tx, nil
}

func (a *IncrementalAttacher) LedgerCoverage() multistate.LedgerCoverage {
	return a.coverage
}

func (a *IncrementalAttacher) LedgerCoverageSum() uint64 {
	return a.coverage.Sum()
}

func (a *IncrementalAttacher) TargetTs() ledger.Time {
	return a.targetTs
}

func (a *IncrementalAttacher) NumInputs() int {
	return len(a.inputs) + 2
}

// Completed returns true is past cone all solid and consistent (no conflicts)
func (a *IncrementalAttacher) Completed() (done bool) {
	done = !a.containsUndefinedExcept(nil) && len(a.rooted) > 0
	if done {
		util.Assertf(a.coverage.LatestDelta() > 0, "a.coverage.LatestDelta() > 0")
	}
	return
}

func (a *IncrementalAttacher) Extending() vertex.WrappedOutput {
	return a.inputs[0]
}

func (a *IncrementalAttacher) Endorsing() []*vertex.WrappedTx {
	return a.endorse
}
