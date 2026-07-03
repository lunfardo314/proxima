// Package branches implements caching of branch data
package branches

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		NotifyBranchCommitted(branchSlot uint32)
		// RequestPrune signals the memDAG to run LRB-depth pruning on the next tick.
		RequestPrune()
	}

	cachedBranchData struct {
		*multistate.BranchData
		lastActive time.Time
	}

	Branches struct {
		environment
		mutex sync.Mutex
		// earliest retained state (floor of the node's available history). Because the DAG forks, the
		// earliest retained slot can hold several branches; all are kept (heaviest coverage first). Not
		// "the snapshot branch": the snapshot anchor is pruned once it crosses the branch-record TTL, and
		// the floor advances. Maintained under mutex: refreshed from the store whenever a commit's prune
		// advances the earliest-slot marker. earliestBranches is empty only transiently before first init.
		earliestSlot     uint32
		earliestBranches []base.TransactionID
		m                map[base.TransactionID]cachedBranchData

		// Cache of state readers. Single state (trie) reader for the branch/root. When accessed through the cache,
		// reading is highly optimized because each state reader keeps its trie cache, so consequent calls to
		// HasUTXO, GetUTXO and similar do not require database involvement during attachment and solidification
		// in the same slot. Inactive cached readers with their trie caches are constantly cleaned up
		stateReaders map[base.TransactionID]*cachedStateReader

		// pending holds deferred branch commits. The actual DB write is deferred until
		// the branch state is requested via GetStateReaderForTheBranch().
		// Orphan branches that are never requested are discarded during cleanup.
		pending map[base.TransactionID]*PendingBranchCommit

		// committing tracks branches currently being committed outside the mutex.
		// When a pending branch commit begins, a closed-when-done channel is stored here.
		// Other goroutines that need the same branch wait on that channel instead of
		// duplicating the commit work. Entries are removed once the commit completes.
		committing map[base.TransactionID]chan struct{}
	}

	cachedStateReader struct {
		multistate.IndexedStateReader
		lastActivity time.Time
	}

	// PendingBranchCommit holds data needed to lazily commit a branch to DB.
	// The actual DB write is deferred until the branch state is requested via GetStateReaderForTheBranch().
	PendingBranchCommit struct {
		Mutations        *multistate.Mutations
		RootRecParams    *multistate.RootRecordParams
		BaselineBranchID   base.TransactionID
		PreviousBranchID   base.TransactionID // stem link to previous branch (for mutation chain traversal)
		TxIDTTLSlots       uint32
		BranchTxIDTTLSlots uint32
		CommittedTxs       []base.TransactionID
		SequencerName    string
		// Stem aggregates carried for the in-memory BranchData cache (so callers
		// see the same values they will see after commit). These are also on the
		// produced stem output — kept here to avoid parsing the stem on hot paths.
		Supply          uint64
		TotalCoverage   uint64
		CoverageDelta   uint64
		FrozenCoverage  uint64
		SlotInflation   uint64
		NumConfirmedTransactions uint32
		NumSeqTransactions       uint32
		NumSeq                   uint32
		BaselineRoot    []byte
	}
)

const (
	stateReaderTTLSlots     = 2
	branchDataCacheTTLSlots = 12
	stateReaderCacheLimit   = 3000
	stateReaderCacheMaxSize = 100 // hard cap on cached state readers; evict oldest when exceeded
)

func New(env environment) *Branches {
	earliestSlot, earliestBranches := multistate.FetchEarliestBranchIDList(env.StateStore())
	ret := &Branches{
		environment:      env,
		earliestSlot:     earliestSlot,
		earliestBranches: earliestBranches,
		m:                make(map[base.TransactionID]cachedBranchData),
		stateReaders:     make(map[base.TransactionID]*cachedStateReader),
		pending:          make(map[base.TransactionID]*PendingBranchCommit),
		committing:       make(map[base.TransactionID]chan struct{}),
	}
	env.RepeatInBackground("branches_cleanup", 5*time.Second, func() bool {
		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		ret._cleanupCachedStateReaders()
		ret._cleanupBranches()

		return true
	}, true)
	return ret
}

// IsPending reports whether the given branch ID is currently held in b.pending
// (i.e. its state is not yet committed to the trie).
// Diagnostic helper for the 2026-04-23 consensus-halt investigation.
func (b *Branches) IsPending(branchID base.TransactionID) bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	_, ok := b.pending[branchID]
	return ok
}

