package multistate

import (
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

// additional partitions of the k/v store
const (
	// rootRecordDBPartition
	rootRecordDBPartition        = immutable.PartitionOther
	latestSlotDBPartition        = rootRecordDBPartition + 1
	earliestSlotDBPartition      = latestSlotDBPartition + 1
	restoreInProgressDBPartition = earliestSlotDBPartition + 1
	// upgradeLibraryDBPartition stores compiled library JSON blobs keyed by upgrade slot.
	// Key: partition byte + 4-byte slot (big-endian)
	// Value: compiled library JSON bytes
	upgradeLibraryDBPartition = restoreInProgressDBPartition + 1
)

func WriteRootRecord(w common.KVWriter, branchTxID base.TransactionID, rootData RootRecord) {
	common.UseConcatBytes(func(key []byte) {
		w.Set(key, rootData.Bytes())
	}, []byte{rootRecordDBPartition}, branchTxID[:])
}

// DeleteRootRecord removes a branch's RootRecord from the flat KV partition. Called atomically
// with the branch txID trie-prune so the two never diverge (see claude/txid_ttl_tiered.md §2a).
func DeleteRootRecord(w common.KVWriter, branchTxID base.TransactionID) {
	common.UseConcatBytes(func(key []byte) {
		w.Set(key, nil)
	}, []byte{rootRecordDBPartition}, branchTxID[:])
}

func WriteLatestSlotRecord(w common.KVWriter, slot uint32) {
	w.Set([]byte{latestSlotDBPartition}, base.Slot2Bytes(slot))
}

func WriteEarliestSlotRecord(w common.KVWriter, slot uint32) {
	w.Set([]byte{earliestSlotDBPartition}, base.Slot2Bytes(slot))
}

// WriteRestoreInProgressRecord marks the database as having a restore in progress
func WriteRestoreInProgressRecord(w common.KVWriter) {
	w.Set([]byte{restoreInProgressDBPartition}, []byte{1})
}

// DeleteRestoreInProgressRecord removes the restore-in-progress marker
func DeleteRestoreInProgressRecord(w common.KVWriter) {
	w.Set([]byte{restoreInProgressDBPartition}, nil)
}

// IsRestoreInProgress checks if a restore was interrupted (database is corrupted)
func IsRestoreInProgress(store common.KVReader) bool {
	bin := store.Get([]byte{restoreInProgressDBPartition})
	return len(bin) > 0
}

// FetchLatestCommittedSlot fetches the latest recorded slot
func FetchLatestCommittedSlot(store common.KVReader) uint32 {
	bin := store.Get([]byte{latestSlotDBPartition})
	if len(bin) == 0 {
		return 0
	}
	ret, err := base.SlotFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

// FetchEarliestSlot returns the earliest-retained-slot marker: a monotonic LOWER BOUND on the slots
// still held in the multi-state DB — nothing is retained below it. Initialized to 0 for a genesis DB
// and to the snapshot slot for a restored one, then advanced by the branch prune (in the same commit
// batch) as the retained-history floor rises. It is a bound, not a guarantee that the slot itself holds
// a branch: after a prune the marker may point at an empty slot, so callers scan/iterate forward from it.
func FetchEarliestSlot(store common.KVReader) uint32 {
	bin := store.Get([]byte{earliestSlotDBPartition})
	util.Assertf(len(bin) > 0, "internal error: earliest state is not set")
	ret, err := base.SlotFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

// FetchEarliestBranchIDList returns the earliest committed branches whose state is still retained —
// the floor of the node's available history — together with the slot they sit in. Despite the legacy
// "snapshot branch" name this is NOT the branch a snapshot was created from: on a snapshot-restored
// node the restore point is initially the only branch at the earliest slot, but it is pruned like any
// other branch once its slot crosses the branch-record TTL horizon (BranchTxIDTTLSlots), after which
// the floor advances. Because the DAG forks, the earliest retained slot can hold several branches; all
// are returned (sorted by total coverage, heaviest first) rather than guessing a single canonical one —
// consumers test membership / "does any floor branch know tx X" instead of assuming one anchor.
//
// The earliest-slot marker is advanced by the branch prune (atomically, in the same commit batch), but
// this read tolerates a stale marker: it scans forward from the marker to the first slot that still
// holds a root record, so a DB last written by an older binary — whose marker points at a since-pruned
// anchor — still opens instead of asserting.
func FetchEarliestBranchIDList(store common.KVTraversableReader) (uint32, []base.TransactionID) {
	type branchCoverage struct {
		id       base.TransactionID
		coverage uint64
	}
	latestSlot := FetchLatestCommittedSlot(store)
	for slot := FetchEarliestSlot(store); ; slot++ {
		var lst []branchCoverage
		IterateRootRecords(store, func(txid base.TransactionID, rd RootRecord) bool {
			lst = append(lst, branchCoverage{txid, FetchBranchDataByRoot(store, rd).TotalCoverage})
			return true
		}, slot)
		if len(lst) > 0 {
			sort.Slice(lst, func(i, j int) bool { return lst[i].coverage > lst[j].coverage })
			ret := make([]base.TransactionID, len(lst))
			for i := range lst {
				ret[i] = lst[i].id
			}
			return slot, ret
		}
		util.Assertf(slot < latestSlot, "FetchEarliestBranchIDList: no root record between earliest slot %d and latest committed slot %d",
			FetchEarliestSlot(store), latestSlot)
	}
}

const numberOfElementsInRootRecord = 2

func (r *RootRecord) Bytes() []byte {
	arr := tuples.EmptyTupleEditable(numberOfElementsInRootRecord)
	arr.MustPush(r.SequencerID.Bytes()) // 0
	arr.MustPush(r.Root.Bytes())        // 1

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
	return RootRecord{
		Root:        root,
		SequencerID: chainID,
	}, nil
}

func (r *RootRecord) Lines(prefix ...string) *lines.Lines {
	return lines.New(prefix...).
		Add("sequencer id: %s", r.SequencerID.String()).
		Add("root:         %s", r.Root.String())
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

func iterateRootRecordsOfParticularSlots(store common.Traversable, fun func(branchTxID base.TransactionID, rootData RootRecord) bool, slots []uint32) {
	prefix := [5]byte{rootRecordDBPartition, 0, 0, 0, 0}
	for _, s := range slots {
		copy(prefix[1:], base.Slot2Bytes(s))

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
func IterateRootRecords(store common.Traversable, fun func(branchTxID base.TransactionID, rootData RootRecord) bool, optSlot ...uint32) {
	if len(optSlot) == 0 {
		iterateAllRootRecords(store, fun)
		return
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
func FetchAnyLatestRootRecord(store global.StoreReader) RootRecord {
	recs := FetchRootRecords(store, FetchLatestCommittedSlot(store))
	util.Assertf(len(recs) > 0, "FetchAnyLatestRootRecord: can't find any root records in DB")
	return recs[0]
}

// FetchRootRecordsNSlotsBack load root records from N lates slots, present in the store
func FetchRootRecordsNSlotsBack(store global.StoreReader, nBack int) []RootRecord {
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
func FetchRootRecords(store common.Traversable, slots ...uint32) []RootRecord {
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

// FetchLatestRootRecords returns the root records for the latest committed
// slot. Order is not defined — to sort by coverageDelta, promote to
// []*BranchData via FetchLatestBranches and sort there (CoverageDelta now
// lives on the stem, accessible only through BranchData).
func FetchLatestRootRecords(store global.StoreReader) []RootRecord {
	return FetchRootRecords(store, FetchLatestCommittedSlot(store))
}

// FetchBranchData returns branch data by the branch transaction id
func FetchBranchData(store common.KVReader, branchTxID base.TransactionID) (BranchData, bool) {
	if rd, found := FetchRootRecord(store, branchTxID); found {
		return FetchBranchDataByRoot(store, rd), true
	}
	return BranchData{}, false
}

// FetchBranchDataByRoot returns existing branch data by root record. Aggregates
// (Supply, TotalCoverage, CoverageDelta, FrozenCoverage, SlotInflation,
// NumConfirmedTransactions, BaselineRoot) are projected from the branch's stem output —
// they live inside the trie commitment now (see metadata-refactor §5).
func FetchBranchDataByRoot(store common.KVReader, rootData RootRecord) BranchData {
	rdr, err := NewSugaredReadableState(store, rootData.Root, 0)
	util.AssertNoError(err)

	seqOut, err := rdr.GetChainOutputWithID(rootData.SequencerID)
	util.AssertNoError(err)

	stemOut := rdr.GetStemOutput()
	bd := BranchData{
		RootRecord:      rootData,
		Stem:            stemOut,
		SequencerOutput: seqOut,
	}
	if stemLock, ok := stemOut.Output.StemLock(); ok {
		bd.Supply = stemLock.TotalSupply
		bd.TotalCoverage = stemLock.TotalCoverage
		bd.SlotInflation = stemLock.SlotInflation
	}
	// CoverageDelta moved off the stem onto the branch's sequencer milestone
	// output (sequencer constraint). Project it from there.
	if sc, idx := seqOut.Output.SequencerConstraint(); idx != 0xff {
		bd.CoverageDelta = sc.CoverageDelta
	}
	if oracleData, ok := stemOut.Output.OracleData(); ok {
		bd.FrozenCoverage = oracleData.FrozenCoverage
		bd.NumConfirmedTransactions = oracleData.NumConfirmedTransactions
		bd.NumSeqTransactions = oracleData.NumSeqTransactions
		bd.NumSeq = oracleData.NumSeq
		bd.BaselineRoot = oracleData.BaselineRoot
	}
	return bd
}

// FetchBranchDataMulti returns branch records for particular root records
func FetchBranchDataMulti(store global.StoreReader, rootData ...RootRecord) []*BranchData {
	ret := make([]*BranchData, len(rootData))
	for i, rd := range rootData {
		bd := FetchBranchDataByRoot(store, rd)
		ret[i] = &bd
	}
	return ret
}

// FetchLatestBranches branches of the latest slot sorted by coverage descending
func FetchLatestBranches(store global.StoreReader) []*BranchData {
	ret := FetchBranchDataMulti(store, FetchLatestRootRecords(store)...)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].CoverageDelta > ret[j].CoverageDelta
	})
	return ret
}

// FetchLatestBranchTransactionIDs sorted descending by coverage
func FetchLatestBranchTransactionIDs(store global.StoreReader) []base.TransactionID {
	bd := FetchLatestBranches(store)
	ret := make([]base.TransactionID, len(bd))

	for i := range ret {
		ret[i] = bd[i].Stem.ID.TransactionID()
	}
	return ret
}

// FetchHeaviestBranchChainNSlotsBack descending by epoch
func FetchHeaviestBranchChainNSlotsBack(store global.StoreReader, nBack int) []*BranchData {
	rootData := make(map[base.TransactionID]RootRecord)
	latestSlot := FetchLatestCommittedSlot(store)

	if nBack < 0 {
		IterateRootRecords(store, func(branchTxID base.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		})
	} else {
		from := uint32(0)
		if latestSlot > uint32(nBack) {
			from = latestSlot - uint32(nBack)
		}
		IterateRootRecords(store, func(branchTxID base.TransactionID, rd RootRecord) bool {
			rootData[branchTxID] = rd
			return true
		}, util.MakeRange(from, latestSlot)...)
	}

	sortedTxIDs := util.KeysSorted(rootData, func(k1, k2 base.TransactionID) bool {
		// descending by epoch
		return k1.Slot() > k2.Slot()
	})

	// FetchLatestBranches already sorts descending by CoverageDelta — pick the head.
	latestBD := FetchLatestBranches(store)
	util.Assertf(len(latestBD) > 0, "len(latestBD) > 0")
	lastInTheChain := latestBD[0]

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

func FindFirstBranchThat(store global.StoreReader, filter func(branch *BranchData) bool) *BranchData {
	var ret BranchData
	found := false
	IterateSlotsBack(store, func(slot uint32, roots []RootRecord) bool {
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
func FindLatestHealthySlot(store global.StoreReader) (uint32, bool) {
	ret := FindFirstBranchThat(store, func(branch *BranchData) bool {
		return branch.IsHealthy()
	})
	if ret == nil {
		return 0, false
	}
	return ret.Stem.ID.Slot(), true
}

// FirstHealthySlotIsNotBefore determines if first healthy slot is not before tha refSlot.
// Usually refSlot is just few slots back, so the operation does not require
// each time traversing unbounded number of slots
func FirstHealthySlotIsNotBefore(store global.StoreReader, refSlot uint32) (ret bool) {
	IterateSlotsBack(store, func(slot uint32, roots []RootRecord) bool {
		if slot < refSlot {
			return false
		}
		for _, rr := range roots {
			br := FetchBranchDataByRoot(store, rr)
			if ret = br.IsHealthy(); ret {
				return false // found
			}
		}
		return slot > refSlot
	})
	return
}

// IterateSlotsBack iterates descending slots from the latest committed slot down to the earliest available
func IterateSlotsBack(store global.StoreReader, fun func(slot uint32, roots []RootRecord) bool) {
	earliest := FetchEarliestSlot(store)
	slot := FetchLatestCommittedSlot(store)
	for {
		if !fun(slot, FetchRootRecords(store, slot)) || slot == earliest {
			return
		}
		slot--
	}
}

// FindBranchesFromLatestHealthySlot
// Healthy slot is a slot which contains at least one healthy branch.
// Function returns all branches from the latest healthy slot.
// Note that in theory latest healthy slot it may not exist at all, i.e. all slots in the DB
// may not contain any healthy branch. Normally it will exist tho, because:
// - either database contains all branches down to genesis
// - or it was started from snapshot which (normally) represents a healthy state
//
// Aggregates (CoverageDelta, Supply) live on the stem now, so the search has to
// promote each candidate slot's root records to BranchData.
func FindBranchesFromLatestHealthySlot(store global.StoreReader) ([]*BranchData, bool) {
	var found []*BranchData

	IterateSlotsBack(store, func(slot uint32, roots []RootRecord) bool {
		if len(roots) == 0 {
			return true
		}
		bds := FetchBranchDataMulti(store, roots...)
		maxElemIdx := util.IndexOfMaximum(bds, func(i, j int) bool {
			return bds[i].CoverageDelta < bds[j].CoverageDelta
		})
		if bds[maxElemIdx].IsHealthy() {
			found = bds
			return false
		}
		return true
	})
	return found, len(found) > 0
}

// IterateBranchChainBack iterates the past chain of the tip branch (including the tip)
// Stops when the current branch has no predecessor
func IterateBranchChainBack(store global.StoreReader, branch *BranchData, fun func(branchID *base.TransactionID, branch *BranchData) bool) {
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
func FindLatestReliableBranch(store global.StoreReader) *BranchData {
	tips, ok := FindBranchesFromLatestHealthySlot(store)
	if !ok {
		// if the healthy slot does not exist, the reliable branch does not exist either
		return nil
	}
	// filter out not-healthy branches in the healthy slot
	tips = util.PurgeSlice(tips, func(bd *BranchData) bool {
		return bd.IsHealthy()
	})
	util.Assertf(len(tips) > 0, "len(tips)>0")
	if len(tips) == 1 {
		// if only one branch is in the latest healthy slot, it is the one reliable
		return tips[0]
	}

	// several healthy branches in the latest healthy slot — start traversing
	// back from the heaviest one
	rootMaxIdx := util.IndexOfMaximum(tips, func(i, j int) bool {
		return tips[i].CoverageDelta < tips[j].CoverageDelta
	})
	util.Assertf(tips[rootMaxIdx].IsHealthy(), "tips[rootMaxIdx].IsHealthy()")

	// we will be checking if transaction is contained in all tip states.
	// For this we create a collection of state readers (one per non-max tip).
	readers := make([]*Readable, 0, len(tips)-1)
	for i := range tips {
		if !ledger.CommitmentModel.EqualCommitments(tips[i].Root, tips[rootMaxIdx].Root) {
			readers = append(readers, MustNewReadable(store, tips[i].Root))
		}
	}
	util.Assertf(len(readers) > 0, "len(readers) > 0")

	var branchFound *BranchData
	first := true
	IterateBranchChainBack(store, tips[rootMaxIdx], func(branchID *base.TransactionID, branch *BranchData) bool {
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
func FindLatestReliableBranchAndNSlotsBack(store global.StoreReader, n int) (ret *BranchData) {
	lrb := FindLatestReliableBranch(store)
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
func GetMainChain(store global.StoreReader, max ...int) ([]*BranchData, error) {
	lrb := FindLatestReliableBranch(store)
	if lrb == nil {
		return nil, fmt.Errorf("can't find latest reliable branch")
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
//func CheckTransactionInLRB(store global.StoreReader, txid base.TransactionID, maxDepth int, fraction global.Fraction) (lrb *BranchData, foundAtDepth int) {
//	foundAtDepth = -1
//	lrb = FindLatestReliableBranch(store, fraction)
//	if lrb == nil {
//		return
//	}
//
//	IterateBranchChainBack(store, lrb, func(branchID *base.TransactionID, branch *BranchData) bool {
//		if foundAtDepth >= maxDepth {
//			return false
//		}
//		rdr := MustNewReadable(store, branch.Root, 0)
//		if !rdr.KnowsCommittedTransaction(txid) {
//			return false
//		}
//		foundAtDepth++
//		return true
//	})
//	return
//}

// IsHealthy reports whether the branch passes the healthy-branch threshold which applies in
// its own slot (the ledger fraction, or the configured relief fraction inside its window).
func (br *BranchData) IsHealthy() bool {
	return global.IsHealthyBranchAt(br.Stem.ID.Slot(), br.CoverageDelta, br.Supply)
}

// branchAggregateLines renders the human-readable summary of the
// stem-projected aggregates carried on BranchData (post metadata-refactor).
func (br *BranchData) branchAggregateLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	var frozenPct float32
	if br.Supply > 0 {
		frozenPct = (float32(br.FrozenCoverage) * 100) / float32(br.Supply)
	}
	ret.Add("sequencer id:    %s", br.SequencerID.String()).
		Add("supply:          %s", util.Th(br.Supply)).
		Add("coverage delta:  %s", util.Th(br.CoverageDelta)).
		Add("total coverage:  %s", util.Th(br.TotalCoverage)).
		Add("frozen coverage: %s (%.2f%s of supply)", util.Th(br.FrozenCoverage), frozenPct, "%").
		Add("slot inflation:  %s", util.Th(br.SlotInflation)).
		Add("num confirmed transactions: %d", br.NumConfirmedTransactions).
		Add("num sequencer transactions: %d", br.NumSeqTransactions).
		Add("num sequencers:  %d", br.NumSeq).
		Add("healthy(%s):     %v", global.FractionHealthyBranchAt(br.Stem.ID.Slot()).String(), br.IsHealthy())
	return ret
}

func (br *BranchData) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("Sequencer output ID: %s (hex = %s)", br.SequencerOutput.ID.String(), br.SequencerOutput.ID.StringHex()).
		Add("Stem output ID:      %s", br.Stem.ID.String())
	if lck, ok := br.Stem.Output.StemLock(); ok {
		ret.Add("Stem predecessor ID: %s (hex=%s)", lck.PredecessorOutputID.String(), lck.PredecessorOutputID.StringHex())
	}
	return ret.Append(br.branchAggregateLines(prefix...))
}

func (br *BranchData) LinesVerbose(prefix ...string) *lines.Lines {
	ret := br.Lines(prefix...)
	ret.Add("root: %s", br.Root.String()).
		Add("---- Stem ----").
		Append(br.Stem.LinesSource(prefix...)).
		Add("---- Sequencer output ----").
		Append(br.SequencerOutput.LinesSource(prefix...))
	return ret
}

func (br *BranchData) LinesShort(prefix ...string) *lines.Lines {
	name := "(no name)"
	if msData, err := ledger.ParseSequencerData(br.SequencerOutput.Output); err == nil {
		name = msData.Name()
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

func (br *BranchData) Slot() uint32 {
	return br.Stem.ID.Slot()
}

func (br *BranchData) StemPredecessorBranchID() base.TransactionID {
	stemLock, ok := br.Stem.Output.StemLock()
	util.Assertf(ok, "stem lock not found")
	return stemLock.PredecessorOutputID.TransactionID()
}
