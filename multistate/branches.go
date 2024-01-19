package multistate

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

const (
	rootRecordDBPartition = immutable.PartitionOther
	latestSlotDBPartition = rootRecordDBPartition + 1
)

// TODO replace general.StateStore with common.KVReader wherever relevant

func writeRootRecord(w common.KVWriter, branchTxID ledger.TransactionID, rootData RootRecord) {
	key := common.ConcatBytes([]byte{rootRecordDBPartition}, branchTxID[:])
	w.Set(key, rootData.Bytes())
}

func writeLatestSlot(w common.KVWriter, slot ledger.Slot) {
	w.Set([]byte{latestSlotDBPartition}, slot.Bytes())
}

func FetchLatestSlot(store global.StateStore) ledger.Slot {
	bin := store.Get([]byte{latestSlotDBPartition})
	if len(bin) == 0 {
		return 0
	}
	ret, err := ledger.TimeSlotFromBytes(bin)
	common.AssertNoError(err)
	return ret
}

func (lc *LedgerCoverage) MakeNext(shift int, nextDelta uint64) (ret LedgerCoverage) {
	util.Assertf(shift >= 1, "shift >= 1")
	if shift < HistoryCoverageDeltas {
		copy(ret[shift:], lc[:])
	}
	ret[0] = nextDelta
	return
}

func (lc *LedgerCoverage) LatestDelta() uint64 {
	return lc[0]
}

func (lc *LedgerCoverage) Sum() (ret uint64) {
	if lc == nil {
		return 0
	}
	for _, v := range lc {
		ret += v
	}
	return
}

func (lc *LedgerCoverage) Bytes() []byte {
	util.Assertf(len(lc) == HistoryCoverageDeltas, "len(lc) == HistoryCoverageDeltas")
	ret := make([]byte, len(lc)*8)
	for i, d := range lc {
		binary.BigEndian.PutUint64(ret[i*8:(i+1)*8], d)
	}
	return ret
}

func (lc *LedgerCoverage) String() string {
	if lc == nil {
		return "0"
	}
	all := make([]string, len(lc))
	for i, c := range lc {
		all[i] = util.GoThousands(c)
	}
	return fmt.Sprintf("sum(%s) -> %s", strings.Join(all, ", "), util.GoThousands(lc.Sum()))
}

func LedgerCoverageFromBytes(data []byte) (ret LedgerCoverage, err error) {
	if len(data) != HistoryCoverageDeltas*8 {
		err = fmt.Errorf("LedgerCoverageFromBytes: wrong data size")
		return
	}
	for i := 0; i < HistoryCoverageDeltas; i++ {
		ret[i] = binary.BigEndian.Uint64(data[i*8 : (i+1)*8])
	}
	return
}

func (r *RootRecord) Bytes() []byte {
	arr := lazybytes.EmptyArray(3)
	arr.Push(r.SequencerID.Bytes())
	arr.Push(r.Root.Bytes())
	arr.Push(r.LedgerCoverage.Bytes())
	return arr.Bytes()
}

func (br *BranchData) TxID() *ledger.TransactionID {
	ret := br.Stem.ID.TransactionID()
	return &ret
}

func RootRecordFromBytes(data []byte) (RootRecord, error) {
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(data, 3)
	if err != nil {
		return RootRecord{}, err
	}
	chainID, err := ledger.ChainIDFromBytes(arr.At(0))
	if err != nil {
		return RootRecord{}, err
	}
	root, err := common.VectorCommitmentFromBytes(ledger.CommitmentModel, arr.At(1))
	if err != nil {
		return RootRecord{}, err
	}
	if len(arr.At(2)) != 8*HistoryCoverageDeltas {
		return RootRecord{}, fmt.Errorf("RootRecordFromBytes: wrong data length")
	}
	coverage, err := LedgerCoverageFromBytes(arr.At(2))
	if err != nil {
		return RootRecord{}, err
	}

	return RootRecord{
		Root:           root,
		SequencerID:    chainID,
		LedgerCoverage: coverage,
	}, nil
}

