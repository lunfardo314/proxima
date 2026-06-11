// Package forward_sync implements forward-syncing: windowed parallel branch catch-up.
//
// Always-on background process. Monitors the gap between the highest branch slot
// heard from peers (NOT wall-clock slot) and the latest committed healthy branch.
// Anchoring the gap to peer-observed branches — rather than wall clock — means a
// cold network restart, where no node is producing branches yet, does not look like
// a large gap and does not spuriously trigger sync; the gap only grows once peers
// genuinely outpace this node. When gap >= thresholdUp, starts pulling branches
// from trusted API sources. When gap <= thresholdDown, goes idle and hands the
// remaining tail back to gossip-driven recursive pull (bounded by
// vertex.MaxAttachmentDepthForPull, kept >= thresholdDown so recursion can span it).
//
// Pull strategy: pulls a window of pull_ahead branches in parallel (ascending slot order).
// Each branch triggers an attacher goroutine that recursively solidifies its past cone.
// Parallel attachers overlap their network round-trips, dramatically speeding up sync
// compared to pulling one branch at a time. The next window starts after all branches
// in the current window are committed.
//
// Coexists with recursive pull: recursive attachers handle recent transactions
// (capped at maxAttachmentDepthForPull). When they stall at the depth cap,
// the gap grows, forward-sync kicks in and commits the missing branches,
// satisfying the stalled attachers' dependencies.
package forward_sync

import (
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/spf13/viper"
)

const (
	Name = "forward_sync"

	// defaultThresholdUp: start forward-sync once the peer-anchored gap reaches this
	// many slots. Kept within gossip's recursive-pull reach (MaxAttachmentDepthForPull)
	// so there is no dead zone: any gap gossip cannot backfill on its own triggers sync.
	// Well above the measured steady-state healthy gap (~1-2 slots), so a caught-up node
	// does not flap, and below the minimum snapshot staleness (safety_slot_back), so every
	// snapshot restore self-heals instead of stalling.
	defaultThresholdUp = 10
	// defaultThresholdDown: go idle once the gap shrinks to this. Above steady-state noise
	// so a synced node stops force-committing, and well inside the gossip depth cap so the
	// handed-off tail always closes. Must stay < defaultThresholdUp (hysteresis).
	defaultThresholdDown = 4
	defaultPullAhead     = 5
	defaultCommitBatch   = 10
	syncLoopPeriod       = time.Second
	pullRepeatInterval   = 5 * time.Second
	// forkSafetyDepth: how many branches back from LRB to use as the anchor point
	// when requesting branch lists. Going back K slots gives margin for short-lived forks.
	forkSafetyDepth = 10
	// stallWarningTicks: after this many consecutive sync ticks where no source is ahead,
	// emit an ERROR-level warning. At 1s per tick this is ~30 seconds.
	stallWarningTicks = 30
	// stallWarningRepeat: repeat the stall warning every N ticks (~60 seconds)
	stallWarningRepeat = 60
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		TxBytesStore() global.TxBytesStore
		TxBytesFromStoreIn(txBytesWithMetadata []byte) (base.TransactionID, error)
		PullFromPeers(txid base.TransactionID) int
		AddPulledTransaction(txid base.TransactionID)
		ForceCommitBranch(branchID base.TransactionID)
		// LatestBranchSlotFromPeers returns the highest slot of any branch transaction
		// received and validated from peers (0 if none heard yet). This is the sync
		// anchor: the gap is measured against it, not against wall-clock slot.
		LatestBranchSlotFromPeers() uint32
	}

	Sync struct {
		environment
		sources       []*client.APIClient
		sourceURLs    []string // original URLs for diagnostics
		sourceIdx     int      // current source index, cycles on failure
		thresholdUp   uint32
		thresholdDown uint32
		pullAhead     int // window size: pull this many branches ahead in parallel
		commitBatch   int // max branches to commit per sync tick
		syncing       bool
		// latestTargetTicks is TicksSinceGenesis of the current forward-sync target.
		// Read by attacher goroutines via LatestForwardSyncedTimestamp() to skip depth cap.
		latestTargetTicks atomic.Int64
		// cached branch list (oldest first), protected by the sync loop goroutine (no concurrent access)
		branchList    []base.TransactionID
		currentTarget atomic.Uint32 // slot of the branch we're waiting for
		windowPulled  bool          // true when all branches in the current window have been pulled
		lastPullTime  time.Time     // when the current window was last pulled
		wakeup        chan struct{} // signaled when the target branch commits
		stallCounter  int          // consecutive sync ticks where no source was ahead
	}
)

