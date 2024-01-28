package attacher

import (
	"crypto/ed25519"
	"fmt"
	"slices"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

const TraceTagIncrementalAttacher = "incAttach"

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

	ret.setBaseline(baseline, targetTs) // also fetches baseline baselineCoverage

	// attach sequencer predecessor
	if !ret.attachOutput(ret.extend, ledger.NilLogicalTime) {
		return nil, ret.reason
	}
	// attach endorsements
	for _, endorsement := range endorse {
		env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertEndorsement: %s", name, endorsement.IDShortString)
		if err := ret.insertEndorsement(endorsement); err != nil {
			return nil, err
		}
	}
	if targetTs.Tick() == 0 {
		// for branches, include stem input
		env.Tracef(TraceTagIncrementalAttacher, "NewIncrementalAttacher(%s). insertStemInput", name)
		ret.stemOutput = baseline.StemWrappedOutput()
		if ret.stemOutput.VID == nil {
			return nil, fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", baseline.IDShortString())
		}
		if !ret.attachOutput(ret.stemOutput, ledger.NilLogicalTime) {
			return nil, ret.reason
		}
	}
	return ret, nil
}

func (a *IncrementalAttacher) BaselineBranch() *vertex.WrappedTx {
	return a.baselineBranch
}

func (a *IncrementalAttacher) insertEndorsement(endorsement *vertex.WrappedTx) error {
	if endorsement.IsBadOrDeleted() {
		return fmt.Errorf("NewIncrementalAttacher: can't endorse %s. Reason: '%s'", endorsement.IDShortString(), endorsement.GetReason())
	}
	endBaseline := endorsement.BaselineBranch()
	if !a.branchesCompatible(a.baselineBranch, endBaseline) {
		return fmt.Errorf("baseline branch %s of the endorsement branch %s is incompatible with the baseline %s",
			endBaseline.IDShortString(), endorsement.IDShortString(), a.baselineBranch.IDShortString())
	}

	endorsement.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		a.attachVertexUnwrapped(v, endorsement, ledger.NilLogicalTime)
	}})
	return a.reason
}

// InsertTagAlongInput inserts tag along input.
// In case of failure return false and attacher state consistent
func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput, visited set.Set[*vertex.WrappedTx]) (bool, error) {
	// save state for possible rollback because in case of fail the side effect makes attacher inconsistent
	// TODO a better way than cloning potentially big maps with each new input?
	util.AssertNoError(a.reason)

	saveUndefinedPastVertices := a.attacher.undefinedPastVertices.Clone()
	saveValidPastVertices := a.attacher.definedPastVertices.Clone()
	saveRooted := maps.Clone(a.attacher.rooted)
	for vid, outputIdxSet := range saveRooted {
		saveRooted[vid] = outputIdxSet.Clone()
	}
	saveCoverageDelta := a.coverage

	if !a.attachOutput(wOut, ledger.NilLogicalTime) || !a.Completed() {
		// it is either conflicting, or not solid yet
		// in either case rollback
		a.attacher.undefinedPastVertices = saveUndefinedPastVertices
		a.attacher.definedPastVertices = saveValidPastVertices
		a.attacher.rooted = saveRooted
		a.coverage = saveCoverageDelta
		retReason := a.GetReason()
		a.setReason(nil)
		return false, retReason
	}
	a.tagAlongInputs = append(a.tagAlongInputs, wOut)
	util.AssertNoError(a.GetReason())
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
		panic(err) // should produce correct transaction
		//return nil, err
	}
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
	if done = len(a.undefinedPastVertices) == 0 && len(a.rooted) > 0; done {
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