func iterateAllRootRecords(store global.StateStore, fun func(branchTxID ledger.TransactionID, rootData RootRecord) bool) {
	store.Iterator([]byte{rootRecordDBPartition}).Iterate(func(k, data []byte) bool {
		txid, err := ledger.TransactionIDFromBytes(k[1:])
		util.AssertNoError(err)

		rootData, err := RootRecordFromBytes(data)
		util.AssertNoError(err)

		return fun(txid, rootData)
	})
}

func iterateRootRecordsOfParticularSlots(store global.StateStore, fun func(branchTxID ledger.TransactionID, rootData RootRecord) bool, slots []ledger.Slot) {
	prefix := [5]byte{rootRecordDBPartition, 0, 0, 0, 0}
	for _, s := range slots {
		slotPrefix := ledger.NewTransactionIDPrefix(s, true, true)
		copy(prefix[1:], slotPrefix[:])

		store.Iterator(prefix[:]).Iterate(func(k, data []byte) bool {
			txid, err := ledger.TransactionIDFromBytes(k[1:])
			util.AssertNoError(err)

			rootData, err := RootRecordFromBytes(data)
			util.AssertNoError(err)

			return fun(txid, rootData)
		})
	}
}

// IterateRootRecords iterates root records in the store:
// - if len(optSlot) > 0, it iterates specific slots
// - if len(optSlot) == 0, it iterates all records in the store
func IterateRootRecords(store global.StateStore, fun func(branchTxID ledger.TransactionID, rootData RootRecord) bool, optSlot ...ledger.Slot) {
	if len(optSlot) == 0 {
		iterateAllRootRecords(store, fun)
	}
	iterateRootRecordsOfParticularSlots(store, fun, optSlot)
}

// FetchRootRecord returns root data, stem output index and existence flag
// Exactly one root record must exist for the branch transaction
func FetchRootRecord(store global.StateStore, branchTxID ledger.TransactionID) (ret RootRecord, found bool) {
	key := common.Concat(rootRecordDBPartition, branchTxID[:])
	data := store.Get(key)
	if len(data) == 0 {
		return
	}
	ret, err := RootRecordFromBytes(data)
	util.AssertNoError(err)
	found = true
	return
}

func FetchAnyLatestRootRecord(store global.StateStore) RootRecord {
	recs := FetchRootRecords(store, FetchLatestSlot(store))
	util.Assertf(len(recs) > 0, "len(recs)>0")
	return recs[0]
}

func FetchRootRecordsNSlotsBack(store global.StateStore, nBack int) []RootRecord {
	latestSlot := FetchLatestSlot(store)
	if ledger.Slot(nBack) >= latestSlot {
		return FetchAllRootRecords(store)
	}
	if latestSlot == 0 {
		return nil
	}
	return FetchRootRecords(store, util.MakeRange(latestSlot-ledger.Slot(nBack), latestSlot)...)
}

func FetchAllRootRecords(store global.StateStore) []RootRecord {
	ret := make([]RootRecord, 0)
	IterateRootRecords(store, func(_ ledger.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	})
	return ret
}

func FetchRootRecords(store global.StateStore, slots ...ledger.Slot) []RootRecord {
	if len(slots) == 0 {
		return nil
	}
	ret := make([]RootRecord, 0)
	IterateRootRecords(store, func(_ ledger.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	}, slots...)

	return ret
}

func FetchStemOutputID(store global.StateStore, branchTxID ledger.TransactionID) (ledger.OutputID, bool) {
	rr, ok := FetchRootRecord(store, branchTxID)
	if !ok {
		return ledger.OutputID{}, false
	}
	rdr, err := NewSugaredReadableState(store, rr.Root, 0)
	util.AssertNoError(err)

	o := rdr.GetStemOutput()
	return o.ID, true
}

func FetchBranchData(store global.StateStore, branchTxID ledger.TransactionID) (BranchData, bool) {
	if rd, found := FetchRootRecord(store, branchTxID); found {
		return FetchBranchDataByRoot(store, rd), true
	}
	return BranchData{}, false
}

