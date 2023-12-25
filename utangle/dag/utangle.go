package dag

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (ut *DAG) GetVertex(txid *core.TransactionID) *vertex.WrappedTx {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut.GetVertexNoLock(txid)
}

func (ut *DAG) GetIndexedStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) (global.IndexedStateReader, error) {
	rr, found := multistate.FetchRootRecord(ut.stateStore, *branchTxID)
	if !found {
		return nil, fmt.Errorf("root record for %s has not been found", branchTxID.StringShort())
	}
	return multistate.NewReadable(ut.stateStore, rr.Root, clearCacheAtSize...)
}

func (ut *DAG) MustGetIndexedStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) global.IndexedStateReader {
	ret, err := ut.GetIndexedStateReader(branchTxID, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

func (ut *DAG) _timeSlotsOrdered(descOrder ...bool) []core.TimeSlot {
	desc := false
	if len(descOrder) > 0 {
		desc = descOrder[0]
	}
	slots := set.New[core.TimeSlot]()
	for br := range ut.branches {
		slots.Insert(br.TimeSlot())
	}
	if desc {
		return util.SortKeys(slots, func(e1, e2 core.TimeSlot) bool {
			return e1 > e2
		})
	}
	return util.SortKeys(slots, func(e1, e2 core.TimeSlot) bool {
		return e1 < e2
	})
}

func (ut *DAG) _branchesDescending(slot core.TimeSlot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for br := range ut.branches {
		if br.TimeSlot() == slot {
			ret = append(ret, br)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].GetLedgerCoverage().Sum() > ret[j].GetLedgerCoverage().Sum()
	})
	return ret
}

// LatestTimeSlot latest time slot with some branches
func (ut *DAG) LatestTimeSlot() (ret core.TimeSlot) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut._latestTimeSlot()
}

func (ut *DAG) _latestTimeSlot() (ret core.TimeSlot) {
	for br := range ut.branches {
		if br.TimeSlot() > ret {
			ret = br.TimeSlot()
		}
	}
	return
}

func (ut *DAG) FindOutputInLatestTimeSlot(oid *core.OutputID) (ret *vertex.WrappedTx, rdr multistate.SugaredStateReader) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	for _, br := range ut._branchesDescending(ut._latestTimeSlot()) {
		if ut.branches[br].HasUTXO(oid) {
			return br, multistate.MakeSugared(ut.branches[br])
		}
	}
	return
}

func (ut *DAG) HasOutputInAllBranches(e core.TimeSlot, oid *core.OutputID) bool {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	for _, br := range ut._branchesDescending(e) {
		if !ut.branches[br].HasUTXO(oid) {
			return false
		}
	}
	return true
}

//func (ut *DAG) GetSequencerBootstrapOutputs(seqID core.ChainID) (chainOut vertex.WrappedOutput, stemOut vertex.WrappedOutput, found bool) {
//	branches := multistate.FetchLatestBranches(ut.stateStore)
//	for _, bd := range branches {
//		rdr := multistate.MustNewSugaredStateReader(ut.stateStore, bd.Root)
//		if seqOut, err := rdr.GetChainOutput(&seqID); err == nil {
//			retStem, ok, _ := ut.GetWrappedOutput(&bd.Stem.ID, rdr)
//			util.Assertf(ok, "can't get wrapped stem output %s", bd.Stem.ID.StringShort())
//
//			retSeq, ok, _ := ut.GetWrappedOutput(&seqOut.ID, rdr)
//			util.Assertf(ok, "can't get wrapped sequencer output %s", seqOut.ID.StringShort())
//
//			return retSeq, retStem, true
//		}
//	}
//	return vertex.WrappedOutput{}, vertex.WrappedOutput{}, false
//}
//

// ScanAccount collects all outputIDs, unlockable by the address
// It is a global scan of the tangle and of the state. Should be only done once upon sequencer start.
// Further on the account should be maintained by the listener
//func (ut *DAG) ScanAccount(addr core.AccountID, lastNTimeSlots int) set.Set[vertex.WrappedOutput] {
//	toScan, _, _ := ut.TipList(lastNTimeSlots)
//	ret := set.New[vertex.WrappedOutput]()
//
//	for _, vid := range toScan {
//		if vid.IsBranchTransaction() {
//			rdr := multistate.MustNewSugaredStateReader(ut.stateStore, ut.mustGetBranch(vid))
//			outs, err := rdr.GetIDSLockedInAccount(addr)
//			util.AssertNoError(err)
//
//			for i := range outs {
//				ow, ok, _ := ut.GetWrappedOutput(&outs[i], rdr)
//				util.Assertf(ok, "ScanAccount: can't fetch output %s", outs[i].StringShort())
//				ret.Insert(ow)
//			}
//		}
//
//		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
//			v.Tx.ForEachProducedOutput(func(i byte, o *core.Output, oid *core.OutputID) bool {
//				lck := o.Lock()
//				// Note, that stem output is unlockable with any account
//				if lck.Name() != core.StemLockName && lck.UnlockableWith(addr) {
//					ret.Insert(vertex.WrappedOutput{
//						VID:   vid,
//						Index: i,
//					})
//				}
//				return true
//			})
//		}})
//	}
//	return ret
//}

//func (ut *DAG) _baselineTime(nLatestSlots int) (time.Time, int) {
//	util.Assertf(nLatestSlots > 0, "nLatestSlots > 0")
//
//	var earliestSlot core.TimeSlot
//	count := 0
//	for _, s := range ut._timeSlotsOrdered(true) {
//		if len(ut.branches[s]) > 0 {
//			earliestSlot = s
//			count++
//		}
//		if count == nLatestSlots {
//			break
//		}
//	}
//	var baseline time.Time
//	first := true
//	for vid := range ut.branches[earliestSlot] {
//		if first || vid.Time().Before(baseline) {
//			baseline = vid.Time()
//			first = false
//		}
//	}
//	return baseline, count
//}

// _tipList returns:
// - time of the oldest branch of 'nLatestSlots' non-empty time slots (baseline time) or 'time.Time{}'
// - a list of transactions which has Time() not-older that baseline time
// - true if there are less or equal than 'nLatestSlots' non-empty time slots
// returns nil, time.Time{}, false if not enough timeslots
// list is randomly ordered
//func (ut *DAG) _tipList(nLatestSlots int) ([]*vertex.WrappedTx, time.Time, int) {
//	baseline, nSlots := ut._baselineTime(nLatestSlots)
//
//	ret := make([]*vertex.WrappedTx, 0)
//	for _, vid := range ut.dag {
//		if !vid.Time().Before(baseline) {
//			ret = append(ret, vid)
//		}
//	}
//	return ret, baseline, nSlots
//}
//
//func (ut *DAG) TipList(nLatestSlots int) ([]*vertex.WrappedTx, time.Time, int) {
//	ut.mutex.RLock()
//	defer ut.mutex.RUnlock()
//
//	return ut._tipList(nLatestSlots)
//}
