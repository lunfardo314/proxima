package multistate

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
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

func WriteRootRecord(w common.KVWriter, branchTxID base.TransactionID, rootData RootRecord) {
	common.UseConcatBytes(func(key []byte) {
		w.Set(key, rootData.Bytes())
	}, []byte{rootRecordDBPartition}, branchTxID[:])
}

func WriteLatestSlotRecord(w common.KVWriter, slot base.Slot) {
	w.Set([]byte{latestSlotDBPartition}, slot.Bytes())
}

func WriteEarliestSlotRecord(w common.KVWriter, slot base.Slot) {
	w.Set([]byte{earliestSlotDBPartition}, slot.Bytes())
}

// FetchLatestCommittedSlot fetches the latest recorded slot
func FetchLatestCommittedSlot(store common.KVReader) base.Slot {
	bin := store.Get([]byte{latestSlotDBPartition})
	if len(bin) == 0 {
		return 0
	}
	ret, err := base.SlotFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

// FetchEarliestSlot return earliest slot among roots in the multi-state DB.
// It is set when multi-state DB is initialized and then remains immutable. For genesis database it is 0,
// For DB created from snapshot it is slot of the snapshot
func FetchEarliestSlot(store common.KVReader) base.Slot {
	bin := store.Get([]byte{earliestSlotDBPartition})
	util.Assertf(len(bin) > 0, "internal error: earliest state is not set")
	ret, err := base.SlotFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

func FetchSnapshotBranchID(store common.KVTraversableReader) base.TransactionID {
	earliestSlot := FetchEarliestSlot(store)
	roots := FetchRootRecords(store, earliestSlot)
	util.Assertf(len(roots) == 1, "expected exactly 1 root record in the earliest slot %d", earliestSlot)

	branchData := FetchBranchDataByRoot(store, roots[0])
	return branchData.Stem.ID.TransactionID()
}

const numberOfElementsInRootRecord = 7

func (r *RootRecord) Bytes() []byte {
	arr := tuples.EmptyTupleEditable(numberOfElementsInRootRecord)
	arr.MustPush(r.SequencerID.Bytes())   // 0
	arr.MustPush(r.Root.Bytes())          // 1
	arr.MustPushUint64(r.CoverageDelta)   // 2
	arr.MustPushUint64(r.FrozenCoverage)  // 3
	arr.MustPushUint64(r.SlotInflation)   // 4
	arr.MustPushUint64(r.Supply)          // 5
	arr.MustPushUint32(r.NumTransactions) // 6

	util.Assertf(arr.NumElements() == numberOfElementsInRootRecord, "arr.NumElements() == %d", numberOfElementsInRootRecord)
	return arr.Bytes()
}

func RootRecordFromBytes(data []byte) (RootRecord, error) {
	arr, err := tuples.TupleFromBytes(data, numberOfElementsInRootRecord)
	if err != nil {
		return RootRecord{}, err
	}
	if arr.NumElements() != numberOfElementsInRootRecord {
		return RootRecord{}, fmt.Errorf("%d elements expected, got %d", numberOfElementsInRootRecord, arr.NumElements())
	}
	chainID, err := base.ChainIDFromBytes(arr.MustAt(0))
	if err != nil {
		return RootRecord{}, err
	}
	root, err := common.VectorCommitmentFromBytes(ledger.CommitmentModel, arr.MustAt(1))
	if err != nil {
		return RootRecord{}, err
	}
	for _, i := range []int{2, 3, 4, 5} {
		if len(arr.MustAt(i)) != 8 {
			return RootRecord{}, fmt.Errorf("wrong data length")
		}
	}
	if len(arr.MustAt(6)) != 4 {
		return RootRecord{}, fmt.Errorf("wrong data length")
	}
	return RootRecord{
		Root:            root,
		SequencerID:     chainID,
		CoverageDelta:   binary.BigEndian.Uint64(arr.MustAt(2)),
		FrozenCoverage:  binary.BigEndian.Uint64(arr.MustAt(3)),
		SlotInflation:   binary.BigEndian.Uint64(arr.MustAt(4)),
		Supply:          binary.BigEndian.Uint64(arr.MustAt(5)),
		NumTransactions: binary.BigEndian.Uint32(arr.MustAt(6)),
	}, nil
}

func (r *RootRecord) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	proc := (float32(r.FrozenCoverage) * 100) / float32(r.CoverageDelta)
	ret.Add("sequencer id: %s", r.SequencerID.String()).
		Add("supply: %s", util.Th(r.Supply)).
		Add("coverage delta: %s (%s, %.02f%%)", util.Th(r.CoverageDelta), util.Th(r.FrozenCoverage), proc).
		Add("healthy(%s): %v", global.FractionHealthyBranch.String(), global.IsHealthyCoverageDelta(r.CoverageDelta, r.Supply, global.FractionHealthyBranch))
	return ret
}

func (r *RootRecord) LinesVerbose(prefix ...string) *lines.Lines {
	ret := r.Lines(prefix...)
	ret.Add("root: %s", r.Root.String()).
		Add("slot inflation: %s", util.Th(r.SlotInflation)).
		Add("num transactions: %d", r.NumTransactions)
	return ret
}

func iterateAllRootRecords(store common.Traversable, fun func(branchTxID base.TransactionID, rootData RootRecord) bool) {
	store.Iterator([]byte{rootRecordDBPartition}).Iterate(func(k, data []byte) bool {
		txid, err := base.TransactionIDFromBytes(k[1:])
		util.AssertNoError(err)

		rootData, err := RootRecordFromBytes(data)
		util.AssertNoError(err)

		return fun(txid, rootData)
	})
}

func iterateRootRecordsOfParticularSlots(store common.Traversable, fun func(branchTxID base.TransactionID, rootData RootRecord) bool, slots []base.Slot) {
	prefix := [5]byte{rootRecordDBPartition, 0, 0, 0, 0}
	for _, s := range slots {
		s.PutBytes(prefix[1:])

		store.Iterator(prefix[:]).Iterate(func(k, data []byte) bool {
			txid, err := base.TransactionIDFromBytes(k[1:])
			util.AssertNoError(err)
			util.Assertf(txid.IsBranchTransaction(), "txid.IsBranchTransaction()")

			rootData, err := RootRecordFromBytes(data)
			util.AssertNoError(err)

			return fun(txid, rootData)
		})
	}
}

// IterateRootRecords iterates root records in the store:
// - if len(optSlot) > 0, it iterates specific slots
// - if len(optSlot) == 0, it iterates all records in the store
func IterateRootRecords(store common.Traversable, fun func(branchTxID base.TransactionID, rootData RootRecord) bool, optSlot ...base.Slot) {
	if len(optSlot) == 0 {
		iterateAllRootRecords(store, fun)
	}
	iterateRootRecordsOfParticularSlots(store, fun, optSlot)
}

// FetchRootRecord returns root data, stem output index and existence flag
// Exactly one root record must exist for the branch transaction
func FetchRootRecord(store common.KVReader, branchTxID base.TransactionID) (ret RootRecord, found bool) {
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
func FetchAnyLatestRootRecord(store StateStoreReader) RootRecord {
	recs := FetchRootRecords(store, FetchLatestCommittedSlot(store))
	util.Assertf(len(recs) > 0, "FetchAnyLatestRootRecord: can't find any root records in DB")
	return recs[0]
}

// FetchRootRecordsNSlotsBack load root records from N lates slots, present in the store
func FetchRootRecordsNSlotsBack(store StateStoreReader, nBack int) []RootRecord {
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
	IterateRootRecords(store, func(_ base.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	})
	return ret
}

// FetchRootRecords returns root records for particular slots in the DB
func FetchRootRecords(store common.Traversable, slots ...base.Slot) []RootRecord {
	if len(slots) == 0 {
		return nil
	}
	ret := make([]RootRecord, 0)
	IterateRootRecords(store, func(_ base.TransactionID, rootData RootRecord) bool {
		ret = append(ret, rootData)
		return true
	}, slots...)

	return ret
}

// FetchLatestRootRecords sorted descending by coverage
func FetchLatestRootRecords(store StateStoreReader) []RootRecord {
	ret := FetchRootRecords(store, FetchLatestCommittedSlot(store))
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].CoverageDelta > ret[j].CoverageDelta
	})
	return ret
}

