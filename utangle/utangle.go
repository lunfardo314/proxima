package utangle

import (
	"bytes"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

func (ut *UTXOTangle) getVertex(txid *core.TransactionID) (*WrappedTx, bool) {
	ret, found := ut.vertices[*txid]

	return ret, found
}

func (ut *UTXOTangle) GetVertex(txid *core.TransactionID) (*WrappedTx, bool) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut.getVertex(txid)
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

func (ut *UTXOTangle) _timeSlotsOrdered(descOrder ...bool) []core.TimeSlot {
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
	slots := ut._timeSlotsOrdered()

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
			coverage := ut.LedgerCoverage(vid)
			seqID, isAvailable := vid.SequencerIDIfAvailable()
			util.Assertf(isAvailable, "sequencer ID expected in %s", vid.IDShort())
			ln.Add("    branch %s, seqID: %s, coverage: %s", vid.IDShort(), seqID.Short(), util.GoThousands(coverage))
			if verb {
				ln.Add("    == root: " + ut.branches[e][vid].String()).
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

func (ut *UTXOTangle) KnowsCommittedTransaction(txid *core.TransactionID) bool {
	return ut.HasTransactionOnTangle(txid)
}

func (ut *UTXOTangle) GetBranch(vid *WrappedTx) (common.VCommitment, bool) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut.getBranch(vid)
}

// MustGetBranchState returns state reader corresponding to the branch transaction
func (ut *UTXOTangle) MustGetBranchState(vid *WrappedTx) multistate.SugaredStateReader {
	util.Assertf(vid.IsBranchTransaction(), "vid.IsBranchTransaction()")
	rootData, ok := multistate.FetchRootRecord(ut.stateStore, *vid.ID())
	util.Assertf(ok, "can't get root data for branch transaction")

	ret, err := multistate.NewSugaredReadableState(ut.stateStore, rootData.Root, 0)
	util.AssertNoError(err)

	return ret
}

func (ut *UTXOTangle) getBranch(vid *WrappedTx) (common.VCommitment, bool) {
	eb, found := ut.branches[vid.TimeSlot()]
	if !found {
		return nil, false
	}
	if ret, found := eb[vid]; found {
		return ret, true
	}
	return nil, false
}

func (ut *UTXOTangle) mustGetBranch(vid *WrappedTx) common.VCommitment {
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

func (ut *UTXOTangle) GetIndexedStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) (general.IndexedStateReader, error) {
	rr, found := multistate.FetchRootRecord(ut.stateStore, *branchTxID)
	if !found {
		return nil, fmt.Errorf("root record for %s has not been found", branchTxID.Short())
	}
	return multistate.NewReadable(ut.stateStore, rr.Root, clearCacheAtSize...)
}

func (ut *UTXOTangle) MustGetIndexedStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) general.IndexedStateReader {
	ret, err := ut.GetIndexedStateReader(branchTxID, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

func (ut *UTXOTangle) MustGetStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) general.StateReader {
	return ut.MustGetIndexedStateReader(branchTxID, clearCacheAtSize...)
}

func (ut *UTXOTangle) MustGetSugaredStateReader(branchTxID *core.TransactionID) multistate.SugaredStateReader {
	return multistate.MakeSugared(ut.MustGetIndexedStateReader(branchTxID))
}

func (ut *UTXOTangle) GetStateUpdatable(branchTxID *core.TransactionID) (*multistate.Updatable, error) {
	rr, found := multistate.FetchRootRecord(ut.stateStore, *branchTxID)
	if !found {
		return nil, fmt.Errorf("root record for %s has not been found", branchTxID.Short())
	}
	return multistate.NewUpdatable(ut.stateStore, rr.Root)
}

func (ut *UTXOTangle) HeaviestStateRootForLatestTimeSlot() common.VCommitment {
	return ut.heaviestBranchForLatestTimeSlot()
}

// HeaviestStateForLatestTimeSlot returns the heaviest input state (by ledger coverage) for the latest slot which have one
func (ut *UTXOTangle) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	root := ut.HeaviestStateRootForLatestTimeSlot()
	ret, err := multistate.NewReadable(ut.stateStore, root)
	util.AssertNoError(err)
	return multistate.MakeSugared(ret)
}

// heaviestBranchForLatestTimeSlot return branch transaction vertex with the highest ledger coverage
// Returns cached full root or nil
func (ut *UTXOTangle) heaviestBranchForLatestTimeSlot() common.VCommitment {
	var largestBranch common.VCommitment
	var found bool

	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ut.forEachBranchSorted(ut.LatestTimeSlot(), func(vid *WrappedTx, root common.VCommitment) bool {
		largestBranch = root
		found = true
		return false
	}, true)

	util.Assertf(found, "inconsistency: cannot find heaviest finalized state")
	return largestBranch
}

// LatestTimeSlot latest time slot with some branches
func (ut *UTXOTangle) LatestTimeSlot() core.TimeSlot {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	for _, e := range ut._timeSlotsOrdered(true) {
		if len(ut.branches[e]) > 0 {
			return e
		}
	}
	return 0
}

func (ut *UTXOTangle) HeaviestStemOutput() *core.OutputWithID {
	return ut.HeaviestStateForLatestTimeSlot().GetStemOutput()
}

func (ut *UTXOTangle) ForEachBranchStateDesc(e core.TimeSlot, fun func(vid *WrappedTx, rdr multistate.SugaredStateReader) bool) error {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ut.forEachBranchSorted(e, func(vid *WrappedTx, root common.VCommitment) bool {
		r, err := multistate.NewReadable(ut.stateStore, root, 0)
		util.AssertNoError(err)
		return fun(vid, multistate.MakeSugared(r))
	}, true)
	return nil
}

func (ut *UTXOTangle) forEachBranchSorted(e core.TimeSlot, fun func(vid *WrappedTx, root common.VCommitment) bool, desc bool) {
	branches, ok := ut.branches[e]
	if !ok {
		return
	}

	vids := util.SortKeys(branches, func(vid1, vid2 *WrappedTx) bool {
		if desc {
			return ut.LedgerCoverage(vid1) > ut.LedgerCoverage(vid2)
		}
		return ut.LedgerCoverage(vid1) < ut.LedgerCoverage(vid2)
	})
	for _, vid := range vids {
		if !fun(vid, branches[vid]) {
			return
		}
	}
}

func (ut *UTXOTangle) GetSequencerBootstrapOutputs(seqID core.ChainID) (chainOut WrappedOutput, stemOut WrappedOutput, found bool) {
	branches := multistate.FetchLatestBranches(ut.stateStore)
	for _, bd := range branches {
		rdr := multistate.MustNewSugaredStateReader(ut.stateStore, bd.Root)
		if seqOut, err := rdr.GetChainOutput(&seqID); err == nil {
			retStem, ok, _ := ut.GetWrappedOutput(&bd.Stem.ID, rdr)
			util.Assertf(ok, "can't get wrapped stem output %s", bd.Stem.ID.Short())

			retSeq, ok, _ := ut.GetWrappedOutput(&seqOut.ID, rdr)
			util.Assertf(ok, "can't get wrapped sequencer output %s", seqOut.ID.Short())

			return retSeq, retStem, true
		}
	}
	return WrappedOutput{}, WrappedOutput{}, false
}

func (ut *UTXOTangle) HasOutputInTimeSlot(e core.TimeSlot, oid *core.OutputID) bool {
	ret := false
	err := ut.ForEachBranchStateDesc(e, func(_ *WrappedTx, rdr multistate.SugaredStateReader) bool {
		_, ret = rdr.GetUTXO(oid)
		return !ret
	})
	util.AssertNoError(err)
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
			rdr := multistate.MustNewSugaredStateReader(ut.stateStore, ut.mustGetBranch(vid))
			outs, err := rdr.GetIDSLockedInAccount(addr)
			util.AssertNoError(err)

			for i := range outs {
				ow, ok, _ := ut.GetWrappedOutput(&outs[i], rdr)
				util.Assertf(ok, "ScanAccount: can't fetch output %s", outs[i].Short())
				ret.Insert(ow)
			}
		}

		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			v.Tx.ForEachProducedOutput(func(i byte, o *core.Output, oid *core.OutputID) bool {
				lck := o.Lock()
				// Note, that stem output is unlockable with any account
				if lck.Name() != core.StemLockName && lck.UnlockableWith(addr) {
					ret.Insert(WrappedOutput{
						VID:   vid,
						Index: i,
					})
				}
				return true
			})
		}})
	}
	return ret
}

