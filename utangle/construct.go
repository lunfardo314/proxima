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
// TODO load latest common branch too
func Load(stateStore general.StateStore, txBytesStore general.TxBytesStore) *UTXOTangle {
	ret := newUTXOTangle(stateStore, txBytesStore)
	// fetch branches of the latest slot
	branches := multistate.FetchLatestBranches(stateStore)
	for _, br := range branches {
		ret.AddVertexAndBranch(newVirtualBranchTx(br).Wrap(), br.Root)
	}
	return ret
}

func newVirtualBranchTx(br *multistate.BranchData) *VirtualTransaction {
	txid := br.Stem.ID.TransactionID()
	v := newVirtualTx(&txid)
	v.addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	v.addOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	return v
}

// attach attaches transaction to the utxo tangle. It must be called from within global utangle lock critical section
// If conflict occurs, newly propagated forks, if any, will do no harm.
// The transaction is marked orphaned, so it will be ignored in the future cones
func (ut *UTXOTangle) attach(vid *WrappedTx) (conflict WrappedOutput) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		// book consumer into the inputs. Store forks (double spends), detect new ones and propagate to the future cone
		v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
			inp.addConsumerOfOutput(v.Tx.MustOutputIndexOfTheInput(i), vid, ut)
			return true
		})
		// maintain endorser list in predecessors
		v.forEachEndorsement(func(_ byte, vEnd *WrappedTx) bool {
			vEnd.addEndorser(vid)
			return true
		})
		// forks must be recalculated after all new double spends are detected and propagated
		conflict = v.reMergeParentForkSets()
	}})
	if conflict.VID != nil {
		// mark orphaned and do not add to the utangle. If it was added to the descendants lists, it will be ignored
		// upon traversal of the future cone
		vid.MarkOrphaned()
		return
	}
	txid := vid.ID()
	_, already := ut.vertices[*txid]
	util.Assertf(!already, "attach: repeating transaction %s", txid.Short())
	ut.vertices[*txid] = vid
	return
}

func (ut *UTXOTangle) attachWithSaveTx(vid *WrappedTx) (conflict WrappedOutput) {
	if conflict = ut.attach(vid); conflict.VID != nil {
		return
	}
	// saving transaction bytes to the transaction store
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		err := ut.txBytesStore.SaveTxBytes(v.Tx.Bytes())
		util.AssertNoError(err)
	}})
	ut.numAddedVertices++
	return
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

	conflict := ut.attach(branchVID)
	util.Assertf(conflict.VID == nil, "AddVertexAndBranch: conflict %s", conflict.IDShort())

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

func NewVertex(tx *transaction.Transaction) *Vertex {
	return &Vertex{
		Tx:           tx,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
	}
}

func (ut *UTXOTangle) _appendVertex(vid *WrappedTx) error {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	if conflict := ut.attachWithSaveTx(vid); conflict.VID != nil {
		return fmt.Errorf("AppendVertex: conflict at %s", conflict.IDShort())
	}

	if vid.IsBranchTransaction() {
		if err := ut.finalizeBranch(vid); err != nil {
			SaveGraphPastCone(vid, "finalizeBranchError")
			return err
		}
	}
	return nil
}

func (ut *UTXOTangle) AppendVertex(v *Vertex, bypassValidation ...bool) (*WrappedTx, error) {
	if !v.IsSolid() {
		return nil, fmt.Errorf("AppendVertex: some inputs are not solid")
	}
	if len(bypassValidation) == 0 || !bypassValidation[0] {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("AppendVertex.validate: %v", err)
		}
	}
	vid := v.Wrap()
	return vid, ut._appendVertex(vid)
}

// AppendVertexFromTransactionBytesDebug for testing mainly
func (ut *UTXOTangle) AppendVertexFromTransactionBytesDebug(txBytes []byte) (*WrappedTx, string, error) {
	vertexDraft, err := ut.MakeDraftVertexFromTxBytes(txBytes)
	if err != nil {
		return nil, "", err
	}
	retTxStr := vertexDraft.String()

	ret, err := ut.AppendVertex(vertexDraft)
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

	var newRoot common.VCommitment
	var nextStemOutputID core.OutputID

	tx := newBranchVertex.UnwrapTransaction()
	seqTxData := tx.SequencerTransactionData()
	nextStemOutputID = tx.OutputID(seqTxData.StemOutputIndex)

	baselineVID := newBranchVertex.BaselineBranch()
	util.Assertf(baselineVID != nil, "can't get baseline branch")

	{
		// calculate mutations, update the state and get new root
		muts, conflict := newBranchVertex.getBranchMutations(ut)
		if conflict.VID != nil {
			return fmt.Errorf("conflict while calculating mutations: %s", conflict.DecodeID().Short())
		}
		upd, err := ut.GetStateUpdatable(baselineVID.ID())
		if err != nil {
			return err
		}
		coverage := ut.LedgerCoverageDelta(newBranchVertex)
		err = upd.Update(muts, &nextStemOutputID, &seqTxData.SequencerID, coverage)
		if err != nil {
			return fmt.Errorf("finalizeBranch %s: '%v'=== mutations: %d\n%s",
				newBranchVertex.IDShort(), err, muts.Len(), muts.Lines().String())
		}
		newRoot = upd.Root()
		// assert consistency
		rdr, err := multistate.NewSugaredReadableState(ut.stateStore, newRoot)
		if err != nil {
			return fmt.Errorf("finalizeBranch: double check failed: '%v'\n%s", err, newBranchVertex.String())
		}

		var stemID core.OutputID
		err = util.CatchPanicOrError(func() error {
			stemID = rdr.GetStemOutput().ID
			return nil
		})
		util.Assertf(err == nil, "double check failed: %v\n%s\n%s", err, muts.Lines().String(), newBranchVertex.ForkLines("   "))
		util.Assertf(stemID == nextStemOutputID, "rdr.GetStemOutput().ID == nextStemOutputID\n%s != %s\n%s",
			stemID.Short(), nextStemOutputID.Short(),
			func() any { return newBranchVertex.LinesForks().String() })
	}
	{
		// store new branch to the tangle data structure
		branches := ut.branches[newBranchVertex.TimeSlot()]
		if len(branches) == 0 {
			branches = make(map[*WrappedTx]common.VCommitment)
			ut.branches[newBranchVertex.TimeSlot()] = branches
		}
		branches[newBranchVertex] = newRoot
		ut.numAddedBranches++
	}
	return nil
}

func (ut *UTXOTangle) TransactionStringFromBytes(txBytes []byte) string {
	return transaction.ParseBytesToString(txBytes, ut.GetUTXO)
}
