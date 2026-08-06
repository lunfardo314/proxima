package multistate

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/set256"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	// Updatable is an updatable ledger state, with the particular root
	// Suitable for chained updates
	// Not-thread safe, should be used individual instance for each parallel update.
	// DB (store) is updated atomically with all mutations in one DB transaction
	Updatable struct {
		trie  *immutable.TrieUpdatable
		store global.Store
	}

	// Readable is a read-only ledger state, with the particular root.
	// The trie reader mutates its internal cache on every read, so trie access requires
	// an exclusive lock (mutex.Lock). However, the L2 caches (txCache, utxoCache) allow
	// concurrent reads (mutex.RLock) for cached entries, avoiding trie contention on hot
	// paths (KnowsCommittedTransaction, HasUTXO, GetUTXO).
	Readable struct {
		mutex     sync.RWMutex
		trie      *immutable.TrieReader
		txCache   map[base.TransactionID]txCacheEntry
		utxoCache map[base.OutputID]utxoCacheEntry
	}

	// txCacheEntry is an L2 cache entry for a txID record in the trie.
	// exists == false means the txID is not in the state.
	txCacheEntry struct {
		exists  bool
		unspent set256.Set256
	}

	// utxoCacheEntry is an L2 cache entry for a UTXO byte payload in the trie.
	// found == false means the OID is not in the state (rare: passes the txCache
	// presence check but the partition lookup misses).
	utxoCacheEntry struct {
		data  []byte
		found bool
	}

	// RootRecord is the persistent per-branch DB record. After the
	// metadata-refactor (see claude/metadata-refactor.md §5), it carries only
	// the trie root and the sequencer ChainID — every other deterministic
	// aggregate (Supply, CoverageDelta, FrozenCoverage, SlotInflation,
	// NumConfirmedTransactions, TotalCoverage, BaselineRoot) lives inside the stem
	// output's stemLock constraint and is part of the trie commitment.
	RootRecord struct {
		Root        common.VCommitment
		SequencerID base.ChainID
	}

	// BranchData is the in-memory convenience struct exposed to the rest of
	// the codebase. The aggregates below are projected from the branch's stem
	// output (parsed via Stem.Output.StemLock()) at construction time inside
	// FetchBranchDataByRoot, so callers like br.Supply / br.CoverageDelta
	// keep working without churn. CoverageDelta is the exception: it is
	// projected from the SequencerOutput's sequencer constraint.
	BranchData struct {
		RootRecord                       // Root, SequencerID (from DB)
		Stem            *ledger.OutputWithID
		SequencerOutput *ledger.OutputWithID
		// Projected from Stem.Output.StemLock() / Stem.Output.OracleData() at
		// construction time (CoverageDelta from SequencerOutput's sequencer
		// constraint).
		Supply          uint64
		TotalCoverage   uint64
		CoverageDelta   uint64
		FrozenCoverage  uint64
		SlotInflation   uint64
		NumConfirmedTransactions uint32
		// NumSeqTransactions / NumSeq are deterministic consensus stats projected
		// from Stem.Output.OracleData() (output index 3). NumSeqTransactions is the
		// new sequencer-tx count in the branch's slot; NumSeq is the number of
		// distinct sequencers active in that slot.
		NumSeqTransactions uint32
		NumSeq             uint32
		// 24-byte trie root of the predecessor branch (per metadata-refactor §3).
		// nil at genesis. Held as raw bytes — callers that need a VCommitment
		// reconstitute it via ledger.CommitmentModel.NewVectorCommitment().
		BaselineRoot []byte
	}
)

// partitions of the state store on the trie
// Ledger state contains records of UTXOs (keys 33 bytes long output IDs ) and all past transaction IDs (32 byte long keys)
// reason why we put index entries (accounts, chain ChainID) into the trie is because index is ledger state-specific
//
// NOTE: transaction IDs (32 byte long) and UTXO IDs (33 byte long) are on the same partition (1-byte prefix) TriePartitionLedgerState,
// i.e. txs and utxos are distinguished by size of their keys. This is significant optimization of the trie, because txid and tx outputs
// have the same 32 byte long prefix

