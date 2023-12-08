package utangle

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

func newUTXOTangle(stateStore global.StateStore) *UTXOTangle {
	return &UTXOTangle{
		stateStore: stateStore,
		vertices:   make(map[core.TransactionID]*WrappedTx),
		branches:   make(map[core.TimeSlot]map[*WrappedTx]common.VCommitment),
		syncData:   newSyncData(),
	}
}

// Load fetches latest branches from the multi-state and creates an UTXO tangle with those branches as virtual transactions
func Load(stateStore global.StateStore) *UTXOTangle {
	ret := newUTXOTangle(stateStore)
	// fetch branches of the latest slot
	branches := multistate.FetchLatestBranches(stateStore)
	for _, br := range branches {
		ret.AddVertexAndBranch(newVirtualBranchTx(br).Wrap(), br.Root)
		ret.syncData.EvidenceIncomingBranch(br.TxID(), br.SequencerID)
		ret.syncData.EvidenceBookedBranch(br.TxID(), br.SequencerID)
	}
	return ret
}

func NewVertex(tx *transaction.Transaction) *Vertex {
	return &Vertex{
		Tx:           tx,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
		pastTrack:    newPastTrack(),
	}
}

func newVirtualBranchTx(br *multistate.BranchData) *VirtualTransaction {
	txid := br.Stem.ID.TransactionID()
	v := newVirtualTx(&txid)
	v.addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	v.addOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	return v
}

func (ut *UTXOTangle) Contains(txid *core.TransactionID) bool {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	_, found := ut.vertices[*txid]
	return found
}

func (ut *UTXOTangle) _attach(vid *WrappedTx) (conflict *WrappedOutput) {
	if vid.isVirtualTx() {
		ut._mustAttachVirtualTx(vid)
	} else {
		conflict = ut._attachVertex(vid)
	}
	return
}

// _attachVertex attaches new vertex (not a virtualTx) transaction to the utxo tangle. It must be called from within global utangle lock critical section
// If conflict occurs, newly propagated forks, if any, will do no harm.
// The transaction is marked orphaned, so it will be ignored in the future cones
func (ut *UTXOTangle) _attachVertex(vid *WrappedTx) (conflict *WrappedOutput) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if conflict = v.inheritPastTracks(ut.StateStore); conflict != nil {
				return
			}
			// book consumer into the inputs. Detect new double-spends, double-links and propagates
			v.forEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
				conflict = vidInput.attachAsConsumer(v.Tx.MustOutputIndexOfTheInput(i), vid)
				return conflict == nil
			})
			// maintain endorser list in predecessors
			v.forEachEndorsement(func(_ byte, vEnd *WrappedTx) bool {
				vEnd.attachAsEndorser(vid)
				return true
			})
		},
		VirtualTx: func(_ *VirtualTransaction) {
			util.Panicf("unexpected virtualTx")
		},
		Deleted: vid.PanicAccessDeleted,
	})
	if conflict != nil {
		// mark orphaned and do not add to the utangle. If it was added to the descendants lists, it will be ignored
		// upon traversal of the future cone
		vid.MarkDeleted()
		return
	}
	txid := vid.ID()
	_, already := ut.vertices[*txid]
	util.Assertf(!already, "_attach: repeating transaction %s", txid.StringShort())
	// put vertex into the map
	ut.vertices[*txid] = vid
	// save latest tx time (from timestamp)
	ut.SyncData().storeLatestTxTime(txid)
	return
}

func (ut *UTXOTangle) _mustAttachVirtualTx(vid *WrappedTx) {
	txid := vid.ID()
	vidPrev, already := ut.vertices[*txid]
	if !already {
		// if the transactions is new, nothing to do, just add it
		ut.vertices[*txid] = vid
		return
	}
	if !vidPrev.isVirtualTx() {
		// should not happen. Ignore
		return
	}
	var vNew *VirtualTransaction
	vid.Unwrap(UnwrapOptions{VirtualTx: func(v *VirtualTransaction) {
		vNew = v
	}})
	// virtual Tx already exists, merge new outputs into it
	vidPrev.Unwrap(UnwrapOptions{VirtualTx: func(v *VirtualTransaction) {
		v.mustMergeNewOutputs(vNew)
	}})
}

