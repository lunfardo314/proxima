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
		extend     *vertex.WrappedTx
		endorse    []*vertex.WrappedTx
		inputs     []vertex.WrappedOutput
		targetTs   ledger.LogicalTime
		seqOutput  vertex.WrappedOutput
		stemOutput vertex.WrappedOutput
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
		inputs:           make([]vertex.WrappedOutput, 0),
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
		if err := ret.insertEndorsement(endorsement); err != nil {
			return nil, err
		}
	}
	if err := ret.insertSequencerInput(); err != nil {
		return nil, err
	}
	if targetTs.Tick() == 0 {
		if err := ret.InsertStemInput(); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (a *IncrementalAttacher) insertEndorsement(endorsement *vertex.WrappedTx) error {
	if endorsement.IsBadOrDeleted() {
		return fmt.Errorf("NewIncrementalAttacher: can't endorse %s. Reason: '%s'", endorsement.IDShortString(), endorsement.GetReason())
	}
	endorsement.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		a.attachVertex(v, endorsement, ledger.NilLogicalTime, set.New[*vertex.WrappedTx]())
	}})
	return a.reason
}

func (a *IncrementalAttacher) insertSequencerInput() error {
	a.seqOutput = a.extend.SequencerWrappedOutput()
	if a.seqOutput.VID == nil {
		return fmt.Errorf("sequencer output not available in %s", a.extend.IDShortString())
	}
	a.attachOutput(a.seqOutput) // <<< TODO
	return nil
}

func (a *IncrementalAttacher) InsertStemInput() error {
	panic("not implemented")
}

func (a *IncrementalAttacher) InsertTagAlongInput(wOut vertex.WrappedOutput) error {
	panic("not implemented")
}

func (a *IncrementalAttacher) MakeTransaction() (*transaction.Transaction, error) {
	return nil, nil
}