const (
	TriePartitionLedgerState = byte(iota)
	TriePartitionControllers
	TriePartitionChainID
)

func PartitionToString(p byte) string {
	switch p {
	case TriePartitionLedgerState:
		return "UTXO"
	case TriePartitionControllers:
		return "ACCN"
	case TriePartitionChainID:
		return "CHID"
	default:
		return "????"
	}
}

func LedgerIdentityBytesFromStore(store global.Store) []byte {
	rr := FetchAnyLatestRootRecord(store)
	return LedgerIdentityBytesFromRoot(store, rr.Root)
}

func LedgerIdentityBytesFromRoot(store global.StoreReader, root common.VCommitment) []byte {
	trie, err := immutable.NewTrieReader(ledger.CommitmentModel, store, root, 0)
	util.AssertNoError(err)
	return trie.Get(nil)
}

// NewReadable creates read-only ledger state with the given root
func NewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) (*Readable, error) {
	trie, err := immutable.NewTrieReader(ledger.CommitmentModel, store, root, clearCacheAtSize...)
	if err != nil {
		return nil, err
	}
	return &Readable{
		trie:      trie,
		txCache:   make(map[base.TransactionID]txCacheEntry),
		utxoCache: make(map[base.OutputID]utxoCacheEntry),
	}, nil
}

func MustNewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) *Readable {
	ret, err := NewReadable(store, root, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

// NewUpdatable creates updatable state with the given root. After updated, the root changes.
// Suitable for chained updates of the ledger state
func NewUpdatable(store global.Store, root common.VCommitment) (*Updatable, error) {
	trie, err := immutable.NewTrieUpdatable(ledger.CommitmentModel, store, root)
	if err != nil {
		return nil, err
	}
	return &Updatable{
		trie:  trie,
		store: store,
	}, nil
}

func MustNewUpdatable(store global.Store, root common.VCommitment) *Updatable {
	ret, err := NewUpdatable(store, root)
	util.AssertNoError(err)
	return ret
}

// _lookupTxRecord returns the cached txID record, populating the cache from the trie on miss.
// Uses RLock for cache hits (concurrent), Lock only for misses (trie access).
func (r *Readable) _lookupTxRecord(txid base.TransactionID) txCacheEntry {
	// Fast path: RLock for cache hit
	r.mutex.RLock()
	if entry, ok := r.txCache[txid]; ok {
		r.mutex.RUnlock()
		return entry
	}
	r.mutex.RUnlock()

	// Slow path: exclusive lock for trie read + cache write
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Double-check after acquiring write lock
	if entry, ok := r.txCache[txid]; ok {
		return entry
	}
	return r._readAndCacheTxRecord(txid)
}

// _readAndCacheTxRecord reads a txID record from the trie and stores it in the L2 cache.
// Caller must hold exclusive lock (mutex.Lock).
func (r *Readable) _readAndCacheTxRecord(txid base.TransactionID) txCacheEntry {
	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	v := partition.Get(txid[:])
	entry := txCacheEntry{exists: len(v) > 0}
	if entry.exists {
		entry.unspent = set256.NewFromSlice(v)
	}
	r.txCache[txid] = entry
	return entry
}

func (r *Readable) GetUTXO(oid base.OutputID) ([]byte, bool) {
	// Synthetic upgrade UTXOs have no txID record — skip Set256 check
	if !base.IsUpgradeOutputID(oid) {
		entry := r._lookupTxRecord(oid.TransactionID())
		if !entry.exists || !entry.unspent.Contains(oid.Index()) {
			return nil, false
		}
	}

	// Fast path: RLock for cache hit
	r.mutex.RLock()
	if e, ok := r.utxoCache[oid]; ok {
		r.mutex.RUnlock()
		return e.data, e.found
	}
	r.mutex.RUnlock()

	// Slow path: exclusive lock for trie read + cache write
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Double-check after acquiring write lock
	if e, ok := r.utxoCache[oid]; ok {
		return e.data, e.found
	}

	data, found := r._getUTXO(oid)
	r.utxoCache[oid] = utxoCacheEntry{data: data, found: found}
	return data, found
}

func (r *Readable) _getUTXO(oid base.OutputID, partition ...*common.ReaderPartition) ([]byte, bool) {
	var part *common.ReaderPartition
	if len(partition) > 0 {
		part = partition[0]
	} else {
		part = common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
		defer part.Dispose()
	}

	ret := part.Get(oid[:])
	if len(ret) == 0 {
		return nil, false
	}

	return ret, true
}

func (r *Readable) HasUTXO(oid base.OutputID) bool {
	// Synthetic upgrade UTXOs have no txID record — fall back to 33-byte key lookup
	if base.IsUpgradeOutputID(oid) {
		r.mutex.Lock()
		defer r.mutex.Unlock()

		partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
		defer partition.Dispose()

		return partition.Has(oid[:])
	}
	entry := r._lookupTxRecord(oid.TransactionID())
	return entry.exists && entry.unspent.Contains(oid.Index())
}

func (r *Readable) KnowsCommittedTransaction(txid base.TransactionID) bool {
	return r._lookupTxRecord(txid).exists
}

func (r *Readable) GetUTXOIDsForController(addr ledger.ControllerID) ([]base.OutputID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(addr) > 255 {
		return nil, fmt.Errorf("accountID length should be <= 255")
	}
	ret := make([]base.OutputID, 0)
	var oid base.OutputID
	var err error

	accountPrefix := common.Concat(TriePartitionControllers, byte(len(addr)), addr)
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		oid, err = base.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		ret = append(ret, oid)
		return true
	})

	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *Readable) GetUTXOsForController(addr ledger.ControllerID) ([]*ledger.OutputDataWithID, error) {
	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	ret := make([]*ledger.OutputDataWithID, 0)
	err := r.IterateUTXOsForController(addr, func(oid base.OutputID, odata []byte) bool {
		ret = append(ret, &ledger.OutputDataWithID{
			ID:   oid,
			Data: odata,
		})
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *Readable) IterateUTXOsForController(controllerID ledger.ControllerID, fun func(oid base.OutputID, odata []byte) bool) (err error) {
	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	return r.IterateUTXOIDsForController(controllerID, func(oid base.OutputID) bool {
		if odata, found := r._getUTXO(oid, partition); found {
			return fun(oid, odata)
		}
		return true
	})
}

func (r *Readable) IsKnownController(addr ledger.ControllerID) (ret bool) {
	err := r.IterateUTXOsForController(addr, func(oid base.OutputID, odata []byte) bool {
		ret = true
		return false
	})
	util.AssertNoError(err)
	return
}

func (r *Readable) IterateUTXOIDsForController(controller ledger.ControllerID, fun func(oid base.OutputID) bool) (err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(controller) > 255 {
		return fmt.Errorf("controllerID length should be <= 255")
	}
	accountPrefix := common.Concat(TriePartitionControllers, byte(len(controller)), controller)

	var oid base.OutputID

	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		oid, err = base.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		return fun(oid)
	})
	return err
}

func (r *Readable) GetUTXOForChainID(id base.ChainID) (*ledger.OutputDataWithID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r._getUTXOForChainID(id)
}

func (r *Readable) _getUTXOForChainID(id base.ChainID) (*ledger.OutputDataWithID, error) {
	chainPartition := common.MakeReaderPartition(r.trie, TriePartitionChainID)
	outID := chainPartition.Get(id[:])
	defer chainPartition.Dispose()

	if len(outID) == 0 {
		return nil, ErrNotFound
	}
	oid, err := base.OutputIDFromBytes(outID)
	if err != nil {
		return nil, err
	}
	outData, found := r._getUTXO(oid)

	if !found {
		return nil, ErrNotFound
	}
	return &ledger.OutputDataWithID{
		ID:   oid,
		Data: outData,
	}, nil
}