// FetchBranchData returns branch data by the branch transaction id
func FetchBranchData(store common.KVReader, branchTxID base.TransactionID) (BranchData, bool) {
	if rd, found := FetchRootRecord(store, branchTxID); found {
		return FetchBranchDataByRoot(store, rd), true
	}
	return BranchData{}, false
}

// FetchBranchDataByRoot returns existing branch data by root record. The root record is usually returned by FetchRootRecord
func FetchBranchDataByRoot(store common.KVReader, rootData RootRecord) BranchData {
	rdr, err := NewSugaredReadableState(store, rootData.Root, 0)
	util.AssertNoError(err)

	seqOut, err := rdr.GetChainOutputWithID(rootData.SequencerID)
	util.AssertNoError(err)

	return BranchData{
		RootRecord:      rootData,
		Stem:            rdr.GetStemOutput(),
		SequencerOutput: seqOut,
	}
}

// FetchBranchDataMulti returns branch records for particular root records
func FetchBranchDataMulti(store StateStoreReader, rootData ...RootRecord) []*BranchData {
	ret := make([]*BranchData, len(rootData))
	for i, rd := range rootData {
		bd := FetchBranchDataByRoot(store, rd)
		ret[i] = &bd
	}
	return ret
}

// FetchLatestBranches branches of the latest slot sorted by coverage descending
func FetchLatestBranches(store StateStoreReader) []*BranchData {
	return FetchBranchDataMulti(store, FetchLatestRootRecords(store)...)
}

