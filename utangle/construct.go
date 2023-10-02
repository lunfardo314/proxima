package utangle

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/txlog"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/atomic"
)

type (
	UTXOTangle struct {
		mutex        sync.RWMutex
		stateStore   general.StateStore
		txBytesStore general.TxBytesStore
		vertices     map[core.TransactionID]*WrappedTx
		branches     map[core.TimeSlot]map[*WrappedTx]branch

		lastPrunedOrphaned atomic.Time
		lastCutFinal       atomic.Time

		numAddedVertices   int
		numDeletedVertices int
		numAddedBranches   int
		numDeletedBranches int
	}

	Vertex struct {
		txLog        *txlog.TransactionLog
		Tx           *transaction.Transaction
		StateDelta   UTXOStateDelta //
		Inputs       []*WrappedTx
		Endorsements []*WrappedTx
	}

	UTXOStateDelta struct {
		baselineBranch *WrappedTx
		transactions   map[*WrappedTx]transactionData
		coverage       uint64
	}

	transactionData struct {
		consumed          set.Set[byte] // TODO optimize with bitmap?
		includedThisDelta bool
	}

	VirtualTransaction struct {
		txid             core.TransactionID
		mutex            sync.RWMutex
		outputs          map[byte]*core.Output
		sequencerOutputs *[2]byte // if nil, it is unknown
	}

	branch struct {
		root common.VCommitment
	}
)

const TipSlots = 5

func newUTXOTangle(stateStore general.StateStore, txBytesStore general.TxBytesStore) *UTXOTangle {
	return &UTXOTangle{
		stateStore:   stateStore,
		txBytesStore: txBytesStore,
		vertices:     make(map[core.TransactionID]*WrappedTx),
		branches:     make(map[core.TimeSlot]map[*WrappedTx]branch),
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
		m = make(map[*WrappedTx]branch)
	}
	m[branchVID] = branch{
		root: root,
	}
	ut.branches[branchVID.TimeSlot()] = m
}

func (ut *UTXOTangle) SolidifyInputsFromTxBytes(txBytes []byte) (*Vertex, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	return ut.SolidifyInputs(tx)
}

func newVertex(tx *transaction.Transaction, txLog *txlog.TransactionLog) *Vertex {
	return &Vertex{
		Tx:           tx,
		txLog:        txLog,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
		StateDelta:   *NewUTXOStateDelta(nil),
	}
}

func (ut *UTXOTangle) SolidifyInputs(tx *transaction.Transaction, txl ...*txlog.TransactionLog) (*Vertex, error) {
	var txLog *txlog.TransactionLog
	if len(txl) > 0 {
		txLog = txl[0]
	}
	ret := newVertex(tx, txLog)
	if err := ret.FetchMissingDependencies(ut); err != nil {
		return nil, err
	}
	return ret, nil
}

func _calcDelta(vid *WrappedTx) error {
	v, ok := vid.UnwrapVertex()
	util.Assertf(ok, "vertex expected")

	if err := v.mergeInputDeltas(); err != nil {
		return err
	}
	if conflict := v.StateDelta.include(vid); conflict != nil {
		return fmt.Errorf("conflict %s while including %s into delta:\n%s", conflict.IDShort(), vid.IDShort(), v.StateDelta.LinesRecursive().String())
	}
	return nil
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

func (ut *UTXOTangle) AppendVertexFromTransactionBytes(txBytes []byte) (*WrappedTx, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	vertexDraft, err := ut.SolidifyInputs(tx)
	if err != nil {
		return nil, err
	}
	ret, err := MakeVertex(vertexDraft)
	if err != nil {
		return nil, err
	}
	err = ut.AppendVertex(ret)
	return ret, err
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

// finalizeBranch also starts new delta on mutations
func (ut *UTXOTangle) finalizeBranch(newBranchVertex *WrappedTx) error {
	util.Assertf(newBranchVertex.IsBranchTransaction(), "v.IsBranchTransaction()")

	var err error
	var newRoot common.VCommitment
	var nextStemOutputID core.OutputID

	coverage := newBranchVertex.LedgerCoverage(TipSlots)

	newBranchVertex.Unwrap(UnwrapOptions{

		Vertex: func(v *Vertex) {
			seqData := v.Tx.SequencerTransactionData()
			nextStemOutputID = v.Tx.OutputID(seqData.StemOutputIndex)
			util.Assertf(v.StateDelta.baselineBranch != nil, "v.StateDelta.baselineBranch != nil")
			prevBranch, ok := ut.getBranch(v.StateDelta.baselineBranch)

			if !ok {
				SaveGraphPastCone(newBranchVertex, "branch_prob")
			}
			util.Assertf(ok, "finalizeBranch %s: can't find previous branch %s", newBranchVertex.IDShort(), v.StateDelta.baselineBranch.IDShort())

			upd, err1 := multistate.NewUpdatable(ut.stateStore, prevBranch.root)
			if err1 != nil {
				err = err1
				return
			}
			cmds := v.StateDelta.getUpdateCommands()
			err1 = upd.UpdateWithCommands(cmds, &nextStemOutputID, &seqData.SequencerID, coverage)
			if err1 != nil {
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
		return err
	}
	util.Assertf(rdr.GetStemOutput().ID == nextStemOutputID, "rdr.GetStemOutput().ID == nextStemOutputID")

	// store new branch to the tangle data structure
	branches := ut.branches[newBranchVertex.TimeSlot()]
	if len(branches) == 0 {
		branches = make(map[*WrappedTx]branch)
		ut.branches[newBranchVertex.TimeSlot()] = branches
	}
	branches[newBranchVertex] = branch{
		root: newRoot,
	}
	ut.numAddedBranches++
	return nil
}
