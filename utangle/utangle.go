package utangle

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/lunfardo314/proxima/core"
	state "github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

func (ut *UTXOTangle) TxBytesStore() common.KVStore {
	return ut.txBytesStore
}

func (ut *UTXOTangle) GetVertex(txid *core.TransactionID) (*WrappedTx, bool) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ret, found := ut.vertices[*txid]
	return ret, found
}

func (ut *UTXOTangle) MustGetVertex(txid *core.TransactionID) *WrappedTx {
	ret, ok := ut.GetVertex(txid)
	util.Assertf(ok, "MustGetVertex: can't find %s", txid.Short())
	return ret
}

// HasTransactionOnTangle checks the tangle, does not check the finalized state
func (ut *UTXOTangle) HasTransactionOnTangle(txid *core.TransactionID) bool {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	_, ret := ut.vertices[*txid]
	return ret
}

// getBranchConeTipVertex for a sequencer transaction, it finds a vertex which is to follow towards
// the branch transaction
// Returns:
// - nil, nil if it is not solid
// - nil, err if input is wrong, i.e. it cannot be solidified
// - vertex, nil if vertex, the branch cone tip, has been found
func (ut *UTXOTangle) getBranchConeTipVertex(v *Vertex) (*WrappedTx, error) {
	util.Assertf(v.Tx.IsSequencerMilestone(), "tx.IsSequencerMilestone()")
	oid := v.Tx.SequencerChainPredecessorOutputID()
	if oid == nil {
		// this transaction is chain origin, i.e. it does not have predecessor
		// follow the first endorsement. It enforced by transaction constraint layer
		return ut.mustGetFirstEndorsedVertex(v.Tx), nil
	}
	// sequencer chain predecessor exists
	if oid.TimeSlot() == v.TimeSlot() {
		if oid.SequencerFlagON() {
			return ut.vertexByOutputID(oid)
		}
		return ut.mustGetFirstEndorsedVertex(v.Tx), nil
	}
	if v.Tx.IsBranchTransaction() {
		return ut.vertexByOutputID(oid)
	}
	return ut.mustGetFirstEndorsedVertex(v.Tx), nil
}

// vertexByOutputID returns nil if transaction is not on the tangle or orphaned. Error indicates wrong output index
func (ut *UTXOTangle) vertexByOutputID(oid *core.OutputID) (*WrappedTx, error) {
	txid := oid.TransactionID()
	ret, found := ut.GetVertex(&txid)
	if !found {
		return nil, nil
	}
	if _, err := ret.OutputWithIDAt(oid.Index()); err != nil {
		return nil, err
	}
	return ret, nil
}

// mustGetFirstEndorsedVertex returns first endorsement or nil if not solid
func (ut *UTXOTangle) mustGetFirstEndorsedVertex(tx *transaction.Transaction) *WrappedTx {
	util.Assertf(tx.NumEndorsements() > 0, "tx.NumEndorsements() > 0 @ %s", func() any { return tx.IDShort() })
	txid := tx.EndorsementAt(0)
	if ret, ok := ut.GetVertex(&txid); ok {
		return ret
	}
	// not solid
	return nil
}

// solidifyOutput returns:
// - nil, nil if output cannot be solidified yet, but no error
// - nil, err if output cannot be solidified ever
// - vid, nil if solid reference has been found
func (ut *UTXOTangle) solidifyOutput(oid *core.OutputID, baseStateReader func() state.SugaredStateReader) (*WrappedTx, error) {
	txid := oid.TransactionID()
	ret, found := ut.GetVertex(&txid)
	if found {
		var err error
		ret.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				if int(oid.Index()) >= v.Tx.NumProducedOutputs() {
					err = fmt.Errorf("wrong output %s", oid.Short())
				}
			},
			VirtualTx: func(v *VirtualTransaction) {
				_, err = v.ensureOutputAt(oid.Index(), baseStateReader)
			},
		})
		if err != nil {
			return nil, err
		}
		return ret, nil
	}
	// corresponding vertex has not been found. Create virtual transaction
	virtualTx := newVirtualTx(&txid)
	if _, err := virtualTx.ensureOutputAt(oid.Index(), baseStateReader); err != nil {
		// cannot find solid reference for output, but it is not an error
		return nil, nil
	}
	// store new virtual TX to the tangle
	ret = virtualTx.Wrap()
	ut.AddVertex(ret)
	return ret, nil
}

func (ut *UTXOTangle) timeSlotsOrdered(descOrder ...bool) []core.TimeSlot {
	desc := false
	if len(descOrder) > 0 {
		desc = descOrder[0]
	}
	return util.SortKeys(ut.branches, func(e1, e2 core.TimeSlot) bool {
		if desc {
			return e1 > e2
		}
		return e1 < e2
	})
}