// IsSelfURL returns true if the URL points to this node's own API.
// Checks localhost, 127.0.0.1, and all local interface IPs.
func IsSelfURL(url string, selfAPIPort int) bool {
	for selfURL := range localSelfURLs(selfAPIPort) {
		if strings.HasPrefix(url, selfURL) {
			return true
		}
	}
	return false
}

// localSelfURLs builds the set of URL prefixes that point to this node's own API.
// Called once at startup to avoid repeated net.InterfaceAddrs() calls.
func localSelfURLs(selfAPIPort int) map[string]bool {
	suffix := fmt.Sprintf(":%d", selfAPIPort)
	result := make(map[string]bool)
	// always include loopback/localhost
	for _, host := range []string{"127.0.0.1", "localhost"} {
		result["http://"+host+suffix] = true
		result["https://"+host+suffix] = true
	}
	// include all local interface IPs
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return result
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			ip := ipNet.IP.String()
			result["http://"+ip+suffix] = true
			result["https://"+ip+suffix] = true
		}
	}
	return result
}

// Start initializes and starts the sync module. Always active when sources are configured.
func Start(env environment) *Sync {
	sourceURLs := viper.GetStringSlice("sync.sources")
	if len(sourceURLs) == 0 {
		env.Log().Infof("[%s] no sync sources configured, forward-sync inactive", Name)
		return nil
	}

	// filter out self — detect all local IPs once at startup
	selfURLs := localSelfURLs(viper.GetInt("api.port"))
	filtered := make([]string, 0, len(sourceURLs))
	for _, url := range sourceURLs {
		// strip trailing path to match against the prefix set
		isSelf := false
		for selfURL := range selfURLs {
			if strings.HasPrefix(url, selfURL) {
				isSelf = true
				break
			}
		}
		if isSelf {
			env.Log().Infof("[%s] skipping self URL %s", Name, url)
			continue
		}
		filtered = append(filtered, url)
	}
	sourceURLs = filtered

	if len(sourceURLs) == 0 {
		env.Log().Infof("[%s] all sync sources are self, forward-sync inactive", Name)
		return nil
	}

	thUp := viper.GetInt("sync.threshold_up")
	if thUp <= 0 {
		thUp = defaultThresholdUp
	}
	thDown := viper.GetInt("sync.threshold_down")
	if thDown <= 0 {
		thDown = defaultThresholdDown
	}
	pullAhead := viper.GetInt("sync.pull_ahead")
	if pullAhead <= 0 {
		pullAhead = defaultPullAhead
	}
	commitBatch := viper.GetInt("sync.commit_batch")
	if commitBatch <= 0 {
		commitBatch = defaultCommitBatch
	}

	sources := make([]*client.APIClient, len(sourceURLs))
	for i, url := range sourceURLs {
		sources[i] = client.NewWithGoogleDNS(url, 10*time.Second)
	}

	ret := &Sync{
		environment:   env,
		sources:       sources,
		sourceURLs:    sourceURLs,
		thresholdUp:   uint32(thUp),
		thresholdDown: uint32(thDown),
		pullAhead:     pullAhead,
		commitBatch:   commitBatch,
		wakeup:        make(chan struct{}, 1),
	}

	go ret.syncLoop()

	env.Log().Infof("[%s] started, sources: %v, threshold up: %d, down: %d, pull ahead: %d, commit batch: %d",
		Name, sourceURLs, thUp, thDown, pullAhead, commitBatch)
	return ret
}

