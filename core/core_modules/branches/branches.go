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
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
	}

	branchDataWithLedgerCoverage struct {
		*multistate.BranchData
		ledgerCoverage uint64
		lastActive     time.Time
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
	}

	cachedStateReader struct {
		multistate.IndexedStateReader
		lastActivity time.Time
	}

	// PendingBranchCommit holds data needed to lazily commit a branch to DB.
	// The actual DB write is deferred until the branch state is requested via GetStateReaderForTheBranch().
	PendingBranchCommit struct {
		Mutations          *multistate.Mutations
		RootRecParams      *multistate.RootRecordParams
		BaselineBranchID   base.TransactionID
		PreviousBranchID   base.TransactionID // stem link to previous branch (for mutation chain traversal)
		TxIDTTLSlots       uint32
		CommittedTxs       []base.TransactionID
		SequencerName      string
	}
)

const (
	stateReaderTTLSlots     = 2
	branchDataCacheTTLSlots = 12
	stateReaderCacheLimit   = 3000
)

func New(env environment) *Branches {
	ret := &Branches{
		environment:      env,
		snapshotBranchID: multistate.FetchSnapshotBranchID(env.StateStore()),
		m:                make(map[base.TransactionID]branchDataWithLedgerCoverage),
		stateReaders:     make(map[base.TransactionID]*cachedStateReader),
		pending:          make(map[base.TransactionID]*PendingBranchCommit),
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
				b.Log().Infof("orphaned branch %s (%s, %s), discarding uncommitted state",
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

// _commitPendingBranch performs the actual DB commit for a deferred branch.
// Must be called under b.mutex.
func (b *Branches) _commitPendingBranch(branchID base.TransactionID) {
	pb, ok := b.pending[branchID]
	if !ok {
		return
	}

	// get baseline branch root from cache or DB
	baselineBD, baselineFound := b._getAndCacheNoLock(pb.BaselineBranchID)
	b.Assertf(baselineFound, "_commitPendingBranch: baseline branch %s not found", pb.BaselineBranchID.StringShort)
	b.Assertf(baselineBD.Root != nil, "_commitPendingBranch: baseline branch %s has nil root (still pending)", pb.BaselineBranchID.StringShort)

	// create updatable state from baseline root
	upd := multistate.MustNewUpdatable(b.StateStore(), baselineBD.Root)

	// inject any missing upgrade UTXOs
	baselineReader := multistate.MustNewReadable(b.StateStore(), baselineBD.Root, 0)
	injectedUpgrades := multistate.InjectMissingUpgradeUTXOs(pb.Mutations, baselineReader, branchID.Slot())

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

	// GC old transaction IDs (deterministic operation on the state)
	if branchID.Slot() > pb.TxIDTTLSlots {
		gcSlot := branchID.Slot() - pb.TxIDTTLSlots
		gcTxIDs := upd.Readable().KnownCommittedTxIDs(gcSlot)
		pb.Mutations.DeleteTxIDs(gcTxIDs...)
	}

	// commit to DB
	err := upd.Update(pb.Mutations, pb.RootRecParams)
	if err != nil {
		err = fmt.Errorf("_commitPendingBranch(%s) -> %w:\n-------- mutations --------\n%s",
			branchID.StringShort(), err, pb.Mutations.Lines("    ").String())
	}
	b.Assertf(err == nil, "%v", err)

	// update cached BranchData with the real root
	bd := b.m[branchID]
	bd.BranchData.Root = upd.Root()
	b.m[branchID] = bd

	// remove from pending
	delete(b.pending, branchID)

	// log the deferred commit and committed transactions
	coveragePct := float64(pb.RootRecParams.CoverageDelta) * 100 / float64(pb.RootRecParams.Supply)
	b.Log().Infof("--- BRANCH COMMIT %s '%s' coverage delta: %s (%.2f%%)",
		branchID.StringShort(), pb.SequencerName, util.Th(pb.RootRecParams.CoverageDelta), coveragePct)
	b.LogTx(time.Now(), fmt.Sprintf("committed in branch %s (deferred)", branchID.String()), pb.CommittedTxs...)
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
// If the branch is before the snapshot and branch ChainID is known in the snapshot state, it returns the snapshot state (which always exists)
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
	defer b.mutex.Unlock()

	ret := b.stateReaders[branchID]
	if ret != nil {
		ret.lastActivity = time.Now()
		return ret.IndexedStateReader
	}
	bd, found := b._getAndCacheNoLock(branchID)
	if !found {
		return nil
	}
	// if Root is nil, this is a pending (deferred) branch — commit it now
	if bd.Root == nil {
		b._commitPendingBranch(branchID)
		bd = b.m[branchID]
	}
	b.stateReaders[branchID] = &cachedStateReader{
		IndexedStateReader: multistate.MustNewReadable(b.StateStore(), bd.Root, stateReaderCacheLimit),
		lastActivity:       time.Now(),
	}
	return b.stateReaders[branchID]
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

func (b *Branches) BranchKnowsTransaction(branchID, txid base.TransactionID) bool {
	util.Assertf(branchID.IsBranchTransaction(), "branch tx expected. Got: %s", branchID.StringShort)
	if branchID == txid {
		return true
	}
	if branchID.Slot() <= txid.Slot() {
		return false
	}

	// walk back through pending branches via stem links to avoid forcing DB commits
	b.mutex.Lock()
	currentID := branchID
	for {
		pb, isPending := b.pending[currentID]
		if !isPending {
			// reached a committed branch — use its state reader
			b.mutex.Unlock()
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
	return b.BranchKnowsTransaction(b.snapshotBranchID, txid)
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
	return b.BranchKnowsTransaction(b.snapshotBranchID, txid)
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
