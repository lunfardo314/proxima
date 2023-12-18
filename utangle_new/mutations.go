package utangle_new

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type _mutationData struct {
	outputMutations     map[core.OutputID]*core.Output
	addTxMutations      []*core.TransactionID
	visited             set.Set[*WrappedTx]
	baselineStateReader global.StateReader
}

func (vid *WrappedTx) _collectMutationData(md *_mutationData) (conflict WrappedOutput) {
	if md.visited.Contains(vid) {
		return
	}
	md.visited.Insert(vid)
	if md.baselineStateReader.KnowsCommittedTransaction(vid.ID()) {
		return
	}

	md.addTxMutations = append(md.addTxMutations, vid.ID())

	// FIXME revisit mutations

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
				// recursively collect from inputs
				inp._collectMutationData(md)

				inputID := v.Tx.MustInputAt(i)
				if _, produced := md.outputMutations[inputID]; produced {
					// disable assert: it may be deleted repeatedly along endorsement lines
					// util.Assertf(o != nil, "unexpected double DEL mutation at %s", inputID.StringShort())
					delete(md.outputMutations, inputID)
				} else {
					if md.baselineStateReader.HasUTXO(&inputID) {
						md.outputMutations[inputID] = nil
					} else {
						// output does not exist in the state
						conflict = WrappedOutput{VID: inp, Index: v.Tx.MustOutputIndexOfTheInput(i)}
						return false
					}
				}
				return true
			})
			v.ForEachEndorsement(func(i byte, vidEndorsed *WrappedTx) bool {
				// recursively collect from endorsements
				vidEndorsed._collectMutationData(md)
				return true
			})
			v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
				_, already := md.outputMutations[*oid]
				util.Assertf(!already, "repeating ADD mutation %s", oid.StringShort())
				md.outputMutations[*oid] = o
				return true
			})
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

func (vid *WrappedTx) getBranchMutations(ut *UTXOTangle) (*multistate.Mutations, WrappedOutput) {
	util.Assertf(vid.IsBranchTransaction(), "%s not a branch transaction", vid.IDShortString())

	baselineBranchVID := vid.BaselineBranch()
	util.Assertf(baselineBranchVID != nil, "can't get baseline branch for %s", vid.IDShortString())

	md := &_mutationData{
		outputMutations:     make(map[core.OutputID]*core.Output),
		addTxMutations:      make([]*core.TransactionID, 0),
		visited:             set.New[*WrappedTx](),
		baselineStateReader: ut.MustGetStateReader(baselineBranchVID.ID(), 1000),
	}
	if conflict := vid._collectMutationData(md); conflict.VID != nil {
		return nil, conflict
	}
	ret := multistate.NewMutations()
	for oid, o := range md.outputMutations {
		if o != nil {
			ret.InsertAddOutputMutation(oid, o)
		} else {
			ret.InsertDelOutputMutation(oid)
		}
	}
	slot := vid.TimeSlot()
	for _, txid := range md.addTxMutations {
		ret.InsertAddTxMutation(*txid, slot)
	}
	return ret.Sort(), WrappedOutput{}
}