// LatestForwardSyncedTimestamp returns the timestamp of the current forward-sync target.
// Attachers with dependencies at or before this timestamp skip the depth cap.
// Returns zero LedgerTime when forward-sync is idle or nil.
func (s *Sync) LatestForwardSyncedTimestamp() base.LedgerTime {
	if s == nil {
		return base.LedgerTime{}
	}
	ticks := s.latestTargetTicks.Load()
	if ticks <= 0 {
		return base.LedgerTime{}
	}
	ret, _ := base.LedgerTimeFromTicksSinceGenesis(ticks)
	return ret
}

// IsSyncing returns true when forward-sync is actively catching up.
func (s *Sync) IsSyncing() bool {
	if s == nil {
		return false
	}
	return s.currentTarget.Load() > 0
}

// NotifyBranchCommitted wakes up the sync loop only when the current target branch commits.
func (s *Sync) NotifyBranchCommitted(branchSlot uint32) {
	if s == nil {
		return
	}
	if branchSlot < s.currentTarget.Load() {
		return
	}
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

// syncLoop runs until shutdown. Wakes up on branch commit notification or periodic timer.
func (s *Sync) syncLoop() {
	timer := time.NewTimer(syncLoopPeriod)
	defer timer.Stop()

	for {
		select {
		case <-s.Ctx().Done():
			return
		case <-s.wakeup:
		case <-timer.C:
		}
		s.syncTick()
		timer.Reset(syncLoopPeriod)
	}
}

// findAnchorBranch returns a branch ID K slots back from the latest committed healthy branch.
// This anchor is sent to sync sources so they can verify it's on their chain (fork detection).
// Returns zero ID if no suitable anchor is found.
func (s *Sync) findAnchorBranch() base.TransactionID {
	lrb := multistate.FindLatestReliableBranch(s.StateStore(), global.FractionHealthyBranch())
	if lrb == nil {
		s.Log().Warnf("[%s] findAnchorBranch: no reliable branch found", Name)
		return base.TransactionID{}
	}
	var anchor base.TransactionID
	n := 0
	multistate.IterateBranchChainBack(s.StateStore(), lrb, func(branchID *base.TransactionID, _ *multistate.BranchData) bool {
		anchor = *branchID
		n++
		return n < forkSafetyDepth
	})
	s.Log().Infof("[%s] findAnchorBranch: LRB slot=%d, walked back %d -> anchor slot=%d (%s)",
		Name, lrb.Slot(), n, anchor.Slot(), anchor.StringShort())
	return anchor
}

// requestBranchList tries each source starting from the current index, cycling on failure.
// Uses fork-safe after_branch mode: sends an anchor branch from our own chain.
// The source returns branches after that anchor, or error if it's on a different fork.
// On fork mismatch, tries the next source.
//
// Future improvement: query ALL sources, compute common prefix across their chains
// and our own, then sync from the fork point. This handles the case where all sources
// are on a different fork.
func (s *Sync) requestBranchList() ([]base.TransactionID, uint32, error) {
	anchor := s.findAnchorBranch()
	if anchor == (base.TransactionID{}) {
		return nil, 0, fmt.Errorf("no anchor branch found")
	}

	s.Log().Infof("[%s] requestBranchList: anchor=%s (slot %d)", Name, anchor.StringShort(), anchor.Slot())

	n := len(s.sources)
	var lastErr error
	for i := 0; i < n; i++ {
		idx := (s.sourceIdx + i) % n
		branches, lrbSlot, err := s.sources[idx].GetBranchListAfter(anchor, 100)
		if err != nil {
			lastErr = err
			s.Log().Warnf("[%s] source %d: %v, trying next", Name, idx, err)
			continue
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] source %d: returned 0 branches (source LRB=%d, our anchor slot=%d) — source is at or before our anchor",
				Name, idx, lrbSlot, anchor.Slot())
			continue
		}
		s.Log().Infof("[%s] source %d: returned %d branches (slots %d..%d, source LRB=%d)",
			Name, idx, len(branches), branches[0].Slot(), branches[len(branches)-1].Slot(), lrbSlot)
		s.sourceIdx = idx
		return branches, lrbSlot, nil
	}
	s.sourceIdx = (s.sourceIdx + 1) % n
	if lastErr != nil {
		return nil, 0, fmt.Errorf("all %d sync sources failed, last: %v", n, lastErr)
	}
	return nil, 0, nil
}

