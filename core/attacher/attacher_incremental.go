package attacher

import (
	"fmt"
	"slices"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	IncrementalAttacher struct {
		pastConeAttacher
		extend   *vertex.WrappedTx
		endorse  []*vertex.WrappedTx
		inputs   []vertex.WrappedOutput
		targetTs ledger.LogicalTime
	}
)

func NewIncrementalAttacher(name string, env Environment, targetTs ledger.LogicalTime, extend *vertex.WrappedTx, endorse ...*vertex.WrappedTx) *IncrementalAttacher {
	util.Assertf(extend.IsSequencerMilestone(), "extend.IsSequencerMilestone()")
	util.Assertf(ledger.ValidTimePace(extend.Timestamp(), targetTs), "ledger.ValidTimePace(extend.Timestamp(), targetTs)")
	for _, vid := range endorse {
		util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
		util.Assertf(ledger.ValidTimePace(vid.Timestamp(), targetTs), "ledger.ValidTimePace(vid.Timestamp(), targetTs)")
	}

	ret := &IncrementalAttacher{
		pastConeAttacher: newPastConeAttacher(env, name),
		extend:           extend,
		endorse:          slices.Clone(endorse),
		inputs:           make([]vertex.WrappedOutput, 0),
		targetTs:         targetTs,
	}
	ret.baselineBranch = extend.BaselineBranch()
	if ret.baselineBranch == nil {
		ret.reason = fmt.Errorf("NewIncrementalAttacher: failed to determine the baseline branch of %s", extend.IDShortString())
		return nil
	}
	// attach sequencer predecessor
	visited := set.New[*vertex.WrappedTx]()
	extend.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret.attachVertex(v, extend, ledger.NilLogicalTime, visited)
		},
	})
	if ret.reason != nil {
		return nil
	}
	// attach endorsements
	for _, endorsement := range endorse {
		ret.InsertEndorsement(endorsement)
		if ret.reason != nil {
			return nil
		}
	}
	return ret
}

func (a *IncrementalAttacher) InsertEndorsement(endorsement *vertex.WrappedTx) {
	if endorsement.IsBadOrDeleted() {
		a.reason = fmt.Errorf("NewIncrementalAttacher: can't endorse %s. Reason: '%s'", endorsement.IDShortString(), endorsement.GetReason())
		return
	}
	endorsement.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		a.attachVertex(v, endorsement, ledger.NilLogicalTime, set.New[*vertex.WrappedTx]())
	}})
}

func (a *IncrementalAttacher) InsertStemAndSequencerInputs() error {
	panic("not implemented")
}

func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput) error {
	panic("not implemented")
}

func (a *IncrementalAttacher) MakeTransaction() (*transaction.Transaction, error) {
	return nil, nil
}