// GetRootHex returns the committed root of the branch as hex, or "" if not
// committed / not known. Diagnostic helper.
func (b *Branches) GetRootHex(branchID base.TransactionID) string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	bd, ok := b.m[branchID]
	if !ok || bd.Root == nil {
		return ""
	}
	return hex.EncodeToString(bd.Root.Bytes())
}

func (b *Branches) Get(branchTxID base.TransactionID) *multistate.BranchData {
	util.Assertf(branchTxID.IsBranchTransaction(), "branch transaction ChainID expected. Got %s", branchTxID.StringShort)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	if ret, ok := b._getAndCacheNoLock(branchTxID); ok {
		return ret.BranchData
	}
	return nil
}

// EarliestSlot returns the floor of the node's retained history — the earliest-retained-slot lower
// bound. Branches at or above it may exist; anything strictly below has been pruned.
func (b *Branches) EarliestSlot() uint32 {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.earliestSlot
}

// EarliestBranchIDs returns the branches at the earliest retained slot (heaviest coverage first). The
// floor is a set, not a single branch: the earliest retained slot can hold several forked branches.
func (b *Branches) EarliestBranchIDs() []base.TransactionID {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.earliestBranches
}

// _refreshEarliestNoLock re-reads the retained-history floor from the store. Called after a commit
// whose prune advanced the earliest-slot marker. Cheap: one marker read plus the few root records at
// the floor slot.
func (b *Branches) _refreshEarliestNoLock() {
	if multistate.FetchEarliestSlot(b.StateStore()) <= b.earliestSlot {
		return
	}
	b.earliestSlot, b.earliestBranches = multistate.FetchEarliestBranchIDList(b.StateStore())
}

// EarliestStateKnowsTransaction reports whether any branch of the retained-history floor already
// contains txid (committed at or before the floor). Returns that branch — usable as txid's baseline.
func (b *Branches) EarliestStateKnowsTransaction(txid base.TransactionID) (base.TransactionID, bool) {
	for _, floorBranchID := range b.EarliestBranchIDs() {
		if b.BranchKnowsTransaction(floorBranchID, txid) {
			return floorBranchID, true
		}
	}
	return base.TransactionID{}, false
}

func (b *Branches) _getAndCacheNoLock(branchID base.TransactionID) (cachedBranchData, bool) {
	bd, ok := b.m[branchID]
	if ok {
		bd.lastActive = time.Now()
		b.m[branchID] = bd
		return bd, true
	}

	if branchID.Slot() < b.earliestSlot ||
		(branchID.Slot() == b.earliestSlot && !slices.Contains(b.earliestBranches, branchID)) {
		// below the retained-history floor, or a non-retained fork at the floor slot — pruned/impossible
		return cachedBranchData{}, false
	}

	// fetch branch from the database
	if rd, found := multistate.FetchRootRecord(b.StateStore(), branchID); found {
		bdRec := multistate.FetchBranchDataByRoot(b.StateStore(), rd)
		bd = cachedBranchData{
			BranchData: &bdRec,
			lastActive: time.Now(),
		}
		b.m[branchID] = bd
		return bd, true
	}
	return cachedBranchData{}, false
}

// LedgerCoverage returns the total ledger coverage of the branch — read
// directly from the on-chain stemLock TotalCoverage field (post metadata-
// refactor §6/§9.6). The off-chain 64-slot halving traversal is gone; the
// recurrence enforced inside the stemLock constraint is the single source.
func (b *Branches) LedgerCoverage(branchID base.TransactionID) uint64 {
	util.Assertf(branchID.IsBranchTransaction(), "branch transaction ChainID expected. Got %s", branchID.StringShort)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	bd, ok := b._getAndCacheNoLock(branchID)
	if !ok {
		return 0
	}
	return bd.TotalCoverage
}

func (b *Branches) Supply(branchID base.TransactionID) uint64 {
	util.Assertf(branchID.IsBranchTransaction(), "branch transaction ChainID expected. Got %s", branchID.StringShort)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	if bd, ok := b._getAndCacheNoLock(branchID); ok {
		return bd.Supply
	}
	return 0
}

// FrozenCoverage returns the total frozen-by-delegation tokens recorded on the
// branch (the accumulated state invariant the next branch builds on).
func (b *Branches) FrozenCoverage(branchID base.TransactionID) uint64 {
	util.Assertf(branchID.IsBranchTransaction(), "branch transaction ChainID expected. Got %s", branchID.StringShort)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	if bd, ok := b._getAndCacheNoLock(branchID); ok {
		return bd.FrozenCoverage
	}
	return 0
}