func (ut *UTXOTangle) WriteTransactionLog(w io.Writer) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	sortedKeys := util.SortKeys(ut.vertices, func(k1, k2 core.TransactionID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	for _, txid := range sortedKeys {
		ut.vertices[txid].Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			if v.txLog == nil {
				_, _ = fmt.Fprintf(w, "-- Transaction %s does not have log\n", v.Tx.IDShort())
			} else {
				v.txLog.WriteLog(w)
			}
		}})
	}
}

func (ut *UTXOTangle) TransactionLogAsString() string {
	var buf bytes.Buffer
	ut.WriteTransactionLog(&buf)
	return buf.String()
}

func (ut *UTXOTangle) NumVertices() int {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return len(ut.vertices)
}

func (ut *UTXOTangle) Info(verbose ...bool) string {
	return ut.InfoLines(verbose...).String()
}

func (ut *UTXOTangle) InfoLines(verbose ...bool) *lines.Lines {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ln := lines.New()
	slots := ut.timeSlotsOrdered()

	verb := false
	if len(verbose) > 0 {
		verb = verbose[0]
	}
	ln.Add("UTXOTangle (verbose = %v), numVertices: %d, num slots: %d, addTx: %d, delTx: %d, addBranch: %d, delBranch: %d",
		verb, ut.NumVertices(), len(slots), ut.numAddedVertices, ut.numDeletedVertices, ut.numAddedBranches, ut.numDeletedBranches)
	for _, e := range slots {
		branches := util.SortKeys(ut.branches[e], func(vid1, vid2 *WrappedTx) bool {
			chainID1, ok := vid1.SequencerIDIfAvailable()
			util.Assertf(ok, "can't gets sequencer ID")
			chainID2, ok := vid2.SequencerIDIfAvailable()
			util.Assertf(ok, "can't gets sequencer ID")
			return bytes.Compare(chainID1[:], chainID2[:]) < 0
		})

		ln.Add("---- slot %8d : branches: %d", e, len(branches))
		for _, vid := range branches {
			br := ut.branches[e][vid]
			coverage := vid.LedgerCoverage(TipSlots)
			seqID, isAvailable := vid.SequencerIDIfAvailable()
			util.Assertf(isAvailable, "sequencer ID expected in %s", vid.IDShort())
			ln.Add("    branch %s, seqID: %s, coverage: %s", vid.IDShort(), seqID.Short(), util.GoThousands(coverage))
			if verb {
				ln.Add("    == root: " + br.root.String()).
					Append(vid.Lines("    "))
			}
		}
	}
	return ln
}

// Access to the tangle state is NON-DETERMINISTIC

func (ut *UTXOTangle) GetUTXO(oid *core.OutputID) ([]byte, bool) {
	txid := oid.TransactionID()
	if vid, found := ut.GetVertex(&txid); found {
		if o, err := vid.OutputAt(oid.Index()); err == nil && o != nil {
			return o.Bytes(), true
		}
	}
	return ut.HeaviestStateForLatestTimeSlot().GetUTXO(oid)
}

func (ut *UTXOTangle) HasUTXO(oid *core.OutputID) bool {
	txid := oid.TransactionID()
	if vid, found := ut.GetVertex(&txid); found {
		if o, err := vid.OutputAt(oid.Index()); err == nil && o != nil {
			return true
		}
	}
	return ut.HeaviestStateForLatestTimeSlot().HasUTXO(oid)
}

func (ut *UTXOTangle) GetBranch(vid *WrappedTx) (branch, bool) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut.getBranch(vid)
}

func (ut *UTXOTangle) getBranch(vid *WrappedTx) (branch, bool) {
	eb, found := ut.branches[vid.TimeSlot()]
	if !found {
		return branch{}, false
	}
	if ret, found := eb[vid]; found {
		return ret, true
	}
	return branch{}, false
}

func (ut *UTXOTangle) mustGetBranch(vid *WrappedTx) branch {
	ret, ok := ut.getBranch(vid)
	util.Assertf(ok, "can't get branch %s", vid.IDShort())
	return ret
}

func (ut *UTXOTangle) isValidBranch(br *WrappedTx) bool {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	if !br.IsBranchTransaction() {
		return false
	}
	_, found := ut.getBranch(br)
	return found
}

