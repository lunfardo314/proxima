package utangle

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

func newUTXOTangle(stateStore general.StateStore, txBytesStore general.TxBytesStore) *UTXOTangle {
	return &UTXOTangle{
		stateStore:   stateStore,
		txBytesStore: txBytesStore,
		vertices:     make(map[core.TransactionID]*WrappedTx),
		branches:     make(map[core.TimeSlot]map[*WrappedTx]common.VCommitment),
	}
}

// Load fetches latest branches from the multi-state and creates an UTXO tangle with those branches as virtual transactions
func Load(stateStore general.StateStore, txBytesStore general.TxBytesStore) *UTXOTangle {
	ret := newUTXOTangle(stateStore, txBytesStore)
	// fetch branches of the latest slot
	branches := multistate.FetchLatestBranches(stateStore)

	for _, br := range branches {
		// make a virtual transaction
		vid := newVirtualBranchTx(br).Wrap()
		// add the transaction to the utxo tangle data structure
		ret.addVertex(vid)
		// add the corresponding branch
		ret.addBranch(vid, br.Root)
	}
	return ret
}

func newVirtualBranchTx(br *multistate.BranchData) *VirtualTransaction {
	txid := br.Stem.ID.TransactionID()
	v := newVirtualTx(&txid)
	v.addSequencerIndices(br.SeqOutput.ID.Index(), br.Stem.ID.Index())
	v.addOutput(br.SeqOutput.ID.Index(), br.SeqOutput.Output)
	v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	return v
}

func (ut *UTXOTangle) AddVertexWithSaveTx(vids ...*WrappedTx) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut.addVertexWithSaveTx(vids...)
}

func (ut *UTXOTangle) AddVertexNoSaveTx(vid *WrappedTx) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut.addVertex(vid)
}

func (ut *UTXOTangle) addVertex(vid *WrappedTx) {
	txid := vid.ID()
	_, already := ut.vertices[*txid]
	util.Assertf(!already, "addVertex: repeating transaction %s", txid.Short())
	ut.vertices[*txid] = vid
}

func (ut *UTXOTangle) addVertexWithSaveTx(vids ...*WrappedTx) {
	for _, vid := range vids {
		ut.addVertex(vid)

		// saving transaction bytes to the transaction store
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			err := ut.txBytesStore.SaveTxBytes(v.Tx.Bytes())
			util.AssertNoError(err)
		}})
	}
	ut.numAddedVertices += len(vids)
}

func (ut *UTXOTangle) deleteVertex(txid *core.TransactionID) {
	_, ok := ut.vertices[*txid]
	if ok {
		ut.numDeletedVertices++
	}
	delete(ut.vertices, *txid)
}

func (ut *UTXOTangle) AddVertexAndBranch(branchVID *WrappedTx, root common.VCommitment) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut.addVertex(branchVID)
	ut.addBranch(branchVID, root)
}

func (ut *UTXOTangle) addBranch(branchVID *WrappedTx, root common.VCommitment) {
	m, exist := ut.branches[branchVID.TimeSlot()]
	if !exist {
		m = make(map[*WrappedTx]common.VCommitment)
	}
	m[branchVID] = root
	ut.branches[branchVID.TimeSlot()] = m
}

func (ut *UTXOTangle) SolidifyInputsFromTxBytes(txBytes []byte) (*Vertex, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	return ut.SolidifyInputs(tx)
}

func newVertex(tx *transaction.Transaction) *Vertex {
	return &Vertex{
		Tx:           tx,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
		StateDelta:   *NewUTXOStateDelta(nil),
	}
}

func (ut *UTXOTangle) SolidifyInputs(tx *transaction.Transaction) (*Vertex, error) {
	ret := newVertex(tx)
	if err := ret.FetchMissingDependencies(ut); err != nil {
		return nil, err
	}
	return ret, nil
}

func (v *Vertex) CalcDeltaAndWrap(ut *UTXOTangle) (*WrappedTx, error) {
	if err := v.mergeInputDeltas(ut); err != nil {
		return nil, err
	}
	vid := v.Wrap()
	if conflict := v.StateDelta.Include(vid); conflict.VID != nil {
		return nil, fmt.Errorf("conflict %s while including %s into delta", conflict.IDShort(), vid.IDShort())
	}
	return vid, nil
}