func (b *Branches) _cleanupCachedStateReaders() (int, int) {
	// Check if ledger has been reset (during test cleanup) to avoid nil pointer dereference
	if ledger.IsReset() {
		return 0, len(b.stateReaders)
	}
	ttl := stateReaderTTLSlots * ledger.SlotDuration()
	count := 0

	for txid, br := range b.stateReaders {
		if time.Since(br.lastActivity) > ttl {
			delete(b.stateReaders, txid)
			count++
		}
	}
	// hard cap: if cache still exceeds limit, evict the oldest entries
	for len(b.stateReaders) > stateReaderCacheMaxSize {
		var oldestID base.TransactionID
		var oldestTime time.Time
		for txid, br := range b.stateReaders {
			if oldestTime.IsZero() || br.lastActivity.Before(oldestTime) {
				oldestID = txid
				oldestTime = br.lastActivity
			}
		}
		delete(b.stateReaders, oldestID)
		count++
	}
	return count, len(b.stateReaders)
}

func (b *Branches) _cleanupBranches() (int, int) {
	// Check if ledger has been reset (during test cleanup) to avoid nil pointer dereference
	if ledger.IsReset() {
		return 0, len(b.m)
	}
	ttl := branchDataCacheTTLSlots * ledger.SlotDuration()
	count := 0

	for txid, br := range b.m {
		if time.Since(br.lastActive) > ttl {
			// if pending, discard the uncommitted state
			if pb, isPending := b.pending[txid]; isPending {
				delete(b.pending, txid)
				b.LogTopicf("branch_commit", 1, "orphaned branch %s (%s, %s), discarding uncommitted state",
					txid.StringShort(), pb.SequencerName, pb.RootRecParams.SeqID.StringShort())
			}
			delete(b.m, txid)
			count++
		}
	}
	return count, len(b.m)
}

// AddPendingBranch stores a deferred branch commit. The branch data is cached in b.m (with nil Root)
// so that coverage, supply, and other non-trie lookups work immediately.
// The actual DB commit is deferred until GetStateReaderForTheBranch() is called.
func (b *Branches) AddPendingBranch(branchID base.TransactionID, pb *PendingBranchCommit,
	stemOutput, sequencerOutput *ledger.OutputWithID) {

	b.mutex.Lock()
	defer b.mutex.Unlock()

	// build BranchData with nil Root for immediate use by coverage/supply lookups.
	// Stem-projected aggregates come from the PendingBranchCommit so callers see
	// the same values they will see after commit.
	bd := cachedBranchData{
		BranchData: &multistate.BranchData{
			RootRecord: multistate.RootRecord{
				// Root is nil — will be set when committed
				SequencerID: pb.RootRecParams.SeqID,
			},
			Stem:            stemOutput,
			SequencerOutput: sequencerOutput,
			Supply:          pb.Supply,
			TotalCoverage:   pb.TotalCoverage,
			CoverageDelta:   pb.CoverageDelta,
			FrozenCoverage:  pb.FrozenCoverage,
			SlotInflation:   pb.SlotInflation,
			NumConfirmedTransactions: pb.NumConfirmedTransactions,
			NumSeqTransactions: pb.NumSeqTransactions,
			NumSeq:             pb.NumSeq,
			BaselineRoot:    pb.BaselineRoot,
		},
		lastActive: time.Now(),
	}

	b.m[branchID] = bd
	b.pending[branchID] = pb
}

