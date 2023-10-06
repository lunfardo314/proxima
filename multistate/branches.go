package multistate

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

const (
	rootRecordDBPartition = immutable.PartitionOther
	latestSlotDBPartition = rootRecordDBPartition + 1
)

func writeRootRecord(w common.KVWriter, stemOutputID core.OutputID, rootData RootData) {
	key := common.ConcatBytes([]byte{rootRecordDBPartition}, stemOutputID[:])
	w.Set(key, rootData.Bytes())
}

func writeLatestSlot(w common.KVWriter, slot core.TimeSlot) {
	w.Set([]byte{latestSlotDBPartition}, slot.Bytes())
}

func FetchLatestSlot(store general.StateStore) core.TimeSlot {
	bin := store.Get([]byte{latestSlotDBPartition})
	if len(bin) == 0 {
		return 0
	}
	ret, err := core.TimeSlotFromBytes(bin)
	common.AssertNoError(err)
	return ret
}

func (r *RootData) Bytes() []byte {
	arr := lazybytes.EmptyArray(3)
	arr.Push(r.SequencerID.Bytes())
	arr.Push(r.Root.Bytes())
	var coverageBin [8]byte
	binary.BigEndian.PutUint64(coverageBin[:], r.Coverage)
	arr.Push(coverageBin[:])

	return arr.Bytes()
}

func RootDataFromBytes(data []byte) (RootData, error) {
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(data, 3)
	if err != nil {
		return RootData{}, err
	}
	chainID, err := core.ChainIDFromBytes(arr.At(0))
	if err != nil {
		return RootData{}, err
	}
	root, err := common.VectorCommitmentFromBytes(core.CommitmentModel, arr.At(1))
	if err != nil {
		return RootData{}, err
	}
	if len(arr.At(2)) != 8 {
		return RootData{}, fmt.Errorf("wrong data length")
	}
	coverage := binary.BigEndian.Uint64(arr.At(2))

	return RootData{
		Root:        root,
		SequencerID: chainID,
		Coverage:    coverage,
	}, nil
}

func iterateAllRootRecords(store general.StateStore, fun func(stemOid core.OutputID, rootData RootData) bool) {
	store.Iterator([]byte{rootRecordDBPartition}).Iterate(func(k, data []byte) bool {
		oid, err := core.OutputIDFromBytes(k[1:])
		util.AssertNoError(err)

		rootData, err := RootDataFromBytes(data)
		util.AssertNoError(err)

		return fun(oid, rootData)
	})
}

func iterateRootRecordsOfParticularSlots(store general.StateStore, fun func(stemOid core.OutputID, rootData RootData) bool, slots []core.TimeSlot) {
	prefix := [5]byte{rootRecordDBPartition, 0, 0, 0, 0}
	for _, s := range slots {
		slotPrefix := core.NewTransactionIDPrefix(s, true, true)
		copy(prefix[1:], slotPrefix[:])

		store.Iterator(prefix[:]).Iterate(func(k, data []byte) bool {
			oid, err := core.OutputIDFromBytes(k[1:])
			util.AssertNoError(err)

			rootData, err := RootDataFromBytes(data)
			util.AssertNoError(err)

			return fun(oid, rootData)
		})
	}
}

// IterateRootRecords iterates root records in the store:
// - if len(optSlot) > 0, it iterates specific slots
// - if len(optSlot) == 0, it iterates all records in the store
func IterateRootRecords(store general.StateStore, fun func(stemOid core.OutputID, rootData RootData) bool, optSlot ...core.TimeSlot) {
	if len(optSlot) == 0 {
		iterateAllRootRecords(store, fun)
	}
	iterateRootRecordsOfParticularSlots(store, fun, optSlot)
}

// FetchRootDataByTransactionID returns root data, stem output index and existence flag
// Exactly one root record must exist for the transaction
func FetchRootDataByTransactionID(store general.StateStore, txid core.TransactionID) (ret RootData, found bool) {
	IterateRootRecords(store, func(stemOid core.OutputID, rootData RootData) bool {
		if stemOid.TransactionID() == txid {
			ret = rootData
			found = true
			return false
		}
		return true
	}, txid.TimeSlot())
	return
}