// GetBaseStateRootOfSequencerMilestone returns root of the base state of the sequencer milestone, if possible
func (ut *UTXOTangle) GetBaseStateRootOfSequencerMilestone(vSeq *WrappedTx) (common.VCommitment, bool) {
	if !vSeq.IsSequencerMilestone() {
		return nil, false
	}

	var branchVID *WrappedTx
	isBranch := vSeq.IsBranchTransaction()
	vSeq.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if isBranch {
				branchVID = vSeq
			} else {
				branchVID = v.StateDelta.baselineBranch
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if isBranch {
				branchVID = vSeq
			}
		},
	})
	if branchVID == nil {
		return nil, false
	}
	var br branch
	var ok bool

	br, ok = ut.GetBranch(branchVID)
	util.Assertf(ok, "can't find branch")

	return br.root, true
}

func (ut *UTXOTangle) StateReaderOfSequencerMilestone(vid *WrappedTx) (state.SugaredStateReader, bool) {
	util.Assertf(vid.IsSequencerMilestone(), "StateReaderOfSequencerMilestone: must be sequencer milestone")
	root, available := ut.GetBaseStateRootOfSequencerMilestone(vid)
	if !available {
		return state.SugaredStateReader{}, false
	}
	rdr, err := state.NewReadable(ut.stateStore, root)
	util.AssertNoError(err)
	return state.MakeSugared(rdr), true
}

func (ut *UTXOTangle) HeaviestStateRootForLatestTimeSlot() common.VCommitment {
	return ut.heaviestBranchForLatestTimeSlot().root
}

// HeaviestStateForLatestTimeSlot returns the heaviest input state (by ledger coverage) for the latest slot which have one
func (ut *UTXOTangle) HeaviestStateForLatestTimeSlot() state.SugaredStateReader {
	root := ut.HeaviestStateRootForLatestTimeSlot()
	ret, err := state.NewReadable(ut.stateStore, root)
	util.AssertNoError(err)
	return state.MakeSugared(ret)
}

// heaviestBranchForLatestTimeSlot return branch transaction vertex with the highest ledger coverage
// Returns cached full root or nil
func (ut *UTXOTangle) heaviestBranchForLatestTimeSlot() branch {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	desc := ut.timeSlotsOrdered(true)
	coverage := uint64(0)
	var largestVID *WrappedTx
	for _, e := range desc {
		branches := ut.branches[e]
		if len(branches) == 0 {
			continue
		}
		for vid := range branches {
			cov := vid.LedgerCoverage(TipSlots)
			if coverage == 0 || cov > coverage {
				coverage = cov
				largestVID = vid
			}
		}
		return branches[largestVID]
	}
	util.Assertf(false, "inconsistency: cannot find heaviest finalized state")
	return branch{}
}

func (ut *UTXOTangle) LatestTimeSlot() core.TimeSlot {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	for _, e := range ut.timeSlotsOrdered(true) {
		if len(ut.branches[e]) > 0 {
			return e
		}
	}
	return 0
}

func (ut *UTXOTangle) HeaviestStemOutput() *core.OutputWithID {
	return ut.HeaviestStateForLatestTimeSlot().GetStemOutput()
}

func (ut *UTXOTangle) ForEachBranchState(e core.TimeSlot, fun func(rdr state.SugaredStateReader) bool) error {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ut.forEachBranchSorted(e, func(vid *WrappedTx, br branch) bool {
		r, err := state.NewReadable(ut.stateStore, br.root)
		util.AssertNoError(err)
		return fun(state.MakeSugared(r))
	})
	return nil
}

func (ut *UTXOTangle) forEachBranchSorted(e core.TimeSlot, fun func(vid *WrappedTx, br branch) bool) {
	branches, ok := ut.branches[e]
	if !ok {
		return
	}

	vids := util.SortKeys(branches, func(vid1, vid2 *WrappedTx) bool {
		return vid1.LedgerCoverage(TipSlots) < vid2.LedgerCoverage(TipSlots)
	})
	for _, vid := range vids {
		if !fun(vid, branches[vid]) {
			return
		}
	}
}

func (ut *UTXOTangle) HasOutputInTimeSlot(e core.TimeSlot, oid *core.OutputID) bool {
	ret := false
	err := ut.ForEachBranchState(e, func(rdr state.SugaredStateReader) bool {
		_, ret = rdr.GetUTXO(oid)
		return !ret
	})
	util.AssertNoError(err)
	return ret
}

// WrapOutput fetches output in encoded form. Creates VirtualTransaction vertex, if necessary
func (ut *UTXOTangle) WrapOutput(o *core.OutputWithID) (WrappedOutput, bool) {
	txid := o.ID.TransactionID()
	vid, found := ut.GetVertex(&txid)
	available := true
	if found {
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				if int(o.ID.Index()) > v.Tx.NumProducedOutputs() {
					available = false
				}
			},
			VirtualTx: func(v *VirtualTransaction) {
				v.outputs[o.ID.Index()] = o.Output
			},
			Orphaned: func() {
				available = false
			},
		})
	} else {
		v := newVirtualTx(&txid)
		v.outputs[o.ID.Index()] = o.Output
		vid = v.Wrap()
		ut.AddVertex(vid)
	}
	return WrappedOutput{vid, o.ID.Index()}, available
}