func (r *Readable) GetStem() (uint32, []byte) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	accountPrefix := common.Concat(TriePartitionControllers, byte(len(ledger.StemAccountID)), ledger.StemAccountID)

	var found bool
	var retSlot uint32
	var retBytes []byte

	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	// we iterate one element. Stem output ust always be present in the state
	count := 0
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		util.Assertf(count == 0, "inconsistency: must be exactly 1 index record for stem output")
		count++
		oid, err := base.OutputIDFromBytes(k[len(accountPrefix):])
		util.AssertNoError(err)
		retSlot = oid.Slot()
		retBytes, found = r._getUTXO(oid, partition)
		util.Assertf(found, "can't find stem output")
		return true
	})
	return retSlot, retBytes
}

func (r *Readable) MustLedgerIdentityBytes() []byte {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r.trie.Get(nil)
}

func (r *Readable) Iterator(prefix []byte) common.KVIterator {
	return r.trie.Iterator(prefix)
}

// IterateKnownCommittedTransactions iterates transaction IDs in the state. Optionally, iteration is restricted
// for a slot. In that case first iterates non-sequencer transactions, the sequencer transactions.
func (r *Readable) IterateKnownCommittedTransactions(fun func(txid base.TransactionID) bool, txidSlot ...uint32) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	keyPrefix := []byte{TriePartitionLedgerState}
	if len(txidSlot) > 0 {
		keyPrefix = append(keyPrefix, base.Slot2Bytes(txidSlot[0])...)
	}
	r.trie.Iterator(keyPrefix).Iterate(func(k, v []byte) bool {
		d := k[1:]
		if len(d) != base.TransactionIDLength {
			return true
		}
		txid := base.MustTransactionIDFromBytes(d)
		return fun(txid)
	})
}

func (r *Readable) KnownCommittedTxIDs(slot uint32) []base.TransactionID {
	ret := make([]base.TransactionID, 0)
	r.IterateKnownCommittedTransactions(func(txid base.TransactionID) bool {
		ret = append(ret, txid)
		return true
	}, slot)
	return ret
}

// PrunableTxIDsAtSlot returns txIDs at the given slot whose unspent output set is empty,
// meaning all outputs have been consumed and the txID record can be safely pruned.
// branch selects the kind: false → non-branch txIDs only, true → branch txIDs only.
// The two kinds have different retention horizons (see claude/txid_ttl_tiered.md).
func (r *Readable) PrunableTxIDsAtSlot(slot uint32, branch bool) []base.TransactionID {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	ret := make([]base.TransactionID, 0)
	keyPrefix := append([]byte{TriePartitionLedgerState}, base.Slot2Bytes(slot)...)
	r.trie.Iterator(keyPrefix).Iterate(func(k, v []byte) bool {
		d := k[1:]
		if len(d) != base.TransactionIDLength {
			return true
		}
		txid := base.MustTransactionIDFromBytes(d)
		if txid.IsBranchTransaction() != branch {
			return true
		}
		s := set256.NewFromSlice(v)
		if s.IsEmpty() {
			ret = append(ret, txid)
		}
		return true
	})
	return ret
}

// GetTxUnspentOutputSet returns the Set256 of unspent output indices for the given txID.
// Returns the set and true if the txID record exists, empty set and false otherwise.
func (r *Readable) GetTxUnspentOutputSet(txid base.TransactionID) (set256.Set256, bool) {
	entry := r._lookupTxRecord(txid)
	if !entry.exists {
		return set256.Set256{}, false
	}
	return entry.unspent, true
}