func FetchBranchDataByRoot(store global.StateStore, rootData RootRecord) BranchData {
	rdr, err := NewSugaredReadableState(store, rootData.Root, 0)
	util.AssertNoError(err)

	seqOut, err := rdr.GetChainOutput(&rootData.SequencerID)
	util.AssertNoError(err)

	return BranchData{
		RootRecord:      rootData,
		Stem:            rdr.GetStemOutput(),
		SequencerOutput: seqOut,
	}
}

func FetchBranchDataMulti(store global.StateStore, rootData ...RootRecord) []*BranchData {
	ret := make([]*BranchData, len(rootData))
	for i, rd := range rootData {
		bd := FetchBranchDataByRoot(store, rd)
		ret[i] = &bd
	}
	return ret
}

// FetchLatestBranches branches of the latest slot sorted by coverage descending
func FetchLatestBranches(store global.StateStore) []*BranchData {
	ret := FetchBranchDataMulti(store, FetchRootRecords(store, FetchLatestSlot(store))...)

	sort.Slice(ret, func(i, j int) bool {
		return ret[i].LedgerCoverage.Sum() > ret[j].LedgerCoverage.Sum()
	})
	return ret
}

func FetchLatestBranchTransactionIDs(store global.StateStore) []ledger.TransactionID {
	bd := FetchLatestBranches(store)
	ret := make([]ledger.TransactionID, len(bd))

	for i := range ret {
		ret[i] = bd[i].Stem.ID.TransactionID()
	}
	return ret
}

// FetchHeaviestBranchChainNSlotsBack descending by epoch
func FetchHeaviestBranchChainNSlotsBack(store global.StateStore, nBack int) []*BranchData {
	rootData := make(map[ledger.TransactionID]RootRecord)
	latestSlot := FetchLatestSlot(store)

	if nBack < 0 {
		IterateRootRecords(store, func(branchTxID ledger.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		})
	} else {
		IterateRootRecords(store, func(branchTxID ledger.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		}, util.MakeRange(latestSlot-ledger.Slot(nBack), latestSlot)...)
	}

	sortedTxIDs := util.SortKeys(rootData, func(k1, k2 ledger.TransactionID) bool {
		// descending by epoch
		return k1.TimeSlot() > k2.TimeSlot()
	})

	latestBD := FetchLatestBranches(store)
	var lastInTheChain *BranchData

	for _, bd := range latestBD {
		if lastInTheChain == nil || bd.LedgerCoverage.Sum() > lastInTheChain.LedgerCoverage.Sum() {
			lastInTheChain = bd
		}
	}

	ret := append(make([]*BranchData, 0), lastInTheChain)

	for _, txid := range sortedTxIDs {
		rd := rootData[txid]
		bd := FetchBranchDataByRoot(store, rd)

		if bd.SequencerOutput.ID.TimeSlot() == lastInTheChain.Stem.ID.TimeSlot() {
			continue
		}
		util.Assertf(bd.SequencerOutput.ID.TimeSlot() < lastInTheChain.Stem.ID.TimeSlot(), "bd.SequencerOutput.ID.Slot() < lastInTheChain.Slot()")

		stemLock, ok := lastInTheChain.Stem.Output.StemLock()
		util.Assertf(ok, "stem output expected")

		if bd.Stem.ID != stemLock.PredecessorOutputID {
			continue
		}
		lastInTheChain = &bd
		ret = append(ret, lastInTheChain)
	}
	return ret
}

// BranchIsDescendantOf returns true if predecessor txid is known in the descendents state
func BranchIsDescendantOf(descendant, predecessor *ledger.TransactionID, getStore func() global.StateStore) bool {
	util.Assertf(descendant.IsBranchTransaction(), "must be a branch ts")

	if ledger.EqualTransactionIDs(descendant, predecessor) {
		return true
	}
	if descendant.Timestamp().Before(predecessor.Timestamp()) {
		return false
	}
	store := getStore()
	rr, found := FetchRootRecord(store, *descendant)
	if !found {
		return false
	}
	rdr, err := NewReadable(store, rr.Root)
	if err != nil {
		return false
	}

	return rdr.KnowsCommittedTransaction(predecessor)
}