// _commitPendingBranchUnlocked performs the actual DB commit for a deferred branch.
// Called WITHOUT b.mutex held to avoid blocking all branches.mutex users during the
// expensive trie iteration (PrunableTxIDsAtSlot) and DB commit.
// The caller must extract pb and baselineRoot under the lock before calling this method,
// and must update b.m / b.pending / b.committing under the lock after it returns.
func (b *Branches) _commitPendingBranchUnlocked(branchID base.TransactionID, pb *PendingBranchCommit, baselineRoot common.VCommitment) *multistate.Updatable {
	// create updatable state from baseline root
	upd := multistate.MustNewUpdatable(b.StateStore(), baselineRoot)

	// pb.Mutations must stay immutable after AddPendingBranch: it is read without b.mutex
	// by virtualStateReader and under b.mutex by branchKnowsTransactionCompute /
	// GetChainOutputFromBranch. Apply commit-time appends (upgrade inject, GC) to a clone.
	muts := pb.Mutations.Clone()

	// inject any missing upgrade UTXOs
	baselineReader := multistate.MustNewReadable(b.StateStore(), baselineRoot, 0)
	injectedUpgrades := multistate.InjectMissingUpgradeUTXOs(muts, baselineReader, branchID.Slot())

	// log upgrade activations
	for _, upg := range injectedUpgrades {
		b.Log().Infof("\n"+
			"***************************************************************\n"+
			"***         LEDGER UPGRADE ACTIVATED AT SLOT %-6d         ***\n"+
			"***************************************************************\n"+
			" Library Hash: %s\n"+
			"***************************************************************",
			upg.Slot, hex.EncodeToString(upg.LibraryHash[:]))
	}

	// GC old transaction IDs: only prune txIDs whose unspent output set is empty. Retention is
	// tiered by branch flag (claude/txid_ttl_tiered.md): non-branch records are pruned at a short
	// horizon, branch records at a far longer one. Each kind prunes the single slot that just
	// crossed its horizon. Route the trie iteration through the cached state reader for the
	// baseline rather than upd.Readable() — the cached reader's trie node cache (sized
	// stateReaderCacheLimit) survives across commits, so the top-of-trie nodes stay warm and
	// PrunableTxIDsAtSlot doesn't pay full cold-cache I/O each time. See claude/trie_iteration.md §2.a.
	if branchID.Slot() > pb.TxIDTTLSlots {
		gcSlot := branchID.Slot() - pb.TxIDTTLSlots
		gcTxIDs := b.prunableTxIDsAtSlotCached(pb.BaselineBranchID, gcSlot, false)
		if gcTxIDs == nil {
			// Fallback: cached reader unavailable (e.g., baseline state reader couldn't
			// be created). Fall back to the per-call fresh Updatable's reader.
			gcTxIDs = upd.Readable().PrunableTxIDsAtSlot(gcSlot, false)
		}
		muts.DeleteTxIDs(gcTxIDs...)
		// Set GCSlotNonBranch so that inline output deletions also clean up non-branch TX records
		// that missed the per-slot GC scan because they still had unspent outputs.
		muts.GCSlotNonBranch = gcSlot
	}
	// Branch txID GC. Prune the branch record whose slot just crossed the branch horizon.
	// The schedule MUST be a pure function of the branch slot: branch txid records live in the
	// Merkle-committed trie, so any node-local variation makes the committed state root differ
	// between a continuously-running node and a snapshot-restored one. It must NOT be gated on the
	// earliest-retained slot — on a snapshot-restored node that is the recent restored branch (not the
	// genesis anchor). Such a gate made a restored node skip every branch-record prune at/below its
	// restore point, retaining records the network had already pruned, so its root diverged at
	// snapshot+1 and compounded each slot. The genesis anchor (slot 0) is never a target here:
	// gcSlotBranch == 0 only when branchID.Slot() == BranchTxIDTTLSlots, excluded by the outer strict
	// inequality. Branch RootRecords are deleted atomically with the trie prune (DeleteBranchTxIDs ->
	// updateUTXOLedgerDB); on a restored node the below-restore-point records carry no RootRecord, so
	// those deletions are harmless no-ops.
	//
	// When the prune reaches the restored node's own anchor slot (restoreSlot + BranchTxIDTTLSlots
	// later) it deletes that anchor's RootRecord too; the earliest-slot marker is advanced below so the
	// floor follows, and FetchEarliestBranchIDList resolves the floor from the retained records rather
	// than a since-pruned anchor.
	if pb.BranchTxIDTTLSlots > 0 && branchID.Slot() > pb.BranchTxIDTTLSlots {
		gcSlotBranch := branchID.Slot() - pb.BranchTxIDTTLSlots
		gcBranchIDs := b.prunableTxIDsAtSlotCached(pb.BaselineBranchID, gcSlotBranch, true)
		if gcBranchIDs == nil {
			gcBranchIDs = upd.Readable().PrunableTxIDsAtSlot(gcSlotBranch, true)
		}
		muts.DeleteBranchTxIDs(gcBranchIDs...)
		// This commit prunes the branch records at gcSlotBranch, so nothing is retained below
		// gcSlotBranch+1. Advance the earliest-slot marker to it, atomically in the same batch. The
		// marker is a monotonic LOWER BOUND on retained slots, not necessarily a slot that holds a
		// branch (gcSlotBranch+1 may be an empty slot); readers scan/iterate forward from it.
		pb.RootRecParams.AdvanceEarliestSlotTo = gcSlotBranch + 1
	}

	// commit to DB. On a token-conservation mismatch updateTrie returns the error BEFORE the
	// badger batch is opened, so nothing is written and the last good committed state stays
	// intact on disk. A mismatch here means the deferred branch's precomputed mutations no
	// longer agree with the baseline state — genuine inconsistency, not a recoverable condition.
	// Initiate a graceful shutdown instead of crashing via Fatalf: the orderly stop closes the
	// state and txstore DBs cleanly, preserving that good state so the node can restart from it.
	if err := upd.Update(muts, pb.RootRecParams); err != nil {
		b.Log().Errorf(">>>>>>>> **************** BRANCH COMMIT INCONSISTENCY ****************** \n"+
			"_commitPendingBranchUnlocked(%s) baseline=%s -> %v\n-------- mutations --------\n%s",
			branchID.StringShort(), pb.BaselineBranchID.StringShort(), err, muts.Lines("    ").String())
		b.GracefulShutdown(fmt.Sprintf("branch commit inconsistency for %s (baseline %s): %v",
			branchID.StringShort(), pb.BaselineBranchID.StringShort(), err))
		// upd is unchanged (its root is still the baseline root); return it so the caller unwinds
		// without a nil deref while the node stops.
		return upd
	}

	// log the deferred commit and committed transactions
	var numSeq, numNonSeq int
	for i := range pb.CommittedTxs {
		if pb.CommittedTxs[i].IsSequencerTransaction() {
			numSeq++
		} else {
			numNonSeq++
		}
	}
	var coveragePct float64
	if pb.Supply > 0 {
		coveragePct = float64(pb.CoverageDelta) * 100 / float64(pb.Supply)
	}
	b.LogTopicf("branch_commit", 1, "--- BRANCH COMMIT %s '%s' coverage delta: %s (%.2f%%), tx: %d seq + %d non-seq",
		branchID.StringShort(), pb.SequencerName, util.Th(pb.CoverageDelta), coveragePct, numSeq, numNonSeq)
	b.LogTx(time.Now(), fmt.Sprintf("committed in branch %s (deferred)", branchID.String()), pb.CommittedTxs...)

	b.NotifyBranchCommitted(branchID.Slot())

	return upd
}