func (r *Readable) IterateChainTips(fun func(chainID base.ChainID, oid base.OutputID) bool) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var chainID base.ChainID
	var oid base.OutputID
	var err error
	r.trie.Iterator([]byte{TriePartitionChainID}).Iterate(func(k []byte, v []byte) bool {
		chainID, err = base.ChainIDFromBytes(k[1:])
		if err != nil {
			return false
		}
		oid, err = base.OutputIDFromBytes(v)
		if err != nil {
			return false
		}
		return fun(chainID, oid)
	})
	return err
}

func (r *Readable) Root() common.VCommitment {
	// non need to lock
	return r.trie.Root()
}

// IterateUTXOIDs scans UTXO IDs in the index. Ensures one call per UTXO
func (r *Readable) IterateUTXOIDs(fun func(oid base.OutputID) bool) (err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	oidSet := set.New[base.OutputID]()

	var oid base.OutputID

	r.trie.Iterator([]byte{TriePartitionControllers}).IterateKeys(func(k []byte) bool {
		if oid, err = base.OutputIDFromBytes(k[2+k[1]:]); err != nil {
			return false
		}
		if oidSet.Contains(oid) {
			return true
		}
		oidSet.Insert(oid)
		return fun(oid)
	})
	return
}

func (r *Readable) IterateUTXOs(fun func(o ledger.OutputWithID) bool) (err error) {
	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	var o *ledger.Output

	return r.IterateUTXOIDs(func(oid base.OutputID) bool {
		oData, ok := r._getUTXO(oid, partition)
		util.Assertf(ok, "IterateUTXOs: can't find UTXO %s", oid.String())
		// Use output's slot for parsing (output was created at oid.Slot())
		o, err = ledger.OutputFromBytes(oData)
		util.AssertNoError(err, "IterateUTXOs")

		return fun(ledger.OutputWithID{
			Output: o,
			ID:     oid,
		})
	})
}

// SlotChunkBits is how many low bits of the slot are dropped to form a slot
// chunk: the trie key is partition || slot(4 BE) || …, so a 3-byte prefix
// pins the slot's high 24 bits and covers 256 consecutive slots.
const SlotChunkBits = 8

// SlotChunk returns the chunk a slot belongs to. Chunks descend with age, so
// scanning old state means walking chunk indices down from SlotChunk(now).
func SlotChunk(slot uint32) uint32 { return slot >> SlotChunkBits }

// IterateUTXOsInSlotChunk scans every UTXO whose slot falls in the given chunk
// (256 consecutive slots) in one trie traversal. Scanning slot by slot costs a
// traversal per slot, most of them over long-empty prefixes; the 3-byte prefix
// amortises that 256:1.
//
// Iteration stops as soon as fun returns false, so a caller collecting a fixed
// number of outputs pays only for what it reads, not for the whole chunk.
func (r *Readable) IterateUTXOsInSlotChunk(chunk uint32, fun func(oid base.OutputID, oData []byte) bool) (err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	prefix := common.Concat(TriePartitionLedgerState,
		[]byte{byte(chunk >> 16), byte(chunk >> 8), byte(chunk)})

	var oid base.OutputID
	r.trie.Iterator(prefix).Iterate(func(k, v []byte) bool {
		// The partition also holds bare 32-byte transaction IDs; only the
		// 33-byte UTXO IDs are outputs.
		d := k[1:]
		if len(d) != base.OutputIDLength {
			return true
		}
		if oid, err = base.OutputIDFromBytes(d); err != nil {
			return false
		}
		return fun(oid, v)
	})
	return err
}

func (r *Readable) IterateUTXOsInSlot(slot uint32, fun func(oid base.OutputID, oData []byte) bool) (err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	prefix := common.Concat(TriePartitionLedgerState, base.Slot2Bytes(slot))

	var oid base.OutputID
	r.trie.Iterator(prefix).Iterate(func(k, v []byte) bool {
		d := k[1:]
		if len(d) != base.OutputIDLength {
			return true
		}
		if oid, err = base.OutputIDFromBytes(d); err != nil {
			return false
		}
		return fun(oid, v)
	})
	return err
}

