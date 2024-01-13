package attacher

import (
	"fmt"
	"slices"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

type (
	IncrementalAttacher struct {
		pastConeAttacher
		extend         *vertex.WrappedTx
		endorse        []*vertex.WrappedTx
		tagAlongInputs []vertex.WrappedOutput
		targetTs       ledger.LogicalTime
		seqOutput      vertex.WrappedOutput
		stemOutput     vertex.WrappedOutput
	}
)

func NewIncrementalAttacher(name string, env Environment, targetTs ledger.LogicalTime, extend *vertex.WrappedTx, endorse ...*vertex.WrappedTx) (*IncrementalAttacher, error) {
	util.Assertf(extend.IsSequencerMilestone(), "extend.IsSequencerMilestone()")
	util.Assertf(ledger.ValidTimePace(extend.Timestamp(), targetTs), "ledger.ValidTimePace(extend.Timestamp(), targetTs)")
	for _, vid := range endorse {
		util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
		util.Assertf(ledger.ValidTimePace(vid.Timestamp(), targetTs), "ledger.ValidTimePace(vid.Timestamp(), targetTs)")
	}

	all := append(slices.Clone(endorse), extend)
	latest := util.Maximum(all, func(vid1, vid2 *vertex.WrappedTx) bool {
		return vid1.Timestamp().Before(vid2.Timestamp())
	})
	baseline := latest.BaselineBranch()
	if baseline == nil {
		return nil, fmt.Errorf("NewIncrementalAttacher: failed to determine the baseline branch of %s", extend.IDShortString())
	}
	ret := &IncrementalAttacher{
		pastConeAttacher: newPastConeAttacher(env, name),
		extend:           extend,
		endorse:          slices.Clone(endorse),
		tagAlongInputs:   make([]vertex.WrappedOutput, 0),
		targetTs:         targetTs,
	}
	ret.baselineBranch = baseline

	// attach sequencer predecessor
	visited := set.New[*vertex.WrappedTx]()
	extend.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret.attachVertex(v, extend, ledger.NilLogicalTime, visited)
		},
	})
	if ret.reason != nil {
		return nil, ret.reason
	}
	// attach endorsements
	for _, endorsement := range endorse {
		if err := ret.insertEndorsement(endorsement, visited); err != nil {
			return nil, err
		}
	}
	if err := ret.insertSequencerInput(visited); err != nil {
		return nil, err
	}
	if targetTs.Tick() == 0 {
		if err := ret.insertStemInput(visited); err != nil {
			return nil, err
		}
	}
	return ret, nil
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
		return fmt.Errorf("sequencer output is not available in %s", a.extend.IDShortString())
	}
	if !a.attachOutput(a.seqOutput, ledger.NilLogicalTime, visited) {
		return a.reason
	}
	return nil
}

func (a *IncrementalAttacher) insertStemInput(visited set.Set[*vertex.WrappedTx]) error {
	a.stemOutput = a.baselineBranch.StemWrappedOutput()
	if a.stemOutput.VID == nil {
		return fmt.Errorf("stem output is not available for baseline %s", a.baselineBranch.IDShortString())
	}
	if !a.attachOutput(a.stemOutput, ledger.NilLogicalTime, visited) {
		return a.reason
	}
	return nil
}

// InsertTagAlongInput inserts tag along input.
// In case of failure return false with attacher state consistent
func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput, visited set.Set[*vertex.WrappedTx]) bool {
	// save state for rollback because in case of fail the side effect makes attacher inconsistent
	// TODO is there a better way than cloning potentially big maps?
	saveUndefinedPastVertices := a.pastConeAttacher.undefinedPastVertices.Clone()
	saveValidPastVertices := a.pastConeAttacher.validPastVertices.Clone()
	saveRooted := maps.Clone(a.pastConeAttacher.rooted)
	for vid, outputIdxSet := range saveRooted {
		saveRooted[vid] = outputIdxSet.Clone()
	}
	if !a.attachOutput(wOut, ledger.NilLogicalTime, visited) {
		// rollback
		a.pastConeAttacher.undefinedPastVertices = saveUndefinedPastVertices
		a.pastConeAttacher.validPastVertices = saveValidPastVertices
		a.pastConeAttacher.rooted = saveRooted
		return false
	}
	a.tagAlongInputs = append(a.tagAlongInputs, wOut)
	return true
}

func (a *IncrementalAttacher) MakeTransaction() (*transaction.Transaction, error) {
	return nil, nil
}

func (a *IncrementalAttacher) NumInputs() int {
	return len(a.tagAlongInputs) + 2
}