func (b *Branches) SequencerOutputID(branchID base.TransactionID) (base.OutputID, bool) {
	util.Assertf(branchID.IsBranchTransaction(), "branch transaction ChainID expected. Got %s", branchID.StringShort)
	b.mutex.Lock()
	defer b.mutex.Unlock()

	bd, ok := b._getAndCacheNoLock(branchID)
	if !ok {
		return base.OutputID{}, false
	}
	return bd.SequencerOutput.ID, true
}

// GetStateReaderForTheBranch returns a state reader for the branch or nil if the state does not exist.
// If the branch is before the snapshot and branch ChainID is known in the snapshot state, it returns the snapshot state (which always exists).
//
// For pending (deferred) branches, the DB commit is performed outside b.mutex to prevent
// a lock convoy. Previously, _commitPendingBranch ran under b.mutex and its slow trie
// iteration (PrunableTxIDsAtSlot GC scan) blocked all concurrent GetStateReaderForTheBranch,
// BranchKnowsTransaction, and other branches.mutex callers for seconds, which cascaded
// through IsConsumedInThePastPath (holding ownMilestonesMutex) to stall the entire
// sequencer loop and trigger the deadlock detector.
//
// The commit-outside-lock pattern uses the b.committing channel map: the first goroutine
// to reach a pending branch registers a channel, releases the mutex, performs the commit,
// then stores results and closes the channel. Concurrent goroutines for the same branch
// wait on the channel and retry.
func (b *Branches) GetStateReaderForTheBranch(branchID base.TransactionID) multistate.IndexedStateReader {
	util.Assertf(branchID.IsBranchTransaction(), "GetStateReaderForTheBranchExt: branch tx expected. Got: %s", branchID.StringShort())

	switch {
	case branchID.Slot() < b.EarliestSlot():
		// below the floor: it can only be served by a floor branch whose state contains it. Recurse
		// into each floor branch's reader (always exists, so no deadlock) and return the first match.
		for _, floorBranchID := range b.EarliestBranchIDs() {
			floorRdr := b.GetStateReaderForTheBranch(floorBranchID)
			if floorRdr != nil && floorRdr.KnowsCommittedTransaction(branchID) {
				return floorRdr
			}
		}
		return nil
	case branchID.Slot() == b.EarliestSlot() && !slices.Contains(b.EarliestBranchIDs(), branchID):
		// a non-retained fork at the floor slot
		return nil
	}

	b.mutex.Lock()

	// fast path: cached state reader
	if ret := b.stateReaders[branchID]; ret != nil {
		ret.lastActivity = time.Now()
		b.mutex.Unlock()
		return ret.IndexedStateReader
	}

	bd, found := b._getAndCacheNoLock(branchID)
	if !found {
		b.mutex.Unlock()
		return nil
	}

	if bd.Root != nil {
		// committed branch: create and cache state reader
		rdr := &cachedStateReader{
			IndexedStateReader: multistate.MustNewReadable(b.StateStore(), bd.Root, stateReaderCacheLimit),
			lastActivity:       time.Now(),
		}
		b.stateReaders[branchID] = rdr
		b.mutex.Unlock()
		return rdr.IndexedStateReader
	}

	// pending branch — check if another goroutine is already committing it
	if ch, alreadyCommitting := b.committing[branchID]; alreadyCommitting {
		b.mutex.Unlock()
		// wait for the other goroutine to finish committing
		<-ch
		// retry — state reader should now be cached
		return b.GetStateReaderForTheBranch(branchID)
	}

	// extract pending data and baseline root under the lock
	pb := b.pending[branchID]
	baselineBD, baselineFound := b._getAndCacheNoLock(pb.BaselineBranchID)
	b.Assertf(baselineFound, "GetStateReaderForTheBranch: baseline branch %s not found", pb.BaselineBranchID.StringShort)
	b.Assertf(baselineBD.Root != nil, "GetStateReaderForTheBranch: baseline branch %s has nil root (still pending)", pb.BaselineBranchID.StringShort)
	baselineRoot := baselineBD.Root

	// mark this branch as being committed so other goroutines wait
	ch := make(chan struct{})
	b.committing[branchID] = ch
	b.mutex.Unlock()

	// branch commits are heavy allocators — nudge the async GC worker so it can
	// run runtime.GC() off-thread if heap is above threshold. Non-blocking: the
	// caller does not stall for STW. Worker rate-limits to one GC per asyncGCMinInterval.
	b.MemoryPressureGC()

	// do the expensive commit work outside the mutex
	upd := b._commitPendingBranchUnlocked(branchID, pb, baselineRoot)

	// store results under the lock
	b.mutex.Lock()
	bd = b.m[branchID]
	bd.BranchData.Root = upd.Root()
	b.m[branchID] = bd
	delete(b.pending, branchID)
	delete(b.committing, branchID)
	// the commit may have pruned the old floor and advanced the earliest-slot marker
	b._refreshEarliestNoLock()
	// eagerly free heavy allocations now that the pending entry is removed
	// and no concurrent virtual state reader can reference them
	pb.Mutations = nil
	pb.CommittedTxs = nil

	rdr := &cachedStateReader{
		IndexedStateReader: multistate.MustNewReadable(b.StateStore(), bd.Root, stateReaderCacheLimit),
		lastActivity:       time.Now(),
	}
	b.stateReaders[branchID] = rdr
	b.mutex.Unlock()

	// wake up any goroutines waiting for this commit
	close(ch)

	// signal memDAG to run LRB-depth pruning after this commit
	b.RequestPrune()

	return rdr.IndexedStateReader
}

