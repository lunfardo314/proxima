package multistate

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// two additional partitions of the k/v store
const (
	// rootRecordDBPartition
	rootRecordDBPartition   = immutable.PartitionOther
	latestSlotDBPartition   = rootRecordDBPartition + 1
	earliestSlotDBPartition = latestSlotDBPartition + 1
)

func WriteRootRecord(w common.KVWriter, branchTxID ledger.TransactionID, rootData RootRecord) {
	common.UseConcatBytes(func(key []byte) {
		w.Set(key, rootData.Bytes())
	}, []byte{rootRecordDBPartition}, branchTxID[:])
}

func WriteLatestSlotRecord(w common.KVWriter, slot ledger.Slot) {
	w.Set([]byte{latestSlotDBPartition}, slot.Bytes())
}

func WriteEarliestSlotRecord(w common.KVWriter, slot ledger.Slot) {
	w.Set([]byte{earliestSlotDBPartition}, slot.Bytes())
}

// FetchLatestCommittedSlot fetches latest recorded slot
func FetchLatestCommittedSlot(store common.KVReader) ledger.Slot {
	bin := store.Get([]byte{latestSlotDBPartition})
	if len(bin) == 0 {
		return 0
	}
	ret, err := ledger.SlotFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

// FetchEarliestSlot return earliest slot among roots in the multi-state DB.
// It is set when multi-state DB is initialized and then remains immutable. For genesis database it is 0,
// For DB created from snapshot it is slot of the snapshot
func FetchEarliestSlot(store common.KVReader) ledger.Slot {
	bin := store.Get([]byte{earliestSlotDBPartition})
	util.Assertf(len(bin) > 0, "internal error: earliest state is not set")
	ret, err := ledger.SlotFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

func FetchSnapshotBranchID(store common.KVTraversableReader) ledger.TransactionID {
	earliestSlot := FetchEarliestSlot(store)
	roots := FetchRootRecords(store, earliestSlot)
	util.Assertf(len(roots) == 1, "expected exactly 1 root record in the earliest slot %d", earliestSlot)

	branchData := FetchBranchDataByRoot(store, roots[0])
	return branchData.Stem.ID.TransactionID()
}

const numberOfElementsInRootRecord = 6

func (r *RootRecord) Bytes() []byte {
	util.Assertf(r.LedgerCoverage > 0, "r.Coverage.LatestDelta() > 0")
	arr := lazybytes.EmptyArray(numberOfElementsInRootRecord)
	arr.Push(r.SequencerID.Bytes())
	arr.Push(r.Root.Bytes())

	var coverage [8]byte
	binary.BigEndian.PutUint64(coverage[:], r.LedgerCoverage)
	arr.Push(coverage[:])

	var slotInflationBin, supplyBin [8]byte
	binary.BigEndian.PutUint64(slotInflationBin[:], r.SlotInflation)

	arr.Push(slotInflationBin[:])
	binary.BigEndian.PutUint64(supplyBin[:], r.Supply)

	arr.Push(supplyBin[:])
	var nTxBin [4]byte
	binary.BigEndian.PutUint32(nTxBin[:], r.NumTransactions)

	arr.Push(nTxBin[:])
	util.Assertf(arr.NumElements() == numberOfElementsInRootRecord, "arr.NumElements() == 6")
	return arr.Bytes()
}

func (r *RootRecord) StringShort() string {
	return fmt.Sprintf("%s, %s, %s, %d",
		r.SequencerID.StringShort(), util.Th(r.LedgerCoverage), r.Root.String(), r.NumTransactions)
}

func (r *RootRecord) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("sequencer ID: %s", r.SequencerID.String()).
		Add("supply: %s", util.Th(r.Supply)).
		Add("coverage: %s", util.Th(r.LedgerCoverage)).
		Add("healthy(%s): %v", global.FractionHealthyBranch.String(), global.IsHealthyCoverage(r.LedgerCoverage, r.Supply, global.FractionHealthyBranch))
	return ret
}

func (br *BranchData) LinesVerbose(prefix ...string) *lines.Lines {
	ret := br.RootRecord.Lines(prefix...)
	ret.Add("---- Stem ----").
		Append(br.Stem.Lines(prefix...)).
		Add("---- Sequencer output ----").
		Append(br.SequencerOutput.Lines(prefix...))
	return ret
}

func (br *BranchData) Lines(prefix ...string) *lines.Lines {
	return br.RootRecord.Lines(prefix...).
		Add("Stem output ID: %s", br.Stem.ID.String()).
		Add("Sequencer output ID: %s", br.SequencerOutput.ID.String())
}

func (r *RootRecord) LinesVerbose(prefix ...string) *lines.Lines {
	ret := r.Lines(prefix...)
	ret.Add("root: %s", r.Root.String()).
		Add("slot inflation: %s", util.Th(r.SlotInflation)).
		Add("num transactions: %d", r.NumTransactions)
	return ret
}

func RootRecordFromBytes(data []byte) (RootRecord, error) {
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(data, numberOfElementsInRootRecord)
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
	if len(arr.At(2)) != 8 || len(arr.At(3)) != 8 || len(arr.At(4)) != 8 || len(arr.At(5)) != 4 {
		return RootRecord{}, fmt.Errorf("wrong data length")
	}
	return RootRecord{
		Root:            root,
		SequencerID:     chainID,
		LedgerCoverage:  binary.BigEndian.Uint64(arr.At(2)),
		SlotInflation:   binary.BigEndian.Uint64(arr.At(3)),
		Supply:          binary.BigEndian.Uint64(arr.At(4)),
		NumTransactions: binary.BigEndian.Uint32(arr.At(5)),
	}, nil
}

func ValidInclusionThresholdFraction(numerator, denominator int) bool {
	return numerator > 0 && denominator > 0 && numerator < denominator && denominator >= 2
}

func AbsoluteStrongFinalityCoverageThreshold(supply uint64, numerator, denominator int) uint64 {
	// 2 *supply * theta
	return ((supply / uint64(denominator)) * uint64(numerator)) << 1 // this order to avoid overflow
}

// IsCoverageAboveThreshold the root is dominating if coverage last delta is more than numerator/denominator of the double supply
func (r *RootRecord) IsCoverageAboveThreshold(numerator, denominator int) bool {
	util.Assertf(ValidInclusionThresholdFraction(numerator, denominator), "IsCoverageAboveThreshold: fraction is wrong")
	return r.LedgerCoverage > AbsoluteStrongFinalityCoverageThreshold(r.Supply, numerator, denominator)
}

// TxID transaction ID of the branch, as taken from the stem output ID
func (br *BranchData) TxID() *ledger.TransactionID {
	ret := br.Stem.ID.TransactionID()
	return &ret
}

func iterateAllRootRecords(store common.Traversable, fun func(branchTxID ledger.TransactionID, rootData RootRecord) bool) {
	store.Iterator([]byte{rootRecordDBPartition}).Iterate(func(k, data []byte) bool {
		txid, err := ledger.TransactionIDFromBytes(k[1:])
		util.AssertNoError(err)

		rootData, err := RootRecordFromBytes(data)
		util.AssertNoError(err)

		return fun(txid, rootData)
	})
}

func iterateRootRecordsOfParticularSlots(store common.Traversable, fun func(branchTxID ledger.TransactionID, rootData RootRecord) bool, slots []ledger.Slot) {
	prefix := [5]byte{rootRecordDBPartition, 0, 0, 0, 0}
	for _, s := range slots {
		slotPrefix := ledger.NewTransactionIDPrefix(s, true)
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
func IterateRootRecords(store common.Traversable, fun func(branchTxID ledger.TransactionID, rootData RootRecord) bool, optSlot ...ledger.Slot) {
	if len(optSlot) == 0 {
		iterateAllRootRecords(store, fun)
	}
	iterateRootRecordsOfParticularSlots(store, fun, optSlot)
}

// FetchRootRecord returns root data, stem output index and existence flag
// Exactly one root record must exist for the branch transaction
func FetchRootRecord(store common.KVReader, branchTxID ledger.TransactionID) (ret RootRecord, found bool) {
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

// FetchAnyLatestRootRecord return first root record for the latest slot
func FetchAnyLatestRootRecord(store global.StateStoreReader) RootRecord {
	recs := FetchRootRecords(store, FetchLatestCommittedSlot(store))
	util.Assertf(len(recs) > 0, "len(recs)>0")
	return recs[0]
}

// FetchRootRecordsNSlotsBack load root records from N lates slots, present in the store
func FetchRootRecordsNSlotsBack(store global.StateStoreReader, nBack int) []RootRecord {
	if nBack <= 0 {
		return nil
	}
	ret := make([]RootRecord, 0)
	slotCount := 0
	for s := FetchLatestCommittedSlot(store); ; s-- {
		recs := FetchRootRecords(store, s)
		if len(recs) > 0 {
			ret = append(ret, recs...)
			slotCount++
		}
		if slotCount >= nBack || s == 0 {
			return ret
		}
	}
}

// FetchAllRootRecords returns all root records in the DB
func FetchAllRootRecords(store common.Traversable) []RootRecord {
	ret := make([]RootRecord, 0)
	IterateRootRecords(store, func(_ ledger.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	})
	return ret
}

// FetchRootRecords returns root records for particular slots in the DB
func FetchRootRecords(store common.Traversable, slots ...ledger.Slot) []RootRecord {
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

// FetchBranchData returns branch data by the branch transaction ID
func FetchBranchData(store common.KVReader, branchTxID ledger.TransactionID) (BranchData, bool) {
	if rd, found := FetchRootRecord(store, branchTxID); found {
		return FetchBranchDataByRoot(store, rd), true
	}
	return BranchData{}, false
}

// FetchBranchDataByRoot returns existing branch data by root record. The root record usually returned by FetchRootRecord
func FetchBranchDataByRoot(store common.KVReader, rootData RootRecord) BranchData {
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

// FetchBranchDataMulti returns branch records for particular root records
func FetchBranchDataMulti(store common.KVReader, rootData ...RootRecord) []*BranchData {
	ret := make([]*BranchData, len(rootData))
	for i, rd := range rootData {
		bd := FetchBranchDataByRoot(store, rd)
		ret[i] = &bd
	}
	return ret
}

// FetchLatestBranches branches of the latest slot sorted by coverage descending
func FetchLatestBranches(store global.StateStoreReader) []*BranchData {
	return FetchBranchDataMulti(store, FetchLatestRootRecords(store)...)
}

// FetchLatestRootRecords sorted descending by coverage
func FetchLatestRootRecords(store global.StateStoreReader) []RootRecord {
	ret := FetchRootRecords(store, FetchLatestCommittedSlot(store))
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].LedgerCoverage > ret[j].LedgerCoverage
	})
	return ret
}

// FetchLatestBranchTransactionIDs sorted descending by coverage
func FetchLatestBranchTransactionIDs(store global.StateStoreReader) []ledger.TransactionID {
	bd := FetchLatestBranches(store)
	ret := make([]ledger.TransactionID, len(bd))

	for i := range ret {
		ret[i] = bd[i].Stem.ID.TransactionID()
	}
	return ret
}

// FetchHeaviestBranchChainNSlotsBack descending by epoch
func FetchHeaviestBranchChainNSlotsBack(store global.StateStoreReader, nBack int) []*BranchData {
	rootData := make(map[ledger.TransactionID]RootRecord)
	latestSlot := FetchLatestCommittedSlot(store)

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

	sortedTxIDs := util.KeysSorted(rootData, func(k1, k2 ledger.TransactionID) bool {
		// descending by epoch
		return k1.Slot() > k2.Slot()
	})

	latestBD := FetchLatestBranches(store)
	var lastInTheChain *BranchData

	for _, bd := range latestBD {
		if lastInTheChain == nil || bd.LedgerCoverage > lastInTheChain.LedgerCoverage {
			lastInTheChain = bd
		}
	}

	ret := append(make([]*BranchData, 0), lastInTheChain)

	for _, txid := range sortedTxIDs {
		rd := rootData[txid]
		bd := FetchBranchDataByRoot(store, rd)

		if bd.SequencerOutput.ID.Slot() == lastInTheChain.Stem.ID.Slot() {
			continue
		}
		util.Assertf(bd.SequencerOutput.ID.Slot() < lastInTheChain.Stem.ID.Slot(), "bd.SequencerOutput.ID.Slot() < lastInTheChain.Slot()")

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

// BranchKnowsTransaction returns true if predecessor txid is known in the descendents state
func BranchKnowsTransaction(branchID, txid *ledger.TransactionID, getStore func() common.KVReader) bool {
	util.Assertf(branchID.IsBranchTransaction(), "must be a branch tx: %s", branchID.StringShort)

	if ledger.EqualTransactionIDs(branchID, txid) {
		return true
	}
	if branchID.Timestamp().Before(txid.Timestamp()) {
		return false
	}
	store := getStore()
	rr, found := FetchRootRecord(store, *branchID)
	if !found {
		return false
	}
	rdr, err := NewReadable(store, rr.Root)
	if err != nil {
		return false
	}

	return rdr.KnowsCommittedTransaction(txid)
}

func FindFirstBranch(store global.StateStoreReader, filter func(branch *BranchData) bool) *BranchData {
	var ret BranchData
	found := false
	IterateSlotsBack(store, func(slot ledger.Slot, roots []RootRecord) bool {
		for _, rootRecord := range roots {
			ret = FetchBranchDataByRoot(store, rootRecord)
			if found = filter(&ret); found {
				return false
			}
		}
		return true
	})
	if found {
		return &ret
	}
	return nil
}

// FindLatestHealthySlot finds latest slot, which has at least one branch
// with coverage > numerator/denominator * 2 * totalSupply
// Returns false flag if not found
func FindLatestHealthySlot(store global.StateStoreReader, fraction global.Fraction) (ledger.Slot, bool) {
	ret := FindFirstBranch(store, func(branch *BranchData) bool {
		return branch.IsHealthy(fraction)
	})
	if ret == nil {
		return 0, false
	}
	return ret.Stem.ID.Slot(), true
}

func (br *BranchData) IsHealthy(fraction global.Fraction) bool {
	return global.IsHealthyCoverage(br.LedgerCoverage, br.Supply, fraction)
}

// FirstHealthySlotIsNotBefore determines if first healthy slot is nor before tha refSlot.
// Usually refSlot is just few slots back, so the operation does not require
// each time traversing unbounded number of slots
func FirstHealthySlotIsNotBefore(store global.StateStoreReader, refSlot ledger.Slot, fraction global.Fraction) (ret bool) {
	IterateSlotsBack(store, func(slot ledger.Slot, roots []RootRecord) bool {
		for _, rr := range roots {
			br := FetchBranchDataByRoot(store, rr)
			if ret = br.IsHealthy(fraction); ret {
				return false // found
			}
		}
		return slot > refSlot
	})
	return
}

// IterateSlotsBack iterates  descending slots from latest committed slot down to the earliest available
func IterateSlotsBack(store global.StateStoreReader, fun func(slot ledger.Slot, roots []RootRecord) bool) {
	earliest := FetchEarliestSlot(store)
	slot := FetchLatestCommittedSlot(store)
	for {
		if !fun(slot, FetchRootRecords(store, slot)) || slot == earliest {
			return
		}
		slot--
	}
}

// FindRootsFromLatestHealthySlot return all roots from the latest slot which contains at least one healthy root
// Note that in theory it may not exist at all. Normally it will exist tho, because:
// - either database contain branches down to genesis
// - or it was started from snapshot which (normally) represents healthy state
func FindRootsFromLatestHealthySlot(store global.StateStoreReader, fraction global.Fraction) ([]RootRecord, bool) {
	var rootsFound []RootRecord

	IterateSlotsBack(store, func(slot ledger.Slot, roots []RootRecord) bool {
		if len(roots) == 0 {
			return true
		}
		maxElemIdx := util.IndexOfMaximum(roots, func(i, j int) bool {
			return roots[i].LedgerCoverage < roots[j].LedgerCoverage
		})
		if global.IsHealthyCoverage(roots[maxElemIdx].LedgerCoverage, roots[maxElemIdx].Supply, fraction) {
			rootsFound = roots
			return false
		}
		return true
	})
	return rootsFound, len(rootsFound) > 0
}

// IterateBranchChainBack iterates past chain of the tip branch (including the tip)
// Stops when current branch have no predecessor
func IterateBranchChainBack(store global.StateStoreReader, branch *BranchData, fun func(branchID *ledger.TransactionID, branch *BranchData) bool) {
	branchID := branch.Stem.ID.TransactionID()
	for {
		if !fun(&branchID, branch) {
			return
		}
		stemLock, ok := branch.Stem.Output.StemLock()
		util.Assertf(ok, "inconsistency: can't find stem lock")

		branchID = stemLock.PredecessorOutputID.TransactionID()
		root, found := FetchRootRecord(store, branchID)
		if !found {
			return
		}
		branch = util.Ref(FetchBranchDataByRoot(store, root))
	}
}

// FindLatestReliableBranch reliable branch is the latest branch, which is contained in any
// tip from the latest healthy branch with ledger coverage bigger than total supply
// Reliable branch is the latest global consensus state with big probability
// Returns nil if not found
func FindLatestReliableBranch(store global.StateStoreReader, fraction global.Fraction) *BranchData {
	tipRoots, ok := FindRootsFromLatestHealthySlot(store, fraction)
	if !ok {
		// if healthy slot does not exist, reliable branch does not exist too
		return nil
	}
	// filter out not healthy
	tipRoots = util.PurgeSlice(tipRoots, func(rr RootRecord) bool {
		return global.IsHealthyCoverage(rr.LedgerCoverage, rr.Supply, fraction)
	})
	util.Assertf(len(tipRoots) > 0, "len(tipRoots)>0")
	if len(tipRoots) == 1 {
		// if only one branch is in the latest healthy slot, it is the one reliable
		return util.Ref(FetchBranchDataByRoot(store, tipRoots[0]))
	}
	// there are several roots. We start traversing back from the heaviest one
	util.Assertf(len(tipRoots) > 1, "len(tipRoots)>1")
	rootMaxIdx := util.IndexOfMaximum(tipRoots, func(i, j int) bool {
		return tipRoots[i].LedgerCoverage < tipRoots[j].LedgerCoverage
	})
	util.Assertf(global.IsHealthyCoverage(tipRoots[rootMaxIdx].LedgerCoverage, tipRoots[rootMaxIdx].Supply, fraction),
		"global.IsHealthyCoverage(rootMax.LedgerCoverage, rootMax.Supply, fraction)")

	// we will be checking if transaction is contained in all roots from the latest healthy slot
	// For this we are creating a collection of state readers
	readers := make([]*Readable, 0, len(tipRoots)-1)
	for i := range tipRoots {
		// no need to check in the tip, skip it
		if !ledger.CommitmentModel.EqualCommitments(tipRoots[i].Root, tipRoots[rootMaxIdx].Root) {
			readers = append(readers, MustNewReadable(store, tipRoots[i].Root))
		}
	}
	util.Assertf(len(readers) > 0, "len(readers) > 0")

	chainTip := FetchBranchDataByRoot(store, tipRoots[rootMaxIdx])

	var branchFound *BranchData
	first := true
	IterateBranchChainBack(store, &chainTip, func(branchID *ledger.TransactionID, branch *BranchData) bool {
		if first {
			// skip the tip itself
			first = false
			return true
		}
		// check if the branch is included in every reader
		for _, rdr := range readers {
			if !rdr.KnowsCommittedTransaction(branchID) {
				// the transaction is not known by at least one of selected states,
				// it is not a reliable branch, keep traversing back
				return true
			}
		}
		// branchID is known in all tip states. It is the reliable  one
		branchFound = branch
		return false
	})
	return branchFound
}

// FindLatestReliableBranchAndNSlotsBack finds LRB and iterates n slots back along the main chain from LRB.
// It is a precaution if LRB will be orphaned later
func FindLatestReliableBranchAndNSlotsBack(store global.StateStoreReader, n int, fraction global.Fraction) (ret *BranchData) {
	lrb := FindLatestReliableBranch(store, fraction)
	if lrb == nil {
		return
	}
	IterateBranchChainBack(store, lrb, func(_ *ledger.TransactionID, branch *BranchData) bool {
		ret = branch
		n--
		return n > 0
	})
	return
}

// FindLatestReliableBranchWithSequencerID finds first branch with the given sequencerID in the main LRBID chain
func FindLatestReliableBranchWithSequencerID(store global.StateStoreReader, seqID ledger.ChainID, fraction global.Fraction) (ret *BranchData) {
	lrb := FindLatestReliableBranch(store, fraction)
	if lrb == nil {
		return nil
	}
	IterateBranchChainBack(store, lrb, func(_ *ledger.TransactionID, branch *BranchData) bool {
		if branch.SequencerID == seqID {
			ret = branch
			return false
		}
		return true
	})
	return
}

func GetMainChain(store global.StateStoreReader, fraction global.Fraction, max ...int) ([]*BranchData, error) {
	lrb := FindLatestReliableBranch(store, fraction)
	if lrb == nil {
		return nil, fmt.Errorf("can't find latest reliable brancg")
	}
	ret := make([]*BranchData, 0)
	IterateBranchChainBack(store, lrb, func(branchID *ledger.TransactionID, branch *BranchData) bool {
		ret = append(ret, branch)
		if len(max) > 0 && len(ret) >= max[0] {
			return false
		}
		return true
	})
	return ret, nil
}
