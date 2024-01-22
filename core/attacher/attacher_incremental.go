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

func NewIncrementalAttacher(name string, env Environment, targetTs ledger.LogicalTime, extend *vertex.WrappedTx, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	util.Assertf(extend.IsSequencerMilestone(), "extend.IsSequencerMilestone()")
	util.Assertf(ledger.ValidTimePace(extend.Timestamp(), targetTs), "ledger.ValidTimePace(extend.Timestamp(), targetTs)")
	for _, vid := range endorse {
		util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
		util.Assertf(ledger.ValidTimePace(vid.Timestamp(), targetTs), "ledger.ValidTimePace(vid.Timestamp(), targetTs)")
	}
	env.Tracef("incAttach", "NewIncrementalAttacher(%s). extend: %s, endorse: {%s}",
		name, extend.IDShortString, func() string { return vertex.VerticesLines(endorse).Join(",") })

	// find baseline branch as the baseline branch of the latest among extend and endorsements
	latest := util.Maximum(append(slices.Clone(endorse), extend), func(vid1, vid2 *vertex.WrappedTx) bool {
		return vid1.Timestamp().Before(vid2.Timestamp())
	})
	baseline := latest.BaselineBranch()
	if baseline == nil {
		return nil, fmt.Errorf("NewIncrementalAttacher: failed to determine the baseline branch of %s", extend.IDShortString())
	}
	env.Tracef("incAttach", "NewIncrementalAttacher(%s). baseline: %s", name, baseline.IDShortString)

	ret := &IncrementalAttacher{
		attacher:       newPastConeAttacher(env, name),
		extend:         extend,
		endorse:        slices.Clone(endorse),
		tagAlongInputs: make([]vertex.WrappedOutput, 0),
		targetTs:       targetTs,
	}

	ret.setBaselineBranch(baseline) // also fetches previous coverage

	visited := set.New[*vertex.WrappedTx]()

	// attach sequencer predecessor
	if !extend.IsBranchTransaction() {
		extend.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				env.Tracef("incAttach", "NewIncrementalAttacher(%s). attachVertex: %s", name, baseline.IDShortString)
				ret.attachVertex(v, extend, ledger.NilLogicalTime, visited)
			},
		})
		if ret.reason != nil {
			return nil, ret.reason
		}
	}
	// attach endorsements
	for _, endorsement := range endorse {
		env.Tracef("incAttach", "NewIncrementalAttacher(%s). insertEndorsement: %s", name, endorsement.IDShortString)
		if err := ret.insertEndorsement(endorsement, visited); err != nil {
			return nil, err
		}
	}
	env.Tracef("incAttach", "NewIncrementalAttacher(%s). insertSequencerInput", name)
	if err := ret.insertSequencerInput(visited); err != nil {
		return nil, err
	}
	if targetTs.Tick() == 0 {
		env.Tracef("incAttach", "NewIncrementalAttacher(%s). insertStemInput", name)
		if err := ret.insertStemInput(visited); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (a *IncrementalAttacher) BaselineBranch() *vertex.WrappedTx {
	return a.baselineBranch
}

func (a *IncrementalAttacher) insertEndorsement(endorsement *vertex.WrappedTx, visited set.Set[*vertex.WrappedTx]) error {
	if endorsement.IsBadOrDeleted() {
		return fmt.Errorf("NewIncrementalAttacher: can't endorse %s. Reason: '%s'", endorsement.IDShortString(), endorsement.GetReason())
	}
	endorsement.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		a.attachVertex(v, endorsement, ledger.NilLogicalTime, visited)
	}})
	return a.reason
}

func (a *IncrementalAttacher) insertSequencerInput(visited set.Set[*vertex.WrappedTx]) error {
	a.seqOutput = a.extend.SequencerWrappedOutput()
	if a.seqOutput.VID == nil {
		return fmt.Errorf("NewIncrementalAttacher: sequencer output is not available in %s", a.extend.IDShortString())
	}
	if !a.attachOutput(a.seqOutput, ledger.NilLogicalTime, visited) {
		return a.reason
	}
	return nil
}

func (a *IncrementalAttacher) insertStemInput(visited set.Set[*vertex.WrappedTx]) error {
	a.stemOutput = a.baselineBranch.StemWrappedOutput()
	if a.stemOutput.VID == nil {
		return fmt.Errorf("NewIncrementalAttacher: stem output is not available for baseline %s", a.baselineBranch.IDShortString())
	}
	if !a.attachOutput(a.stemOutput, ledger.NilLogicalTime, visited) {
		return a.reason
	}
	return nil
}

// InsertTagAlongInput inserts tag along input.
// In case of failure return false and attacher state consistent
func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput, visited set.Set[*vertex.WrappedTx]) (bool, error) {
	// save state for possible rollback because in case of fail the side effect makes attacher inconsistent
	// TODO a better way than cloning potentially big maps with each new input?
	saveUndefinedPastVertices := a.attacher.undefinedPastVertices.Clone()
	saveValidPastVertices := a.attacher.validPastVertices.Clone()
	saveRooted := maps.Clone(a.attacher.rooted)
	for vid, outputIdxSet := range saveRooted {
		saveRooted[vid] = outputIdxSet.Clone()
	}
	saveCoverageDelta := a.coverageDelta

	if !a.attachOutput(wOut, ledger.NilLogicalTime, visited) || !a.Completed() {
		// it is either conflicting, or not solid yet
		// in either case rollback
		a.attacher.undefinedPastVertices = saveUndefinedPastVertices
		a.attacher.validPastVertices = saveValidPastVertices
		a.attacher.rooted = saveRooted
		a.coverageDelta = saveCoverageDelta
		return false, a.GetReason()
	}
	a.tagAlongInputs = append(a.tagAlongInputs, wOut)
	util.AssertNoError(a.GetReason())
	return true, nil
}

func (a *IncrementalAttacher) MakeTransaction(seqName string, privateKey ed25519.PrivateKey) (*transaction.Transaction, error) {
	chainIn, err := a.seqOutput.VID.OutputWithIDAt(a.seqOutput.Index)
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
	return a.ledgerCoverage(a.targetTs)
}

func (a *IncrementalAttacher) LedgerCoverageSum() uint64 {
	ret := a.LedgerCoverage()
	return ret.Sum()
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
		util.Assertf(a.coverageDelta > 0, "a.coverageDelta) > 0")
	}
	return
}

func (a *IncrementalAttacher) Extending() *vertex.WrappedTx {
	return a.extend
}

func (a *IncrementalAttacher) Endorsing() []*vertex.WrappedTx {
	return a.endorse
}

func (a *IncrementalAttacher) ExtendedOutput() vertex.WrappedOutput {
	return a.seqOutput
}