// GetChainOutputFromBranch looks up a chain output in a branch without forcing a DB commit.
// It walks back through pending branches via stem links, scanning mutations at each hop.
// Only falls back to a committed state reader when a committed branch is reached.
func (b *Branches) GetChainOutputFromBranch(branchID base.TransactionID, chainID base.ChainID) (*ledger.OutputWithID, error) {
	b.mutex.Lock()

	currentID := branchID
	for {
		pb, isPending := b.pending[currentID]
		if !isPending {
			// reached a committed (or DB-fetched) branch — use its state reader
			b.mutex.Unlock()
			rdr := b.GetStateReaderForTheBranch(currentID)
			if rdr == nil {
				return nil, multistate.ErrNotFound
			}
			return multistate.MakeSugared(rdr).GetChainOutputWithID(chainID)
		}

		// check mutations for the chain output
		if out, found := pb.Mutations.FindChainOutput(chainID); found {
			b.mutex.Unlock()
			return out, nil
		}
		// check if chain was deleted in this branch
		if pb.Mutations.IsChainDeleted(chainID) {
			b.mutex.Unlock()
			return nil, multistate.ErrNotFound
		}
		// chain not modified here — walk back to previous branch via stem link
		currentID = pb.PreviousBranchID
	}
}

// FindLatestReliableBranch finds the LRB using both committed and pending branches from b.m.
// Once found, the LRB is committed to DB via GetStateReaderForTheBranch.
func (b *Branches) FindLatestReliableBranch() *multistate.BranchData {
	b.mutex.Lock()

	// find the latest slot in b.m that has at least one healthy branch
	var latestHealthySlot uint32
	found := false
	for txid, bd := range b.m {
		if !txid.IsBranchTransaction() {
			continue
		}
		slot := txid.Slot()
		if global.IsHealthyCoverageDelta(bd.CoverageDelta, bd.Supply, global.FractionHealthyBranch()) {
			if !found || slot > latestHealthySlot {
				latestHealthySlot = slot
				found = true
			}
		}
	}
	if !found {
		b.mutex.Unlock()
		// b.m has no healthy branches (e.g., startup or tests) — fall back to DB
		return multistate.FindLatestReliableBranch(b.StateStore(), global.FractionHealthyBranch())
	}

	// collect all healthy branches at the latest healthy slot
	type tipEntry struct {
		id base.TransactionID
		bd *multistate.BranchData
	}
	var tips []tipEntry
	for txid, bd := range b.m {
		if txid.Slot() == latestHealthySlot && txid.IsBranchTransaction() &&
			global.IsHealthyCoverageDelta(bd.CoverageDelta, bd.Supply, global.FractionHealthyBranch()) {
			tips = append(tips, tipEntry{txid, bd.BranchData})
		}
	}
	b.mutex.Unlock()

	if len(tips) == 1 {
		// single healthy branch — commit it and return
		b.GetStateReaderForTheBranch(tips[0].id)
		return tips[0].bd
	}

	// multiple healthy tips: pick the heaviest, walk back to find the reliable branch
	heaviestIdx := 0
	for i := 1; i < len(tips); i++ {
		if tips[i].bd.CoverageDelta > tips[heaviestIdx].bd.CoverageDelta {
			heaviestIdx = i
		}
	}

	// collect non-heaviest tip branch IDs for cross-checking
	otherTipIDs := make([]base.TransactionID, 0, len(tips)-1)
	for i := range tips {
		if i != heaviestIdx {
			otherTipIDs = append(otherTipIDs, tips[i].id)
		}
	}

	// walk back from the heaviest tip
	currentID := tips[heaviestIdx].id
	first := true
	for {
		if first {
			// skip the tip itself — it can't be "reliable" (not known in other tips yet)
			first = false
		} else {
			// check if currentID is known in all other tip branches
			knownInAll := true
			for _, otherID := range otherTipIDs {
				if !b.BranchKnowsTransaction(otherID, currentID) {
					knownInAll = false
					break
				}
			}
			if knownInAll {
				// found the LRB — commit it to DB and return
				b.GetStateReaderForTheBranch(currentID)
				b.mutex.Lock()
				bd, ok := b._getAndCacheNoLock(currentID)
				b.mutex.Unlock()
				if ok {
					return bd.BranchData
				}
				return nil
			}
		}
		// walk back via stem link
		b.mutex.Lock()
		bd, ok := b._getAndCacheNoLock(currentID)
		if !ok {
			b.mutex.Unlock()
			return nil
		}
		stemLock, stemOk := bd.Stem.Output.StemLock()
		b.mutex.Unlock()
		if !stemOk {
			return nil
		}
		currentID = stemLock.PredecessorOutputID.TransactionID()
	}
}