func (ut *UTXOTangle) _baselineTime(nLatestSlots int) (time.Time, int) {
	util.Assertf(nLatestSlots > 0, "nLatestSlots > 0")

	var earliestSlot core.TimeSlot
	count := 0
	for _, s := range ut._timeSlotsOrdered(true) {
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

func (ut *UTXOTangle) FetchBranchData(branchTxID *core.TransactionID) (multistate.BranchData, bool) {
	return multistate.FetchBranchData(ut.stateStore, *branchTxID)
}

func (ut *UTXOTangle) StateStore() general.StateStore {
	return ut.stateStore
}

func (ut *UTXOTangle) LedgerCoverageDelta(vid *WrappedTx) uint64 {
	_, ret := vid.CoverageDelta(ut)
	return ret
}

func (ut *UTXOTangle) LedgerCoverage(vid *WrappedTx) uint64 {
	return vid.LedgerCoverage(ut)
}

func (ut *UTXOTangle) LedgerCoverageFromTransaction(tx *transaction.Transaction) (uint64, error) {
	tmpVertex, conflict := ut.MakeDraftVertex(tx)
	if conflict != nil {
		return 0, fmt.Errorf("conflict in the past cone at %s", conflict.Short())
	}
	if !tmpVertex.IsSolid() {
		return 0, fmt.Errorf("some inputs are not solid")
	}
	return ut.LedgerCoverage(tmpVertex.Wrap()), nil
}