func (u *Updatable) Readable() *Readable {
	return &Readable{
		trie:      u.trie.TrieReader,
		txCache:   make(map[base.TransactionID]txCacheEntry),
		utxoCache: make(map[base.OutputID]utxoCacheEntry),
	}
}

func (u *Updatable) Root() common.VCommitment {
	return u.trie.Root()
}

type RootRecordParams struct {
	StemOutputID base.OutputID
	SeqID        base.ChainID
	// SlotInflation is used only for the input/output amount invariant inside
	// updateTrie (consumed + inflation == produced). It is NOT persisted; the
	// authoritative value lives on the produced stem output.
	SlotInflation     uint64
	WriteEarliestSlot bool
	// AdvanceEarliestSlotTo, when non-zero, moves the earliest-slot marker forward to this slot in
	// the same batch that prunes the branch records below it — keeping the marker consistent with the
	// retained-history floor. Written only when it advances the marker (monotonic guard below).
	AdvanceEarliestSlotTo uint32
}

// Update updates trie with mutations
// If par.GenesisStemOutputID != nil, also writes root partition record
func (u *Updatable) Update(muts *Mutations, rootRecordParams *RootRecordParams) error {
	var slotInflation []uint64
	if rootRecordParams != nil {
		slotInflation = []uint64{rootRecordParams.SlotInflation}
	}

	err := u.updateUTXOLedgerDB(func(trie *immutable.TrieUpdatable) error {
		return updateTrie(u.trie, muts, slotInflation...)
	}, rootRecordParams, muts.DeleteBranchRootRecordIDs())
	if err != nil {
		err = fmt.Errorf("%w\n-------- mutations --------\n%s", err, muts.Lines("    ").String())
	}
	return err
}

func (u *Updatable) MustUpdate(muts *Mutations, par *RootRecordParams) {
	err := u.Update(muts, par)
	util.AssertNoError(err)
}

func (u *Updatable) updateUTXOLedgerDB(updateFun func(updatable *immutable.TrieUpdatable) error, rootRecordsParams *RootRecordParams, deleteBranchRootRecords []base.TransactionID) error {
	if err := updateFun(u.trie); err != nil {
		return err
	}
	batch := u.store.BatchedWriter()
	newRoot := u.trie.Commit(batch)
	// Drop RootRecords of pruned branches in the SAME batch as the trie prune, so the trie txID
	// record and the flat-KV RootRecord never diverge (see claude/txid_ttl_tiered.md §2a).
	for i := range deleteBranchRootRecords {
		DeleteRootRecord(batch, deleteBranchRootRecords[i])
	}
	if rootRecordsParams != nil {
		latestSlot := FetchLatestCommittedSlot(u.store)
		if latestSlot < rootRecordsParams.StemOutputID.Slot() {
			WriteLatestSlotRecord(batch, rootRecordsParams.StemOutputID.Slot())
		}
		if rootRecordsParams.WriteEarliestSlot {
			WriteEarliestSlotRecord(batch, rootRecordsParams.StemOutputID.Slot())
		}
		if rootRecordsParams.AdvanceEarliestSlotTo > 0 && rootRecordsParams.AdvanceEarliestSlotTo > FetchEarliestSlot(u.store) {
			// atomic with the branch-record prune in this batch; monotonic (never regresses). Guarded
			// by >0 so the genesis-init batch (which sets the marker for the first time) is skipped.
			WriteEarliestSlotRecord(batch, rootRecordsParams.AdvanceEarliestSlotTo)
		}
		branchID := rootRecordsParams.StemOutputID.TransactionID()
		WriteRootRecord(batch, branchID, RootRecord{
			Root:        newRoot,
			SequencerID: rootRecordsParams.SeqID,
		})
	}
	var err error
	if err = batch.Commit(); err != nil {
		return err
	}
	if u.trie, err = immutable.NewTrieUpdatable(ledger.CommitmentModel, u.store, newRoot); err != nil {
		return err
	}
	return nil
}