// BranchKnowsTransaction reports whether `txid` is part of the state at `branchID`.
// Walks back through pending (uncommitted) branches via stem links to answer
// without forcing a DB commit; on reaching a committed branch, delegates to its
// state reader. Readable already has its own L2 cache for txID records
// (Readable.txCache), evicted when the reader itself is evicted from
// b.stateReaders — no separate global cache is needed here.
// prunableTxIDsAtSlotCached returns prunable txIDs at `slot` in `branchID`'s state,
// going through the cached state reader for `branchID` (b.stateReaders) rather than
// constructing a fresh *Readable. The cached reader's trie node cache is reused
// across commits, so the top-of-trie nodes stay warm. Returns nil if the cached
// reader is unavailable; the caller should fall back to a fresh reader path.
func (b *Branches) prunableTxIDsAtSlotCached(branchID base.TransactionID, slot uint32, branch bool) []base.TransactionID {
	rdr := b.GetStateReaderForTheBranch(branchID)
	if rdr == nil {
		return nil
	}
	// GetStateReaderForTheBranch always returns the *multistate.Readable wrapped
	// in the IndexedStateReader interface (see cachedStateReader construction).
	r, ok := rdr.(*multistate.Readable)
	if !ok {
		return nil
	}
	return r.PrunableTxIDsAtSlot(slot, branch)
}

