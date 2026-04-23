// Package branches implements caching of branch data
package branches

import (
	"encoding/hex"
	"fmt"
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

	branchDataWithLedgerCoverage struct {
		*multistate.BranchData
		ledgerCoverage uint64
		lastActive     time.Time
	}

	// knowsTxKey is the cache key for L2 KnowsCommittedTransaction cache.
	// It avoids hitting the trie (which requires an exclusive Readable.mutex lock)
	// for repeated queries on the same (branchID, txid) pair.
	knowsTxKey struct {
		branchID base.TransactionID
		txid     base.TransactionID
	}

	Branches struct {
		environment
		mutex            sync.Mutex
		snapshotBranchID base.TransactionID
		m                map[base.TransactionID]branchDataWithLedgerCoverage

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

		// knowsTxCache is an L2 cache for BranchKnowsTransaction results.
		// Protected by knowsTxMu (RWMutex): RLock for cache hits, Lock for cache writes.
		// This avoids contention on the trie's exclusive Readable.mutex for repeated queries.
		knowsTxMu    sync.RWMutex
		knowsTxCache map[knowsTxKey]bool
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
		BaselineBranchID base.TransactionID
		PreviousBranchID base.TransactionID // stem link to previous branch (for mutation chain traversal)
		TxIDTTLSlots     uint32
		CommittedTxs     []base.TransactionID
		SequencerName    string
	}
)

const (
	stateReaderTTLSlots     = 2
	branchDataCacheTTLSlots = 12
	stateReaderCacheLimit   = 3000
	stateReaderCacheMaxSize = 100 // hard cap on cached state readers; evict oldest when exceeded
)