func (s *Sync) syncTick() {
	if s.Ctx().Err() != nil {
		return // shutting down — DB may already be closed
	}
	// peerSlot is the highest branch slot heard from peers (0 if none). The gap is
	// measured against it, not wall clock, so a cold restart (no peer branches yet)
	// does not trigger sync.
	peerSlot := s.LatestBranchSlotFromPeers()

	// find latest committed healthy slot
	healthySlot, found := multistate.FindLatestHealthySlot(s.StateStore(), global.FractionHealthyBranch())
	if !found {
		s.Log().Warnf("[%s] no healthy slot found", Name)
		return
	}

	// guard against uint32 underflow: peerSlot may be 0 (nothing heard) or, after a
	// lineage switch, behind our own healthy branch. Either way the gap is 0.
	var gap uint32
	if peerSlot > healthySlot {
		gap = peerSlot - healthySlot
	}
	s.Tracef("sync", "syncTick: peerSlot=%d, healthySlot=%d, gap=%d, syncing=%v, branchList=%d",
		peerSlot, healthySlot, gap, s.syncing, len(s.branchList))

	// hysteresis: go idle if gap is small enough
	if gap <= s.thresholdDown {
		if s.syncing {
			s.Log().Infof("[%s] caught up (gap=%d), going idle", Name, gap)
			s.syncing = false
			s.branchList = nil
			s.windowPulled = false
			s.stallCounter = 0
			s.latestTargetTicks.Store(0)
			s.currentTarget.Store(0)
		}
		return
	}

	// start syncing if gap exceeds threshold
	if !s.syncing {
		if gap >= s.thresholdUp {
			s.Log().Infof("[%s] starting forward-sync (gap=%d, healthy slot=%d, peer slot=%d)", Name, gap, healthySlot, peerSlot)
			s.syncing = true
			s.branchList = nil
		} else {
			return
		}
	}

	// request branch list if empty
	if len(s.branchList) == 0 {
		branches, lrbSlot, err := s.requestBranchList()
		if err != nil {
			s.Log().Warnf("[%s] %v", Name, err)
			s.syncStalled(gap, healthySlot)
			return
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] sync source returned empty branch list (LRB slot=%d)", Name, lrbSlot)
			s.syncStalled(gap, healthySlot)
			return
		}
		// filter out branches that are already committed locally
		newBranches := make([]base.TransactionID, 0, len(branches))
		for _, b := range branches {
			if s.Ctx().Err() != nil {
				return // shutting down — DB may already be closed
			}
			if _, committed := multistate.FetchRootRecord(s.StateStore(), b); !committed {
				newBranches = append(newBranches, b)
			}
		}
		s.Log().Infof("[%s] received %d branches from sync source (slots %d..%d, source LRB=%d), %d new",
			Name, len(branches), branches[0].Slot(), branches[len(branches)-1].Slot(), lrbSlot, len(newBranches))
		if len(newBranches) == 0 {
			s.Log().Warnf("[%s] all %d branches already committed locally — sync source (LRB=%d) is not ahead of us (healthy=%d)",
				Name, len(branches), lrbSlot, healthySlot)
			s.syncStalled(gap, healthySlot)
			return
		}
		s.branchList = newBranches
		s.windowPulled = false
	}

	s.stallCounter = 0 // got new branches, reset stall detection

	// force-commit branches in bounded batches with memory-pressure-based GC.
	// After each batch, check actual heap allocation against the configured memory limit.
	// This adapts to any hardware — no static assumptions about allocation rates or batch sizes.
	nCommitted := 0
	nAlreadyCommitted := 0
	for len(s.branchList) > 0 && nCommitted < s.commitBatch {
		branchID := s.branchList[0]
		// check if already committed before forcing
		_, wasAlreadyCommitted := multistate.FetchRootRecord(s.StateStore(), branchID)
		s.ForceCommitBranch(branchID)
		if _, ok := multistate.FetchRootRecord(s.StateStore(), branchID); !ok {
			s.Log().Infof("[%s] branch %s (slot %d) not yet ready, stopping batch", Name, branchID.StringShort(), branchID.Slot())
			break
		}
		if wasAlreadyCommitted {
			nAlreadyCommitted++
		}
		s.branchList = s.branchList[1:]
		nCommitted++
	}
	if nCommitted > 0 {
		s.stallCounter = 0 // progress made, reset stall detection
		s.MemoryPressureGC()
		s.Log().Infof("[%s] committed %d branches (%d new, %d already committed), %d remaining",
			Name, nCommitted, nCommitted-nAlreadyCommitted, nAlreadyCommitted, len(s.branchList))
		// branches were committed — reset window so next window is pulled
		s.windowPulled = false
	}

	if len(s.branchList) == 0 {
		// all branches committed, will re-request next tick if still behind
		return
	}

	// determine the window: up to pullAhead branches from the head of branchList
	windowEnd := s.pullAhead
	if windowEnd > len(s.branchList) {
		windowEnd = len(s.branchList)
	}
	window := s.branchList[:windowEnd]
	target := window[windowEnd-1]

	// set current target for NotifyBranchCommitted filtering and depth cap exemption
	s.currentTarget.Store(target.Slot())
	s.latestTargetTicks.Store(target.Timestamp().TicksSinceGenesis())

	if !s.windowPulled {
		// pull all branches in the window in ascending slot order.
		// Each pull triggers an attacher goroutine that recursively solidifies the past cone.
		// Parallel attachers overlap their network round-trips, dramatically speeding up sync.
		s.Log().Infof("[%s] pulling window of %d branches (slots %d..%d), %d remaining",
			Name, windowEnd, window[0].Slot(), target.Slot(), len(s.branchList)-1)
		for _, branchID := range window {
			s.AddPulledTransaction(branchID)
			if txBytes := s.TxBytesStore().GetTxBytes(&branchID); len(txBytes) > 0 {
				if _, err := s.TxBytesFromStoreIn(txBytes); err != nil {
					s.Log().Warnf("[%s] re-inject from txstore failed for %s: %v", Name, branchID.StringShort(), err)
				}
			} else {
				s.PullFromPeers(branchID)
			}
		}
		s.windowPulled = true
		s.lastPullTime = time.Now()
	} else if time.Since(s.lastPullTime) >= pullRepeatInterval {
		// re-pull the window if solidification is stalled
		for _, branchID := range window {
			s.PullFromPeers(branchID)
		}
		s.lastPullTime = time.Now()
	}
}

// syncStalled increments the stall counter and emits periodic ERROR warnings
// when the node cannot make sync progress because no source has newer branches.
func (s *Sync) syncStalled(gap, healthySlot uint32) {
	s.stallCounter++
	if s.stallCounter == stallWarningTicks || (s.stallCounter > stallWarningTicks && (s.stallCounter-stallWarningTicks)%stallWarningRepeat == 0) {
		s.Log().Errorf("[%s] SYNC STALLED: node is %d slots behind (healthy slot=%d) but no sync source "+
			"has newer branches. This usually means all configured sync sources are also behind or have their API disabled. "+
			"Configured sources: %v. Ensure at least one source points to a node that is fully synced and has API enabled",
			Name, gap, healthySlot, s.sourceURLs)
	}
}