func (ut *UTXOTangle) _deleteVertex(txid *core.TransactionID) {
	_, ok := ut.vertices[*txid]
	if ok {
		ut.numDeletedVertices++
	}
	delete(ut.vertices, *txid)
}

func (ut *UTXOTangle) AddVertexAndBranch(branchVID *WrappedTx, root common.VCommitment) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut._addVertexAndBranch(branchVID, root)
}

func (ut *UTXOTangle) _addVertexAndBranch(branchVID *WrappedTx, root common.VCommitment) {
	conflict := ut._attach(branchVID)
	util.Assertf(conflict == nil, "AddVertexAndBranch: conflict %s", conflict.IDShort())

	ut.addBranch(branchVID, root)
}

func (ut *UTXOTangle) addBranch(branchVID *WrappedTx, root common.VCommitment) {
	m, exist := ut.branches[branchVID.TimeSlot()]
	if !exist {
		m = make(map[*WrappedTx]common.VCommitment)
	}
	m[branchVID] = root
	ut.branches[branchVID.TimeSlot()] = m
	ut.numAddedBranches++
}

func (ut *UTXOTangle) appendVertex(vid *WrappedTx, onAttach func() error) error {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	if conflict := ut._attach(vid); conflict != nil {
		return fmt.Errorf("AppendVertex: conflict at %s", conflict.IDShort())
	}
	ut.numAddedVertices++

	if vid.IsBranchTransaction() {
		if err := ut.finalizeBranch(vid); err != nil {
			SaveGraphPastCone(vid, "finalizeBranchError")
			err = fmt.Errorf("%v\n-------------------\n%s", err, vid.PastTrackLines("     ").String())
			return err
		}
		tx := vid.UnwrapTransaction()
		ut.syncData.EvidenceBookedBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
	if onAttach != nil {
		return onAttach()
	}
	return nil
}

type (
	appendVertexOptions struct {
		bypassValidation bool
		traceOption      int
	}

	ValidationOption func(options *appendVertexOptions)
)

func BypassValidation(options *appendVertexOptions) {
	options.bypassValidation = true
}

func WithValidationTraceOption(traceOpt int) func(options *appendVertexOptions) {
	return func(opt *appendVertexOptions) {
		opt.traceOption = traceOpt
	}
}

func (ut *UTXOTangle) AppendVertex(v *Vertex, onAttach func() error, opts ...ValidationOption) (*WrappedTx, error) {
	validationOpt := appendVertexOptions{traceOption: transaction.TraceOptionFailedConstraints}
	for _, opt := range opts {
		opt(&validationOpt)
	}
	if !v.IsSolid() {
		return nil, fmt.Errorf("AppendVertex: some inputs are not solid")
	}
	if !validationOpt.bypassValidation {
		if err := v.Validate(validationOpt.traceOption); err != nil {
			return nil, fmt.Errorf("AppendVertex.validate: %v", err)
		}
	}
	vid := v.Wrap()
	return vid, ut.appendVertex(vid, onAttach)
}

// AppendVertexFromTransactionBytesDebug for testing mainly
func (ut *UTXOTangle) AppendVertexFromTransactionBytesDebug(txBytes []byte, onAttach func() error, opts ...ValidationOption) (*WrappedTx, string, error) {
	vertexDraft, err := ut.MakeDraftVertexFromTxBytes(txBytes)
	if err != nil {
		return nil, "", err
	}

	ret, err := ut.AppendVertex(vertexDraft, onAttach, opts...)
	return ret, vertexDraft.Lines().String(), err
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
func (ut *UTXOTangle) _finalizeBranch(newBranchVID *WrappedTx) error {
	util.Assertf(newBranchVID.IsBranchTransaction(), "v.IsBranchTransaction()")

	var newRoot common.VCommitment
	var nextStemOutputID core.OutputID

	tx := newBranchVID.UnwrapTransaction()
	seqTxData := tx.SequencerTransactionData()
	nextStemOutputID = tx.OutputID(seqTxData.StemOutputIndex)

	baselineVID := newBranchVID.BaselineBranch()
	util.Assertf(baselineVID != nil, "can't get baseline branch. Past track:\n%s",
		func() any { return newBranchVID.PastTrackLines().String() })
	{
		// calculate mutations, update the state and get new root
		muts, conflict := newBranchVID.getBranchMutations(ut)
		if conflict.VID != nil {
			return fmt.Errorf("conflict while calculating mutations: %s", conflict.DecodeID().StringShort())
		}
		upd, err := ut.GetStateUpdatable(baselineVID.ID())
		if err != nil {
			return err
		}

		coverageDelta := ut.LedgerCoverageDelta(newBranchVID)
		util.Assertf(coverageDelta <= seqTxData.StemOutputData.Supply-seqTxData.StemOutputData.InflationAmount,
			"coverageDelta (%s) <= seqTxData.StemOutputData.Supply (%s) - seqTxData.StemOutputData.InflationAmount (%s)",
			func() any { return util.GoThousands(coverageDelta) },
			func() any { return util.GoThousands(seqTxData.StemOutputData.Supply) },
			func() any { return util.GoThousands(seqTxData.StemOutputData.InflationAmount) },
		)

		var prevCoverage multistate.LedgerCoverage
		if multistate.HistoryCoverageDeltas > 1 {
			rr, found := multistate.FetchRootRecord(ut.stateStore, *baselineVID.ID())
			util.Assertf(found, "can't fetch root record for %s", baselineVID.IDShort())

			prevCoverage = rr.LedgerCoverage
		}
		nextCoverage := prevCoverage.MakeNext(int(newBranchVID.TimeSlot())-int(baselineVID.TimeSlot()), coverageDelta)

		err = upd.Update(muts, &nextStemOutputID, &seqTxData.SequencerID, nextCoverage)
		if err != nil {
			return fmt.Errorf("finalizeBranch %s: '%v'\n=== mutations: %d\n%s",
				newBranchVID.IDShort(), err, muts.Len(), muts.Lines("          ").String())
		}
		newRoot = upd.Root()
		// assert consistency
		rdr, err := multistate.NewSugaredReadableState(ut.stateStore, newRoot)
		if err != nil {
			return fmt.Errorf("finalizeBranch: double check failed: '%v'\n%s", err, newBranchVID.Lines().String())
		}

		var stemID core.OutputID
		err = util.CatchPanicOrError(func() error {
			stemID = rdr.GetStemOutput().ID
			return nil
		})
		util.Assertf(err == nil, "double check failed: %v\n%s\n%s", err, muts.Lines().String(), newBranchVID.PastTrackLines("   "))
		util.Assertf(stemID == nextStemOutputID, "rdr.GetStemOutput().ID == nextStemOutputID\n%s != %s\n%s",
			stemID.StringShort(), nextStemOutputID.StringShort(),
			func() any { return newBranchVID.PastTrackLines().String() })
	}
	ut.addBranch(newBranchVID, newRoot)
	return nil
}

func (ut *UTXOTangle) AppendVirtualTx(tx *transaction.Transaction) *WrappedTx {
	vid := newVirtualTxFromTx(tx).Wrap()
	conflict := ut._attach(vid)
	util.Assertf(conflict == nil, "conflict %s", conflict.IDShort())
	return vid
}

func (ut *UTXOTangle) TransactionStringFromBytes(txBytes []byte) string {
	return transaction.ParseBytesToString(txBytes, ut.GetUTXO)
}