func New(env environment) *Branches {
	ret := &Branches{
		environment:      env,
		snapshotBranchID: multistate.FetchSnapshotBranchID(env.StateStore()),
		m:                make(map[base.TransactionID]branchDataWithLedgerCoverage),
		stateReaders:     make(map[base.TransactionID]*cachedStateReader),
		pending:          make(map[base.TransactionID]*PendingBranchCommit),
		committing:       make(map[base.TransactionID]chan struct{}),
		knowsTxCache:     make(map[knowsTxKey]bool),
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

func (b *Branches) SnapshotBranchID() base.TransactionID {
	return b.snapshotBranchID
}

func (b *Branches) SnapshotSlot() uint32 {
	return b.snapshotBranchID.Slot()
}

func (b *Branches) _getAndCacheNoLock(branchID base.TransactionID) (branchDataWithLedgerCoverage, bool) {
	bd, ok := b.m[branchID]
	if ok {
		if branchID.Slot() > 0 {
			b.Assertf(bd.ledgerCoverage == 0 || bd.ledgerCoverage >= bd.CoverageDelta, "bd.ledgerCoverage == 0 || bd.LedgerCoverage(%s) >= bd.CoverageDeltaRaw(%s) for %s",
				util.Th(bd.ledgerCoverage), util.Th(bd.CoverageDelta), branchID.StringShort)
		}
		bd.lastActive = time.Now()
		b.m[branchID] = bd
		return bd, true
	}

	if branchID.Slot() < b.snapshotBranchID.Slot() ||
		(branchID.Slot() == b.snapshotBranchID.Slot() && branchID != b.snapshotBranchID) {
		// the branch is impossible assuming the snapshot baseline
		return branchDataWithLedgerCoverage{}, false
	}

	// fetch branch from the database
	if rd, found := multistate.FetchRootRecord(b.StateStore(), branchID); found {
		bdRec := multistate.FetchBranchDataByRoot(b.StateStore(), rd)
		bd = branchDataWithLedgerCoverage{
			BranchData:     &bdRec,
			ledgerCoverage: 0, // will be lazy-calculated when needed
			lastActive:     time.Now(),
		}
		b.m[branchID] = bd
		return bd, true
	}
	return branchDataWithLedgerCoverage{}, false
}

// _ledgerCoverage traverses branches back up to 64 slots and calculates full coverage
func (b *Branches) _ledgerCoverage(br branchDataWithLedgerCoverage) (ret uint64) {
	b.Assertf(br.ledgerCoverage == 0, "brOrig.ledgerCoverage == 0")

	var slotsBack uint32
	var ok bool

	branchID := br.TxID()
	origSlot := br.Slot()

	// coverage delta cannot be greater than supply
	for maxContribution := br.Supply; maxContribution > 0; maxContribution >>= 1 {
		if br, ok = b._getAndCacheNoLock(branchID); !ok {
			break
		}
		slotsBack = origSlot - branchID.Slot()
		ret += br.CoverageDelta >> slotsBack
		branchID = br.StemPredecessorBranchID()
	}
	return
}

// LedgerCoverage strictly speaking, is non-deterministic if the snapshot is after the genesis
// However:
//   - if branchID is far enough (63 slots), it is guaranteed to be the real value and therefore deterministic
//   - if the snapshot is N slots behind the branchID, it is guaranteed that the returned value differs from
//     the real value no more than by 1/2^N
func (b *Branches) LedgerCoverage(branchID base.TransactionID) uint64 {
	util.Assertf(branchID.IsBranchTransaction(), "branch transaction ChainID expected. Got %s", branchID.StringShort)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	bd, ok := b._getAndCacheNoLock(branchID)
	if !ok {
		return 0
	}
	if bd.ledgerCoverage > 0 {
		return bd.ledgerCoverage
	}
	bd.ledgerCoverage = b._ledgerCoverage(bd)
	b.Assertf(bd.ledgerCoverage > 0, "LedgerCoverage: bd.ledgerCoverage > 0 for %s", branchID.StringShort)

	b.m[branchID] = bd
	return bd.ledgerCoverage
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

	// build BranchData with nil Root for immediate use by coverage/supply lookups
	bd := branchDataWithLedgerCoverage{
		BranchData: &multistate.BranchData{
			RootRecord: multistate.RootRecord{
				// Root is nil — will be set when committed
				SequencerID:     pb.RootRecParams.SeqID,
				CoverageDelta:   pb.RootRecParams.CoverageDelta,
				FrozenCoverage:  pb.RootRecParams.FrozenCoverage,
				SlotInflation:   pb.RootRecParams.SlotInflation,
				Supply:          pb.RootRecParams.Supply,
				NumTransactions: pb.RootRecParams.NumTransactions,
			},
			Stem:            stemOutput,
			SequencerOutput: sequencerOutput,
		},
		ledgerCoverage: 0,
		lastActive:     time.Now(),
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

	// GC old transaction IDs: only prune txIDs whose unspent output set is empty
	if branchID.Slot() > pb.TxIDTTLSlots {
		gcSlot := branchID.Slot() - pb.TxIDTTLSlots
		gcTxIDs := upd.Readable().PrunableTxIDsAtSlot(gcSlot)
		muts.DeleteTxIDs(gcTxIDs...)
		// Set GCSlot so that output deletions also clean up TX records
		// for TXs that missed the per-slot GC scan because they still had unspent outputs
		muts.GCSlot = gcSlot
	}

	// commit to DB
	err := upd.Update(muts, pb.RootRecParams)
	if err != nil {
		err = fmt.Errorf("_commitPendingBranchUnlocked(%s) baseline=%s -> %w:\n-------- mutations --------\n%s",
			branchID.StringShort(), pb.BaselineBranchID.StringShort(), err, muts.Lines("    ").String())
	}
	b.Assertf(err == nil, "%v", err)

	// log the deferred commit and committed transactions
	var numSeq, numNonSeq int
	for i := range pb.CommittedTxs {
		if pb.CommittedTxs[i].IsSequencerTransaction() {
			numSeq++
		} else {
			numNonSeq++
		}
	}
	coveragePct := float64(pb.RootRecParams.CoverageDelta) * 100 / float64(pb.RootRecParams.Supply)
	b.LogTopicf("branch_commit", 1, "--- BRANCH COMMIT %s '%s' coverage delta: %s (%.2f%%), tx: %d seq + %d non-seq",
		branchID.StringShort(), pb.SequencerName, util.Th(pb.RootRecParams.CoverageDelta), coveragePct, numSeq, numNonSeq)
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

	snapID := b.SnapshotBranchID()
	switch {
	case branchID.Slot() < snapID.Slot():
		// recursive but won't deadlock because the snapshot state always exists
		snapRdr := b.GetStateReaderForTheBranch(snapID)
		if snapRdr.KnowsCommittedTransaction(branchID) {
			return snapRdr
		}
		return nil
	case branchID.Slot() == snapID.Slot() && branchID != snapID:
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
		if global.IsHealthyCoverageDelta(bd.CoverageDelta, bd.Supply, global.FractionHealthyBranch) {
			if !found || slot > latestHealthySlot {
				latestHealthySlot = slot
				found = true
			}
		}
	}
	if !found {
		b.mutex.Unlock()
		// b.m has no healthy branches (e.g., startup or tests) — fall back to DB
		return multistate.FindLatestReliableBranch(b.StateStore(), global.FractionHealthyBranch)
	}

	// collect all healthy branches at the latest healthy slot
	type tipEntry struct {
		id base.TransactionID
		bd *multistate.BranchData
	}
	var tips []tipEntry
	for txid, bd := range b.m {
		if txid.Slot() == latestHealthySlot && txid.IsBranchTransaction() &&
			global.IsHealthyCoverageDelta(bd.CoverageDelta, bd.Supply, global.FractionHealthyBranch) {
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

func (b *Branches) BranchKnowsTransaction(branchID, txid base.TransactionID) bool {
	util.Assertf(branchID.IsBranchTransaction(), "branch tx expected. Got: %s", branchID.StringShort)
	if branchID == txid {
		return true
	}
	if branchID.Slot() <= txid.Slot() {
		return false
	}

	// check L2 cache first (RLock — no contention with other readers)
	cacheKey := knowsTxKey{branchID: branchID, txid: txid}
	b.knowsTxMu.RLock()
	if result, cached := b.knowsTxCache[cacheKey]; cached {
		b.knowsTxMu.RUnlock()
		return result
	}
	b.knowsTxMu.RUnlock()

	// cache miss — compute the result
	result := b.branchKnowsTransactionCompute(branchID, txid)

	// populate L2 cache
	b.knowsTxMu.Lock()
	b.knowsTxCache[cacheKey] = result
	b.knowsTxMu.Unlock()

	return result
}

// branchKnowsTransactionCompute walks pending branches then falls through to the trie
func (b *Branches) branchKnowsTransactionCompute(branchID, txid base.TransactionID) bool {
	// walk back through pending branches via stem links to avoid forcing DB commits
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

func (b *Branches) SnapshotKnowsTransaction(txid base.TransactionID) bool {
	if b.BranchKnowsTransaction(b.snapshotBranchID, txid) {
		return true
	}
	// Handle TxID TTL expiry: for very old transactions, the txID entry may have been deleted
	// from the trie and all outputs consumed, causing BranchKnowsTransaction to return false
	// even though the transaction was legitimately committed. This prevents the attacher cascade
	// from walking the entire chain history back to genesis.
	return b.txidMayHaveExpiredFromSnapshot(txid)
}

// txidMayHaveExpiredFromSnapshot returns true if the transaction is old enough relative
// to the snapshot that its txID entry may have been deleted from the trie due to TTL expiry.
// For such transactions, BranchKnowsTransaction may return false even though the transaction
// was committed. This is safe because:
// - The transaction predates the snapshot by more than the TTL period
// - Any transaction loaded from the txstore with such an old timestamp was committed
// - Fake old transactions from malicious peers are caught by constraint validation
func (b *Branches) txidMayHaveExpiredFromSnapshot(txid base.TransactionID) bool {
	txSlot := txid.Slot()
	snapSlot := b.snapshotBranchID.Slot()
	if txSlot >= snapSlot {
		return false
	}
	ttl := ledger.L(snapSlot).TxIDStateTTLSlots
	return snapSlot-txSlot > ttl
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

func (b *Branches) TransactionIsInSnapshotState(txid base.TransactionID) bool {
	if txid.Timestamp().After(b.snapshotBranchID.Timestamp()) {
		return false
	}
	if b.BranchKnowsTransaction(b.snapshotBranchID, txid) {
		return true
	}
	// Handle TxID TTL expiry for very old transactions (see txidMayHaveExpiredFromSnapshot)
	return b.txidMayHaveExpiredFromSnapshot(txid)
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
			util.Th(bd.CoverageDelta>>slotsSinceTip), util.Th(bd.ledgerCoverage))

		tip = bd.StemPredecessorBranchID()
	}
	return ret
}

//func (b *Branches) IterateBranchesBack(tip base.TransactionID, fun func(branchID base.TransactionID, branchData *multistate.BranchData) bool) {
//	b.mutex.Lock()
//	defer b.mutex.Unlock()
//
//	bd, ok := b._getAndCacheNoLock(tip)
//	for ok && fun(tip, bd) {
//		tip = bd.StemPredecessorBranchID()
//		bd, ok = b.getNoLock(tip)
//	}
//}

// works badly in startup, where enough to have lrb from DB, i.e., without recursively calculated coverage

//func (b *Branches) FindLatestReliableBranch(fraction global.Fraction) *multistate.BranchData {
//	tipRoots, ok := multistate.FindRootsFromLatestHealthySlot(b.StateStore(), fraction)
//	if !ok {
//		return nil
//	}
//	b.Assertf(len(tipRoots) > 0, "healthyRoots is empty")
//	tipRoots = util.PurgeSlice(tipRoots, func(rr multistate.RootRecord) bool {
//		return global.IsHealthyCoverageDelta(rr.CoverageDelta, rr.Supply, fraction)
//	})
//	util.Assertf(len(tipRoots) > 0, "len(tipRoots)>0")
//
//	if len(tipRoots) == 1 {
//		// if only one branch is in the latest healthy slot, it is the one reliable
//		bd, ok := b.Get(multistate.FetchBranchIDByRoot(b.StateStore(), tipRoots[0].Root))
//		util.Assertf(ok, "inconsistency: branchID by root not found")
//		return util.Ref(bd)
//	}
//
//	rootMaxIdx := util.IndexOfMaximum(tipRoots, func(i, j int) bool {
//		return tipRoots[i].CoverageDelta < tipRoots[j].CoverageDelta
//	})
//	util.Assertf(global.IsHealthyCoverageDelta(tipRoots[rootMaxIdx].CoverageDelta, tipRoots[rootMaxIdx].Supply, fraction),
//		"global.IsHealthyCoverageDelta(rootMax.LedgerCoverage, rootMax.Supply, fraction)")
//
//	tipBranchID := multistate.FetchBranchIDByRoot(b.StateStore(), tipRoots[rootMaxIdx].Root)
//
//	readers := make([]*multistate.Readable, 0, len(tipRoots)-1)
//	for i := range tipRoots {
//		// no need to check in the main tip, skip it
//		if !ledger.CommitmentModel.EqualCommitments(tipRoots[i].Root, tipRoots[rootMaxIdx].Root) {
//			readers = append(readers, multistate.MustNewReadable(b.StateStore(), tipRoots[i].Root))
//		}
//	}
//	util.Assertf(len(readers) > 0, "len(readers) > 0")
//
//	var branchFound *multistate.BranchData
//	first := true
//
//	b.IterateBranchesBack(tipBranchID, func(branchID base.TransactionID, bd *multistate.BranchData) bool {
//		if first {
//			// skip the tip itself
//			first = false
//			return true
//		}
//		// check if the branch is included in every reader
//		for _, rdr := range readers {
//			if !rdr.KnowsCommittedTransaction(branchID) {
//				// the transaction is not known by at least one of selected states,
//				// it is not a reliable branch, keep traversing back
//				return true
//			}
//		}
//		// branchID is known in all tip states. It is the reliable one
//		branchFound = bd
//		return false
//	})
//	return branchFound
//}