func (b *Branches) BranchKnowsTransaction(branchID, txid base.TransactionID) bool {
	util.Assertf(branchID.IsBranchTransaction(), "branch tx expected. Got: %s", branchID.StringShort)
	if branchID == txid {
		return true
	}
	if branchID.Slot() <= txid.Slot() {
		return false
	}
	b.mutex.Lock()
	currentID := branchID
	for {
		pb, isPending := b.pending[currentID]
		if !isPending {
			// reached a committed branch — use its state reader
			b.mutex.Unlock()
			// TODO: add context/timeout to KnowsCommittedTransaction to prevent indefinite blocking on slow trie reads
			rdr := b.GetStateReaderForTheBranch(currentID)
			if rdr == nil {
				return false
			}
			return rdr.KnowsCommittedTransaction(txid)
		}
		// check if this branch added the txID
		if pb.Mutations.HasTx(txid) {
			b.mutex.Unlock()
			return true
		}
		// check if this branch deleted the txID (TTL expiry)
		if pb.Mutations.HasDeletedTx(txid) {
			b.mutex.Unlock()
			return false
		}
		// not modified here — walk back to previous branch
		currentID = pb.PreviousBranchID
	}
}

// IsDescendantBranch returns:
//
//	compatible = true -> then isDescendentOf=true if branch1 known branch2 and false otherwise
//	compatible = false -> branches not in the same chain and isDescendentOf is undefined
func (b *Branches) IsDescendantBranch(descendant, ancestor base.TransactionID) (sameLineage bool, isDescendant bool) {
	b.Assertf(descendant.IsBranchTransaction() && ancestor.IsBranchTransaction(), "branchID1.IsBranchTransaction() && ancestor.IsBranchTransaction()")
	if b.BranchKnowsTransaction(descendant, ancestor) {
		return true, true
	}
	return b.BranchKnowsTransaction(ancestor, descendant), false
}

// TransactionIsInEarliestState reports whether txid was committed at or before the retained-history
// floor and is still known by it. No trust-by-age: a committed tx with any surviving output keeps its
// record, so it reads as known; an ancient one beyond retention is correctly unknown. See
// claude/txid_ttl_tiered.md.
func (b *Branches) TransactionIsInEarliestState(txid base.TransactionID) bool {
	if txid.Timestamp().After(base.T(b.EarliestSlot(), 0)) {
		return false
	}
	_, ok := b.EarliestStateKnowsTransaction(txid)
	return ok
}

// ChainLines for debugging
func (b *Branches) ChainLines(tipOrig base.TransactionID, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	b.mutex.Lock()
	defer b.mutex.Unlock()

	tip := tipOrig
	for i := 0; i < 80; i++ {
		bd, ok := b._getAndCacheNoLock(tip)
		if !ok {
			ret.Add("%2d:  %s  <- chain ends here", i, tip.StringShort())
			break
		}
		slotsSinceTip := tipOrig.Slot() - tip.Slot()
		b.Assertf(tip.Slot() == bd.Slot(), "tip.Slot() == bd.Slot()")
		ret.Add("%2d:  %s (-%d), delta: %s, delta>>slots: %s, coverage: %s",
			i, tip.StringShort(), slotsSinceTip, util.Th(bd.CoverageDelta),
			util.Th(bd.CoverageDelta>>slotsSinceTip), util.Th(bd.TotalCoverage))

		tip = bd.StemPredecessorBranchID()
	}
	return ret
}