func (ut *UTXOTangle) MustWrapOutput(o *core.OutputWithID) WrappedOutput {
	ret, ok := ut.WrapOutput(o)
	util.Assertf(ok, "can't wrap output %s", func() any { return o.IDShort() })
	return ret
}

// ScanAccount collects all outputIDs, unlockable by the address
// It is a global scan of the tangle and of the state. Should be only done once upon sequencer start.
// Further on the account should be maintained by the listener
func (ut *UTXOTangle) ScanAccount(addr core.AccountID, lastNTimeSlots int) set.Set[WrappedOutput] {
	toScan, _, _ := ut.TipList(lastNTimeSlots)
	ret := set.New[WrappedOutput]()

	for _, vid := range toScan {
		if vid.IsBranchTransaction() {
			br := ut.mustGetBranch(vid)
			rdr := state.MustNewSugaredStateReader(ut.stateStore, br.root)
			outs, err := rdr.GetOutputsForAccount(addr)
			util.AssertNoError(err)
			for _, o := range outs {
				ow, ok := ut.WrapOutput(o)
				util.Assertf(ok, "ScanAccount: can't fetch output %s", o.IDShort())
				ret.Insert(ow)
			}
		}

		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			v.Tx.ForEachProducedOutput(func(_ byte, o *core.Output, oid *core.OutputID) bool {
				lck := o.Lock()
				// Note, that stem output is unlockable with any account
				if lck.Name() != core.StemLockName && lck.UnlockableWith(addr) {
					ow, ok := ut.WrapOutput(&core.OutputWithID{
						ID:     *oid,
						Output: o,
					})
					util.Assertf(ok, "ScanAccount: can't fetch output %s", oid.Short())
					ret.Insert(ow)
				}
				return true
			})
		}})
	}
	return ret
}

func (ut *UTXOTangle) LoadSequencerStartOutputs(seqID core.ChainID, stateReader func() state.SugaredStateReader) (WrappedOutput, WrappedOutput, error) {
	rdr := stateReader()
	chainOut, err := rdr.GetChainOutput(&seqID)
	if err != nil {
		return WrappedOutput{}, WrappedOutput{}, fmt.Errorf("can't find chain output for %s: %v", seqID.Short(), err)
	}
	stemOut := rdr.GetStemOutput()

	return ut.MustWrapOutput(chainOut), ut.MustWrapOutput(stemOut), nil
}

func (ut *UTXOTangle) LoadSequencerStartOutputsDefault(seqID core.ChainID) (WrappedOutput, WrappedOutput, error) {
	chainOut, stemOut, err := ut.LoadSequencerStartOutputs(seqID, func() state.SugaredStateReader {
		return ut.HeaviestStateForLatestTimeSlot()
	})
	if err == nil {
		return chainOut, stemOut, nil
	}
	return WrappedOutput{}, WrappedOutput{}, fmt.Errorf("LoadSequencerStartOutputsDefault: %v", err)
}

func (ut *UTXOTangle) _baselineTime(nLatestSlots int) (time.Time, int) {
	util.Assertf(nLatestSlots > 0, "nLatestSlots > 0")

	var earliestSlot core.TimeSlot
	count := 0
	for _, s := range ut.timeSlotsOrdered(true) {
		if len(ut.branches[s]) > 0 {
			earliestSlot = s
			count++
		}
		if count == nLatestSlots {
			break
		}
	}
	var baseline time.Time
	first := true
	for vid := range ut.branches[earliestSlot] {
		if first || vid.Time().Before(baseline) {
			baseline = vid.Time()
			first = false
		}
	}
	return baseline, count
}

// _tipList returns:
// - time of the oldest branch of 'nLatestSlots' non-empty time slots (baseline time) or 'time.Time{}'
// - a list of transactions which has Time() not-older that baseline time
// - true if there are less or equal than 'nLatestSlots' non-empty time slots
// returns nil, time.Time{}, false if not enough timeslots
// list is randomly ordered
func (ut *UTXOTangle) _tipList(nLatestSlots int) ([]*WrappedTx, time.Time, int) {
	baseline, nSlots := ut._baselineTime(nLatestSlots)

	ret := make([]*WrappedTx, 0)
	for _, vid := range ut.vertices {
		if !vid.Time().Before(baseline) {
			ret = append(ret, vid)
		}
	}
	return ret, baseline, nSlots
}

func (ut *UTXOTangle) TipList(nLatestSlots int) ([]*WrappedTx, time.Time, int) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut._tipList(nLatestSlots)
}
