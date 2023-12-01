package multistate

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
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

func writeRootRecord(w common.KVWriter, branchTxID core.TransactionID, rootData RootRecord) {
	key := common.ConcatBytes([]byte{rootRecordDBPartition}, branchTxID[:])
	w.Set(key, rootData.Bytes())
}

func writeLatestSlot(w common.KVWriter, slot core.TimeSlot) {
	w.Set([]byte{latestSlotDBPartition}, slot.Bytes())
}

func FetchLatestSlot(store global.StateStore) core.TimeSlot {
	bin := store.Get([]byte{latestSlotDBPartition})
	if len(bin) == 0 {
		return 0
	}
	ret, err := core.TimeSlotFromBytes(bin)
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

func (lc *LedgerCoverage) Sum() (ret uint64) {
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

func RootRecordFromBytes(data []byte) (RootRecord, error) {
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(data, 3)
	if err != nil {
		return RootRecord{}, err
	}
	chainID, err := core.ChainIDFromBytes(arr.At(0))
	if err != nil {
		return RootRecord{}, err
	}
	root, err := common.VectorCommitmentFromBytes(core.CommitmentModel, arr.At(1))
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

func iterateAllRootRecords(store global.StateStore, fun func(branchTxID core.TransactionID, rootData RootRecord) bool) {
	store.Iterator([]byte{rootRecordDBPartition}).Iterate(func(k, data []byte) bool {
		txid, err := core.TransactionIDFromBytes(k[1:])
		util.AssertNoError(err)

		rootData, err := RootRecordFromBytes(data)
		util.AssertNoError(err)

		return fun(txid, rootData)
	})
}

func iterateRootRecordsOfParticularSlots(store global.StateStore, fun func(branchTxID core.TransactionID, rootData RootRecord) bool, slots []core.TimeSlot) {
	prefix := [5]byte{rootRecordDBPartition, 0, 0, 0, 0}
	for _, s := range slots {
		slotPrefix := core.NewTransactionIDPrefix(s, true, true)
		copy(prefix[1:], slotPrefix[:])

		store.Iterator(prefix[:]).Iterate(func(k, data []byte) bool {
			txid, err := core.TransactionIDFromBytes(k[1:])
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
func IterateRootRecords(store global.StateStore, fun func(branchTxID core.TransactionID, rootData RootRecord) bool, optSlot ...core.TimeSlot) {
	if len(optSlot) == 0 {
		iterateAllRootRecords(store, fun)
	}
	iterateRootRecordsOfParticularSlots(store, fun, optSlot)
}

// FetchRootRecord returns root data, stem output index and existence flag
// Exactly one root record must exist for the branch transaction
func FetchRootRecord(store global.StateStore, branchTxID core.TransactionID) (ret RootRecord, found bool) {
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

func FetchRootRecordsNSlotsBack(store global.StateStore, nBack int) []RootRecord {
	latestSlot := FetchLatestSlot(store)
	if core.TimeSlot(nBack) >= latestSlot {
		return FetchAllRootRecords(store)
	}
	if latestSlot == 0 {
		return nil
	}
	return FetchRootRecords(store, util.MakeRange(latestSlot-core.TimeSlot(nBack), latestSlot)...)
}

func FetchAllRootRecords(store global.StateStore) []RootRecord {
	ret := make([]RootRecord, 0)
	IterateRootRecords(store, func(_ core.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	})
	return ret
}

func FetchRootRecords(store global.StateStore, slots ...core.TimeSlot) []RootRecord {
	if len(slots) == 0 {
		return nil
	}
	ret := make([]RootRecord, 0)
	IterateRootRecords(store, func(_ core.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	}, slots...)

	return ret
}

func FetchStemOutputID(store global.StateStore, branchTxID core.TransactionID) (core.OutputID, bool) {
	rr, ok := FetchRootRecord(store, branchTxID)
	if !ok {
		return core.OutputID{}, false
	}
	rdr, err := NewSugaredReadableState(store, rr.Root, 0)
	util.AssertNoError(err)

	o := rdr.GetStemOutput()
	return o.ID, true
}

func FetchBranchData(store global.StateStore, branchTxID core.TransactionID) (BranchData, bool) {
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

	return util.Sort(ret, func(i, j int) bool {
		return ret[i].LedgerCoverage.Sum() > ret[j].LedgerCoverage.Sum()
	})
}

// FetchHeaviestBranchChainNSlotsBack descending by epoch
func FetchHeaviestBranchChainNSlotsBack(store global.StateStore, nBack int) []*BranchData {
	rootData := make(map[core.TransactionID]RootRecord)
	latestSlot := FetchLatestSlot(store)

	if nBack < 0 {
		IterateRootRecords(store, func(branchTxID core.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		})
	} else {
		IterateRootRecords(store, func(branchTxID core.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		}, util.MakeRange(latestSlot-core.TimeSlot(nBack), latestSlot)...)
	}

	sortedTxIDs := util.SortKeys(rootData, func(k1, k2 core.TransactionID) bool {
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
		util.Assertf(bd.SequencerOutput.ID.TimeSlot() < lastInTheChain.Stem.ID.TimeSlot(), "bd.SequencerOutput.ID.TimeSlot() < lastInTheChain.TimeSlot()")

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