func MakeVertex(draftVertex *Vertex, bypassConstraintValidation ...bool) (*WrappedTx, error) {
	retVID := draftVertex.Wrap()
	if !draftVertex.IsSolid() {
		return retVID, fmt.Errorf("some inputs or endorsements are not solid")
	}

	if err := draftVertex.Validate(bypassConstraintValidation...); err != nil {
		return retVID, fmt.Errorf("validate %s : '%v'", draftVertex.Tx.IDShort(), err)
	}
	if err := _calcDelta(retVID); err != nil {
		return retVID, fmt.Errorf("MakeVertex: %v", err)
	}
	return retVID, nil
}

func (ut *UTXOTangle) AppendVertex(vid *WrappedTx) error {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut.addVertexWithSaveTx(vid)

	if vid.IsBranchTransaction() {
		if err := ut.finalizeBranch(vid); err != nil {
			SaveGraphPastCone(vid, "finalizeBranchError")
			return err
		}
	}
	return nil
}

// AppendVertexFromTransactionBytesDebug for testing mainly
func (ut *UTXOTangle) AppendVertexFromTransactionBytesDebug(txBytes []byte) (*WrappedTx, string, error) {
	vertexDraft, err := ut.SolidifyInputsFromTxBytes(txBytes)
	if err != nil {
		return nil, "", err
	}
	retTxStr := vertexDraft.String()

	ret, err := MakeVertex(vertexDraft)
	if err != nil {
		return ret, retTxStr, err
	}
	err = ut.AppendVertex(ret)
	retTxStr += "\n-------\n\n" + vertexDraft.ConsumedInputsToString()
	return ret, retTxStr, err
}

func (ut *UTXOTangle) finalizeBranch(newBranchVertex *WrappedTx) error {
	err := util.CatchPanicOrError(func() error {
		return ut._finalizeBranch(newBranchVertex)
	})
	if !ut.stateStore.IsClosed() {
		return err
	}
	return nil
}

// _finalizeBranch commits state delta the database and writes branch record
func (ut *UTXOTangle) _finalizeBranch(newBranchVertex *WrappedTx) error {
	util.Assertf(newBranchVertex.IsBranchTransaction(), "v.IsBranchTransaction()")

	var err error
	var newRoot common.VCommitment
	var nextStemOutputID core.OutputID

	coverage := newBranchVertex.LedgerCoverage()

	newBranchVertex.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			util.Assertf(v.BranchConeTipSolid, "branch cone tip not solid in %s", v.Tx.IDShort())

			// determine baseline state
			seqData := v.Tx.SequencerTransactionData()
			nextStemOutputID = v.Tx.OutputID(seqData.StemOutputIndex)
			var stemOut *core.Output
			stemOut, err = v.Tx.ProducedOutputAt(seqData.StemOutputIndex)
			util.AssertNoError(err)
			stemLock, ok := stemOut.StemLock()
			util.Assertf(ok, "can't find stem lock")
			prevBranchData, ok := multistate.FetchBranchData(ut.stateStore, stemLock.PredecessorOutputID.TransactionID())
			util.Assertf(ok, "can't find previous branch data")
			var upd *multistate.Updatable
			upd, err = multistate.NewUpdatable(ut.stateStore, prevBranchData.Root)
			if err != nil {
				return
			}

			cmds := v.StateDelta.getUpdateCommands()
			err = upd.UpdateWithCommands(cmds, &nextStemOutputID, &seqData.SequencerID, coverage)
			if err != nil {
				err = fmt.Errorf("finalizeBranch %s: '%v'\n=== Delta: %s\n=== Commands: %s",
					v.Tx.IDShort(), err, v.StateDelta.LinesRecursive().String(), multistate.UpdateCommandsToLines(cmds))
				return
			}
			newRoot = upd.Root()
		},

		VirtualTx: func(_ *VirtualTransaction) {
			util.Assertf(false, "finalizeBranch: must be a branch vertex")
		},
	})
	if err != nil {
		return err
	}

	// assert consistency
	rdr, err := multistate.NewSugaredReadableState(ut.stateStore, newRoot)
	if err != nil {
		return fmt.Errorf("finalizeBranch: double check failed: '%v'", err)
	}
	util.Assertf(rdr.GetStemOutput().ID == nextStemOutputID, "rdr.GetStemOutput().ID == nextStemOutputID")

	// store new branch to the tangle data structure
	branches := ut.branches[newBranchVertex.TimeSlot()]
	if len(branches) == 0 {
		branches = make(map[*WrappedTx]common.VCommitment)
		ut.branches[newBranchVertex.TimeSlot()] = branches
	}
	branches[newBranchVertex] = newRoot
	ut.numAddedBranches++
	return nil
}