// FetchLatestBranchTransactionIDs sorted descending by coverage
func FetchLatestBranchTransactionIDs(store StateStoreReader) []base.TransactionID {
	bd := FetchLatestBranches(store)
	ret := make([]base.TransactionID, len(bd))

	for i := range ret {
		ret[i] = bd[i].Stem.ID.TransactionID()
	}
	return ret
}

// FetchHeaviestBranchChainNSlotsBack descending by epoch
func FetchHeaviestBranchChainNSlotsBack(store StateStoreReader, nBack int) []*BranchData {
	rootData := make(map[base.TransactionID]RootRecord)
	latestSlot := FetchLatestCommittedSlot(store)

	if nBack < 0 {
		IterateRootRecords(store, func(branchTxID base.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		})
	} else {
		IterateRootRecords(store, func(branchTxID base.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		}, util.MakeRange(latestSlot-base.Slot(nBack), latestSlot)...)
	}

	sortedTxIDs := util.KeysSorted(rootData, func(k1, k2 base.TransactionID) bool {
		// descending by epoch
		return k1.Slot() > k2.Slot()
	})

	latestBD := FetchLatestBranches(store)
	var lastInTheChain *BranchData

	for _, bd := range latestBD {
		if lastInTheChain == nil || bd.CoverageDelta > lastInTheChain.CoverageDelta {
			lastInTheChain = bd
		}
	}
	util.Assertf(lastInTheChain != nil, "lastInTheChain != nil")

	ret := append(make([]*BranchData, 0), lastInTheChain)

	for _, txid := range sortedTxIDs {
		rd := rootData[txid]
		bd := FetchBranchDataByRoot(store, rd)

		if bd.SequencerOutput.ID.Slot() == lastInTheChain.Stem.ID.Slot() {
			continue
		}
		util.Assertf(bd.SequencerOutput.ID.Slot() < lastInTheChain.Stem.ID.Slot(), "bd.SequencerOutput.id.Slot() < lastInTheChain.Slot()")

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

func FindFirstBranchThat(store StateStoreReader, filter func(branch *BranchData) bool) *BranchData {
	var ret BranchData
	found := false
	IterateSlotsBack(store, func(slot base.Slot, roots []RootRecord) bool {
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
func FindLatestHealthySlot(store StateStoreReader, fraction global.Fraction) (base.Slot, bool) {
	ret := FindFirstBranchThat(store, func(branch *BranchData) bool {
		return branch.IsHealthy(fraction)
	})
	if ret == nil {
		return 0, false
	}
	return ret.Stem.ID.Slot(), true
}

// FirstHealthySlotIsNotBefore determines if first healthy slot is not before tha refSlot.
// Usually refSlot is just few slots back, so the operation does not require
// each time traversing unbounded number of slots
func FirstHealthySlotIsNotBefore(store StateStoreReader, refSlot base.Slot, fraction global.Fraction) (ret bool) {
	IterateSlotsBack(store, func(slot base.Slot, roots []RootRecord) bool {
		if slot < refSlot {
			return false
		}
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

// IterateSlotsBack iterates descending slots from the latest committed slot down to the earliest available
func IterateSlotsBack(store StateStoreReader, fun func(slot base.Slot, roots []RootRecord) bool) {
	earliest := FetchEarliestSlot(store)
	slot := FetchLatestCommittedSlot(store)
	for {
		if !fun(slot, FetchRootRecords(store, slot)) || slot == earliest {
			return
		}
		slot--
	}
}

// FindRootsFromLatestHealthySlot
// Healthy slot is a slot which contains at least one healthy root.
// Function returns all roots from the latest healthy slot.
// Note that in theory latest healthy slot it may not exist at all, i.e. all slot in the DB does not contain any healthy root.
// Normally it will exist tho, because:
// - either database contains all branches down to genesis
// - or it was started from snapshot which (normally) represents a healthy state
func FindRootsFromLatestHealthySlot(store StateStoreReader, fraction global.Fraction) ([]RootRecord, bool) {
	var rootsFound []RootRecord

	IterateSlotsBack(store, func(slot base.Slot, roots []RootRecord) bool {
		if len(roots) == 0 {
			return true
		}
		maxElemIdx := util.IndexOfMaximum(roots, func(i, j int) bool {
			return roots[i].CoverageDelta < roots[j].CoverageDelta
		})
		if global.IsHealthyCoverageDelta(roots[maxElemIdx].CoverageDelta, roots[maxElemIdx].Supply, fraction) {
			rootsFound = roots
			return false
		}
		return true
	})
	return rootsFound, len(rootsFound) > 0
}

// IterateBranchChainBack iterates the past chain of the tip branch (including the tip)
// Stops when the current branch has no predecessor
func IterateBranchChainBack(store StateStoreReader, branch *BranchData, fun func(branchID *base.TransactionID, branch *BranchData) bool) {
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

// FindLatestReliableBranch latest reliable branch (LRB) is the latest branch, which is contained in any
// tip from the latest healthy branch with coverage delta bigger than the fraction of total supply.
// Reliable branch is the latest global consensus state with big probability
// Returns nil if not found
func FindLatestReliableBranch(store StateStoreReader, fraction global.Fraction) *BranchData {
	tipRoots, ok := FindRootsFromLatestHealthySlot(store, fraction)
	if !ok {
		// if the healthy slot does not exist, the reliable branch does not exist either
		return nil
	}
	// filter out not healthy roots in the healthy slot
	tipRoots = util.PurgeSlice(tipRoots, func(rr RootRecord) bool {
		return global.IsHealthyCoverageDelta(rr.CoverageDelta, rr.Supply, fraction)
	})
	util.Assertf(len(tipRoots) > 0, "len(tipRoots)>0")
	if len(tipRoots) == 1 {
		// if only one branch is in the latest healthy slot, it is the one reliable
		return util.Ref(FetchBranchDataByRoot(store, tipRoots[0]))
	}

	// there are several healthy roots in the latest healthy slot.
	// we start traversing back from the heaviest one
	util.Assertf(len(tipRoots) > 1, "len(tipRoots)>1")
	rootMaxIdx := util.IndexOfMaximum(tipRoots, func(i, j int) bool {
		return tipRoots[i].CoverageDelta < tipRoots[j].CoverageDelta
	})
	util.Assertf(global.IsHealthyCoverageDelta(tipRoots[rootMaxIdx].CoverageDelta, tipRoots[rootMaxIdx].Supply, fraction),
		"global.IsHealthyCoverageDelta(rootMax.LedgerCoverage, rootMax.Supply, fraction)")

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
	IterateBranchChainBack(store, &chainTip, func(branchID *base.TransactionID, branch *BranchData) bool {
		if first {
			// skip the tip itself
			first = false
			return true
		}
		// check if the branch is included in every reader
		for _, rdr := range readers {
			if !rdr.KnowsCommittedTransaction(*branchID) {
				// the transaction is not known by at least one of selected states,
				// it is not a reliable branch, keep traversing back
				return true
			}
		}
		// branchID is known in all tip states. It is the reliable one
		branchFound = branch
		return false
	})
	return branchFound
}

// FindLatestReliableBranchAndNSlotsBack finds LRB and iterates n slots back along the main chain from LRB.
// It is a precaution if LRB will be orphaned later
func FindLatestReliableBranchAndNSlotsBack(store StateStoreReader, n int, fraction global.Fraction) (ret *BranchData) {
	lrb := FindLatestReliableBranch(store, fraction)
	if lrb == nil {
		return
	}
	IterateBranchChainBack(store, lrb, func(_ *base.TransactionID, branch *BranchData) bool {
		ret = branch
		n--
		return n > 0
	})
	return
}

// GetMainChain returns the chain of branches starting from LRB
func GetMainChain(store StateStoreReader, fraction global.Fraction, max ...int) ([]*BranchData, error) {
	lrb := FindLatestReliableBranch(store, fraction)
	if lrb == nil {
		return nil, fmt.Errorf("can't find latest reliable brancg")
	}
	ret := make([]*BranchData, 0)
	IterateBranchChainBack(store, lrb, func(branchID *base.TransactionID, branch *BranchData) bool {
		ret = append(ret, branch)
		if len(max) > 0 && len(ret) >= max[0] {
			return false
		}
		return true
	})
	return ret, nil
}

// CheckTransactionInLRB return number of slots behind the LRB which contains txid.
// The backwards scan is capped by the maxDepth parameter. If maxDepth == 0, it means only LRB is checked
func CheckTransactionInLRB(store StateStoreReader, txid base.TransactionID, maxDepth int, fraction global.Fraction) (lrb *BranchData, foundAtDepth int) {
	foundAtDepth = -1
	lrb = FindLatestReliableBranch(store, fraction)
	if lrb == nil {
		return
	}

	IterateBranchChainBack(store, lrb, func(branchID *base.TransactionID, branch *BranchData) bool {
		if foundAtDepth >= maxDepth {
			return false
		}
		rdr := MustNewReadable(store, branch.Root, 0)
		if !rdr.KnowsCommittedTransaction(txid) {
			return false
		}
		foundAtDepth++
		return true
	})
	return
}

func (br *BranchData) IsHealthy(fraction global.Fraction) bool {
	return global.IsHealthyCoverageDelta(br.CoverageDelta, br.Supply, fraction)
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
		Add("Stem output id: %s", br.Stem.ID.String()).
		Add("Sequencer output id: %s", br.SequencerOutput.ID.String())
}

func (br *BranchData) LinesShort(prefix ...string) *lines.Lines {
	name := "(no name)"
	if msData := ledger.ParseSeqMilestoneData(br.SequencerOutput.Output); msData != nil {
		name = msData.Name
	}
	return lines.New(prefix...).Add("%s hex=%s (%s) supply: %s, infl: %s, on chain: %s, cov.delta: %s",
		br.Stem.ID.StringShort(),
		br.Stem.ID.StringHex(),
		name,
		util.Th(br.Supply),
		util.Th(br.SlotInflation),
		util.Th(br.SequencerOutput.Output.TokenBalance()),
		util.Th(br.CoverageDelta),
	)
}

// TxID transaction id of the branch, as taken from the stem output id
func (br *BranchData) TxID() base.TransactionID {
	return br.Stem.ID.TransactionID()
}

func (br *BranchData) Slot() base.Slot {
	return br.Stem.ID.Slot()
}

func (br *BranchData) StemPredecessorBranchID() base.TransactionID {
	stemLock, ok := br.Stem.Output.StemLock()
	util.Assertf(ok, "stem lock not found")
	return stemLock.PredecessorOutputID.TransactionID()
}