func FetchRootRecordsNSlotsBack(store general.StateStore, nBack int) []RootData {
	latestSlot := FetchLatestSlot(store)
	if core.TimeSlot(nBack) >= latestSlot {
		return FetchAllRootRecords(store)
	}
	if latestSlot == 0 {
		return nil
	}
	return FetchRootRecords(store, util.MakeRange(latestSlot-core.TimeSlot(nBack), latestSlot)...)
}

func FetchAllRootRecords(store general.StateStore) []RootData {
	ret := make([]RootData, 0)
	IterateRootRecords(store, func(stemOid core.OutputID, rootData RootData) bool {
		ret = append(ret, rootData)
		return true
	})
	return ret
}

func FetchRootRecords(store general.StateStore, slots ...core.TimeSlot) []RootData {
	if len(slots) == 0 {
		return nil
	}
	ret := make([]RootData, 0)
	IterateRootRecords(store, func(stemOid core.OutputID, rootData RootData) bool {
		ret = append(ret, rootData)
		return true
	}, slots...)

	return ret
}

func FetchBranchDataByTransactionID(store general.StateStore, txid core.TransactionID) (BranchData, bool) {
	if rd, found := FetchRootDataByTransactionID(store, txid); found {
		return FetchBranchDataByRoot(store, rd), true
	}
	return BranchData{}, false
}

func FetchBranchDataByRoot(store general.StateStore, rootData RootData) BranchData {
	rdr, err := NewSugaredReadableState(store, rootData.Root, 0)
	util.AssertNoError(err)

	seqOut, err := rdr.GetChainOutput(&rootData.SequencerID)
	util.AssertNoError(err)

	return BranchData{
		RootData:  rootData,
		Stem:      rdr.GetStemOutput(),
		SeqOutput: seqOut,
	}
}

func FetchBranchDataMulti(store general.StateStore, rootData ...RootData) []*BranchData {
	ret := make([]*BranchData, len(rootData))
	for i, rd := range rootData {
		bd := FetchBranchDataByRoot(store, rd)
		ret[i] = &bd
	}
	return ret
}

// FetchLatestBranches branches of the latest slot sorted by coverage descending
func FetchLatestBranches(store general.StateStore) []*BranchData {
	ret := FetchBranchDataMulti(store, FetchRootRecords(store, FetchLatestSlot(store))...)

	return util.Sort(ret, func(i, j int) bool {
		return ret[i].Coverage > ret[j].Coverage
	})
}

func FetchHeaviestBranchChainNSlotsBack(store general.StateStore, nBack int) []*BranchData {
	rootData := make(map[core.TransactionID]RootData)
	latestSlot := FetchLatestSlot(store)

	if nBack < 0 {
		IterateRootRecords(store, func(stemOid core.OutputID, rd RootData) bool {
			rootData[stemOid.TransactionID()] = rd
			return true
		})
	} else {
		IterateRootRecords(store, func(stemOid core.OutputID, rd RootData) bool {
			rootData[stemOid.TransactionID()] = rd
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
		if lastInTheChain == nil || bd.Coverage > lastInTheChain.Coverage {
			lastInTheChain = bd
		}
	}

	ret := append(make([]*BranchData, 0), lastInTheChain)

	for _, txid := range sortedTxIDs {
		rd := rootData[txid]
		bd := FetchBranchDataByRoot(store, rd)

		if bd.SeqOutput.ID.TimeSlot() == lastInTheChain.Stem.ID.TimeSlot() {
			continue
		}
		util.Assertf(bd.SeqOutput.ID.TimeSlot() < lastInTheChain.Stem.ID.TimeSlot(), "bd.SeqOutput.ID.TimeSlot() < lastInTheChain.TimeSlot()")

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
