package attacher

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"

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

func NewIncrementalAttacher(name string, env Environment, targetTs ledger.LogicalTime, extend vertex.WrappedOutput, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	util.Assertf(ledger.ValidTimePace(extend.Timestamp(), targetTs), "ledger.ValidTimePace(extend.Timestamp(), targetTs)")
	for _, endorseVID := range endorse {
		util.Assertf(endorseVID.IsSequencerMilestone(), "NewIncrementalAttacher: endorseVID.IsSequencerMilestone()")
		util.Assertf(targetTs.Slot() == endorseVID.Slot(), "NewIncrementalAttacher: targetTs.Slot() == endorseVid.Slot()")
		util.Assertf(ledger.ValidTimePace(endorseVID.Timestamp(), targetTs), "NewIncrementalAttacher: ledger.ValidTimePace(endorseVID.Timestamp(), targetTs)")
	}
	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). extend: %s, endorse: {%s}",
		name, extend.IDShortString, func() string { return vertex.VerticesLines(endorse).Join(",") })

	var baseline *vertex.WrappedTx

	if targetTs.Tick() == 0 {
		// target is branch
		util.Assertf(len(endorse) == 0, "NewIncrementalAttacher: len(endorse)==0")
		baseline = extend.VID.BaselineBranch()
	} else {
		// target is not branch
		if extend.Slot() != targetTs.Slot() {
			// cross-slot, must have endorsement
			if len(endorse) > 0 {
				baseline = endorse[0].BaselineBranch()
			}
		} else {
			// same slot
			baseline = extend.VID.BaselineBranch()
		}
	}
	if baseline == nil {
		return nil, fmt.Errorf("NewIncrementalAttacher: failed to determine the baseline branch of %s", extend.IDShortString())
	}

	env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). baseline: %s", name, baseline.IDShortString)

	ret := &IncrementalAttacher{
		attacher:       newPastConeAttacher(env, name),
		extend:         extend,
		endorse:        slices.Clone(endorse),
		tagAlongInputs: make([]vertex.WrappedOutput, 0),
		targetTs:       targetTs,
	}

	ret.setBaseline(baseline, targetTs) // also fetches baseline coverage

	// attach endorsements
	for _, endorsement := range endorse {
		env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertEndorsement: %s", name, endorsement.IDShortString)
		if err := ret.insertEndorsement(endorsement); err != nil {
			return nil, err
		}
	}
	if len(endorse) > 0 {
		util.Assertf(len(ret.rooted) > 0, "NewIncrementalAttacher: len(ret.rooted)>0:\n%s", func() string { return ret.dumpLines("      ").String() })
	}
	isRootedAlongEndorsements := ret.isRootedOutput(ret.extend)

	if ret.extend.VID.IsBranchTransaction() {
		fmt.Printf(">>>>>>>>>>>>>> NewIncrementalAttacher(%s): extending branch output %s -> isRooted along endorsements: %v\n",
			name, ret.extend.IDShortString(), isRootedAlongEndorsements)
	}
	// attach sequencer predecessor
	ok, defined := ret.attachOutput(ret.extend, ledger.NilLogicalTime)
	util.Assertf(!isRootedAlongEndorsements || !ok, "NewIncrementalAttacher(%s): attaching output %s expected to fail", name, ret.extend.IDShortString)

	if !ok {
		util.Assertf(ret.err != nil, "ret.err != nil")
		return nil, ret.err
	}
	if !defined {
		return nil, ErrPastConeNotSolidYet
	}
	if targetTs.Tick() == 0 {
		// for branches, include stem input
		env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertStemInput", name)
		ret.stemOutput = baseline.StemWrappedOutput()
		if ret.stemOutput.VID == nil {
			return nil, fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", baseline.IDShortString())
		}
		ok, defined = ret.attachOutput(ret.stemOutput, ledger.NilLogicalTime)
		if !ok {
			util.Assertf(ret.err != nil, "ret.err != nil")
			return nil, ret.err
		}
		if !defined {
			return nil, ErrPastConeNotSolidYet
		}
	}
	return ret, nil
}

func (a *IncrementalAttacher) BaselineBranch() *vertex.WrappedTx {
	return a.baseline
}

func (a *IncrementalAttacher) insertEndorsement(endorsement *vertex.WrappedTx) error {
	if endorsement.IsBadOrDeleted() {
		return fmt.Errorf("NewIncrementalAttacher: can't endorse %s. Reason: '%s'", endorsement.IDShortString(), endorsement.GetError())
	}
	endBaseline := endorsement.BaselineBranch()
	if !a.branchesCompatible(a.baseline, endBaseline) {
		return fmt.Errorf("baseline branch %s of the endorsement branch %s is incompatible with the baseline %s",
			endBaseline.IDShortString(), endorsement.IDShortString(), a.baseline.IDShortString())
	}
	if endorsement.IsBranchTransaction() {
		// branch is compatible with the baseline
		a.markVertexDefined(endorsement)
		a.markVertexRooted(endorsement)
		return nil
	}
	ok, defined := a.attachVertexNonBranch(endorsement, ledger.NilLogicalTime)
	util.Assertf(ok || a.err != nil, "ok || a.err != nil")
	if !ok {
		util.Assertf(a.err != nil, "a.err != nil")
		return a.err
	}
	util.Assertf(a.err == nil, "a.err == nil")
	if !defined {
		return ErrPastConeNotSolidYet
	}
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

	ok, defined := a.attachOutput(wOut, ledger.NilLogicalTime)
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
	a.tagAlongInputs = append(a.tagAlongInputs, wOut)
	util.AssertNoError(a.err)
	return true, nil
}

func (a *IncrementalAttacher) MakeTransaction(seqName string, privateKey ed25519.PrivateKey) (*transaction.Transaction, error) {
	chainIn, err := a.extend.VID.OutputWithIDAt(a.extend.Index)
	if err != nil {
		return nil, err
	}
	var stemIn *ledger.OutputWithID
	if a.targetTs.Tick() == 0 {
		var stemInTmp ledger.OutputWithID
		stemInTmp, err = a.stemOutput.VID.OutputWithIDAt(a.stemOutput.Index)
		stemIn = &stemInTmp
	}
	tagAlongInputs := make([]*ledger.OutputWithID, len(a.tagAlongInputs))

	for i, wOut := range a.tagAlongInputs {
		o, err := wOut.VID.OutputWithIDAt(wOut.Index)
		if err != nil {
			return nil, err
		}
		tagAlongInputs[i] = &o
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
		AdditionalInputs:  tagAlongInputs,
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
		a.Log().Fatalf("IncrementalAttacher.MakeTransaction: %v", err) // should produce correct transaction
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

func (a *IncrementalAttacher) TargetTs() ledger.LogicalTime {
	return a.targetTs
}

func (a *IncrementalAttacher) NumInputs() int {
	return len(a.tagAlongInputs) + 2
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
	return a.extend
}

func (a *IncrementalAttacher) Endorsing() []*vertex.WrappedTx {
	return a.endorse
}
