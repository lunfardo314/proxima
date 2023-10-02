package utangle

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

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

func (ut *UTXOTangle) StateReaderOfSequencerMilestone(vid *WrappedTx) (multistate.SugaredStateReader, bool) {
	util.Assertf(vid.IsSequencerMilestone(), "StateReaderOfSequencerMilestone: must be sequencer milestone")
	root, available := ut.GetBaseStateRootOfSequencerMilestone(vid)
	if !available {
		return multistate.SugaredStateReader{}, false
	}
	rdr, err := multistate.NewReadable(ut.stateStore, root, 0)
	if err != nil {
		return multistate.SugaredStateReader{}, false
	}
	return multistate.MakeSugared(rdr), true
}

func (ut *UTXOTangle) HeaviestStateRootForLatestTimeSlot() common.VCommitment {
	return ut.heaviestBranchForLatestTimeSlot().root
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
func (ut *UTXOTangle) heaviestBranchForLatestTimeSlot() branch {
	var largestBranch branch
	var found bool

	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ut.forEachBranchSorted(ut.LatestTimeSlot(), func(vid *WrappedTx, br branch) bool {
		largestBranch = br
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

func (ut *UTXOTangle) ForEachBranchStateDesc(e core.TimeSlot, fun func(rdr multistate.SugaredStateReader) bool) error {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ut.forEachBranchSorted(e, func(vid *WrappedTx, br branch) bool {
		r, err := multistate.NewReadable(ut.stateStore, br.root, 0)
		util.AssertNoError(err)
		return fun(multistate.MakeSugared(r))
	}, true)
	return nil
}

func (ut *UTXOTangle) forEachBranchSorted(e core.TimeSlot, fun func(vid *WrappedTx, br branch) bool, desc bool) {
	branches, ok := ut.branches[e]
	if !ok {
		return
	}

	vids := util.SortKeys(branches, func(vid1, vid2 *WrappedTx) bool {
		if desc {
			return vid1.LedgerCoverage(TipSlots) > vid2.LedgerCoverage(TipSlots)
		}
		return vid1.LedgerCoverage(TipSlots) < vid2.LedgerCoverage(TipSlots)
	})
	for _, vid := range vids {
		if !fun(vid, branches[vid]) {
			return
		}
	}
}

func (ut *UTXOTangle) GetSequencerBootstrapOutputs(seqID core.ChainID) (chainOut WrappedOutput, stemOut WrappedOutput, found bool) {
	var seqOut, stem *core.OutputWithID
	var err error

	err = ut.ForEachBranchStateDesc(ut.LatestTimeSlot(), func(rdr multistate.SugaredStateReader) bool {
		if seqOut, err = rdr.GetChainOutput(&seqID); err == nil {
			stem = rdr.GetStemOutput()
			found = true
		}
		return !found
	})
	util.AssertNoError(err)

	if !found {
		return
	}
	if chainOut, found = ut.WrapOutput(seqOut); !found {
		return
	}
	stemOut, found = ut.WrapOutput(stem)
	return
}

func (ut *UTXOTangle) HasOutputInTimeSlot(e core.TimeSlot, oid *core.OutputID) bool {
	ret := false
	err := ut.ForEachBranchStateDesc(e, func(rdr multistate.SugaredStateReader) bool {
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
			br := ut.mustGetBranch(vid)
			rdr := multistate.MustNewSugaredStateReader(ut.stateStore, br.root)
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

func (ut *UTXOTangle) LoadSequencerStartOutputs(seqID core.ChainID, stateReader func() multistate.SugaredStateReader) (WrappedOutput, WrappedOutput, error) {
	rdr := stateReader()
	chainOut, err := rdr.GetChainOutput(&seqID)
	if err != nil {
		return WrappedOutput{}, WrappedOutput{}, fmt.Errorf("can't find chain output for %s: %v", seqID.Short(), err)
	}
	stemOut := rdr.GetStemOutput()

	return ut.MustWrapOutput(chainOut), ut.MustWrapOutput(stemOut), nil
}

func (ut *UTXOTangle) LoadSequencerStartOutputsDefault(seqID core.ChainID) (WrappedOutput, WrappedOutput, error) {
	chainOut, stemOut, err := ut.LoadSequencerStartOutputs(seqID, func() multistate.SugaredStateReader {
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
