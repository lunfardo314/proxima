// Package forward_sync implements forward-syncing: uncapped, in-order branch catch-up
// that hands off to recursive sync.
//
// Always-on background process (when enabled). Trigger (no hysteresis): forward sync
// runs exactly while at least one attacher is poll-only at the recursion depth cap —
// i.e. while the global sync-mode counter (global.NumAttachersAtMaxDepth) is non-zero.
// There is no "slots behind" threshold: per-branch attachment depth (sync_semantics.md
// §2.1) makes the at-cap count a monotone, draining quantity, so a single level test
// cannot flap. A node at the tip never reaches the cap, so forward sync stays idle there.
//
// Uncapped, hands off to recursion (sync_semantics.md §3): forward sync has NO reach cap
// of its own. It commits branches forward, in order, from the committed state upward —
// each committed branch becomes rooted, which is precisely what lets the AGNOSTIC
// attacher (which knows nothing about forward sync — its only depth cap is a pure config
// constant) terminate its recursion on it. Forward sync keeps going until it reaches the
// frontier where recursive sync stopped: there the waiting attachers un-cap, the sync-mode
// counter returns to zero, and forward sync goes idle — handing the remaining tail back to
// recursive sync / gossip. Because the stopping point IS the recursion frontier (not a
// fixed window), no gap can open between the two (the 2026-06-20 restore dead zone).
//
// Pull parallelism (NOT a reach cap): forward sync pulls up to pull_ahead branches in
// parallel (ascending slot order) so their attachers overlap network round-trips, and
// commits up to commit_batch per tick. These bound only per-tick throughput and pull
// concurrency — never how far forward sync may ultimately reach, which is governed solely
// by the handoff above.
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

	defaultPullAhead   = 5
	defaultCommitBatch = 10
	syncLoopPeriod     = time.Second
	pullRepeatInterval = 5 * time.Second
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
		sources     []*client.APIClient
		sourceURLs  []string // original URLs for diagnostics
		sourceIdx   int      // current source index, cycles on failure
		pullAhead   int      // window size: pull this many branches ahead in parallel
		commitBatch int      // max branches to commit per sync tick
		syncing     bool
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

// Start initializes and starts the sync module.
//
// Activation is governed solely by the authoritative `sync.disable` flag (default: enabled), NOT
// by whether the source list is populated: when enabled the module ALWAYS runs. The shared
// top-level `sources` list only determines whether the running module can actually pull; with no
// usable sources it runs idle while at the tip and, only if the node falls behind, emits a
// gap-gated SYNC STALLED error (see syncStalled / syncTick) — so the empty-list-silently-disables
// footgun is gone without false-alarming bootstrap/standalone/tip nodes. `sources` is shared with
// snapshot acquisition (see snapshot_restore.tryDownloadRemoteSnapshot) — it is the single list
// of trusted node API endpoints, owned by neither subsystem.
func Start(env environment) *Sync {
	if viper.GetBool("sync.disable") {
		env.Log().Infof("[%s] sync.disable=true, forward-sync inactive", Name)
		return nil
	}

	sourceURLs := viper.GetStringSlice("sources")

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

	// NB: an empty source list does NOT disable the module. Activation is governed solely by
	// sync.disable (checked above). With no usable sources the module still runs but stays idle
	// while the node is at the tip; if the node falls behind, syncStalled emits a loud, gap-gated
	// ERROR telling the operator to configure 'sources'. This keeps the flag authoritative and
	// avoids both the silent-off footgun and false alarms on bootstrap/standalone/tip nodes.

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
		environment: env,
		sources:     sources,
		sourceURLs:  sourceURLs,
		pullAhead:   pullAhead,
		commitBatch: commitBatch,
		wakeup:      make(chan struct{}, 1),
	}

	go ret.syncLoop()

	if len(sourceURLs) == 0 {
		env.Log().Infof("[%s] started, ENABLED but no 'sources' configured: idle while at the tip; "+
			"if this node falls behind it will report SYNC STALLED until 'sources' is set to trusted synced node API URLs",
			Name)
	} else {
		env.Log().Infof("[%s] started, sources: %v, pull ahead: %d, commit batch: %d (trigger: attacher at depth cap)",
			Name, sourceURLs, pullAhead, commitBatch)
	}
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

// requestBranchChain asks sources for the back-chain of the SPECIFIC branch the node's
// stuck recursive attacher needs (target = the lowest-slot needed branch, on the gossiped
// tip's own lineage), down to fromSlot (our committed frontier). The source walks back from
// `target` ITSELF (to_branch mode), not from its own LRB, so the returned chain is guaranteed
// on target's lineage — this is what stitches the forward (commit) and recursive (pull) waves
// onto the same lineage at the seam instead of letting them pass each other. Tries each source,
// cycling on failure: a source that does not know `target` is on a different fork — skip it.
func (s *Sync) requestBranchChain(target base.TransactionID, fromSlot uint32) ([]base.TransactionID, uint32, error) {
	if len(s.sources) == 0 {
		// defensive: syncTick already short-circuits the no-sources case; guard the
		// source-index modulo below against division by zero regardless.
		return nil, 0, fmt.Errorf("no sync sources configured")
	}
	s.Log().Infof("[%s] requesting lineage of needed branch %s (slot %d) down to frontier slot %d",
		Name, target.StringShort(), target.Slot(), fromSlot)

	n := len(s.sources)
	var lastErr error
	for i := 0; i < n; i++ {
		idx := (s.sourceIdx + i) % n
		branches, topSlot, err := s.sources[idx].GetBranchChainTo(target, fromSlot, 100)
		if err != nil {
			lastErr = err
			s.Log().Warnf("[%s] source %d: %v, trying next", Name, idx, err)
			continue
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] source %d: returned 0 branches for target %s (frontier slot=%d) — nothing below target to fill",
				Name, idx, target.StringShort(), fromSlot)
			continue
		}
		s.Log().Infof("[%s] source %d: returned %d branches (slots %d..%d) on the needed branch's lineage",
			Name, idx, len(branches), branches[0].Slot(), branches[len(branches)-1].Slot())
		s.sourceIdx = idx
		return branches, topSlot, nil
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

	// Trigger (no hysteresis): forward sync runs exactly while at least one attacher is
	// poll-only at the depth cap (global sync-mode counter). There is no "slots behind"
	// threshold. Per-branch depth makes the at-cap count a monotone, draining quantity,
	// so a single level test cannot flap. See claude/sync_semantics.md §3-§4.
	if global.NumAttachersAtMaxDepth() == 0 {
		if s.syncing {
			s.Log().Infof("[%s] no attacher at the depth cap — going idle", Name)
			s.syncing = false
			s.branchList = nil
			s.windowPulled = false
			s.stallCounter = 0
			s.currentTarget.Store(0)
		}
		return
	}

	// In sync mode. healthySlot is the committed-frontier bound for the branch-chain
	// request (the floor below which we already have state); peerSlot/gap are for stall
	// detection and logging — NONE of them is the trigger or the sync target.
	peerSlot := s.LatestBranchSlotFromPeers()
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
	s.Tracef("sync", "syncTick: atCap=%d, peerSlot=%d, healthySlot=%d, gap=%d, syncing=%v, branchList=%d",
		global.NumAttachersAtMaxDepth(), peerSlot, healthySlot, gap, s.syncing, len(s.branchList))

	if !s.syncing {
		s.Log().Infof("[%s] starting forward-sync (%d attacher(s) at depth cap, healthy slot=%d, peer slot=%d, gap=%d)",
			Name, global.NumAttachersAtMaxDepth(), healthySlot, peerSlot, gap)
		s.syncing = true
		s.branchList = nil
	}

	// No usable sources: the module is enabled (sync.disable=false) but cannot pull. Surface this
	// via the gap-gated syncStalled ERROR only — not a per-tick warning — so a behind node loudly
	// tells the operator to set 'sources' while a tip node (never reaching here) stays silent.
	// Also avoids the requestBranchChain source-index modulo-by-zero.
	if len(s.sources) == 0 {
		s.syncStalled(gap, healthySlot)
		return
	}

	// request the needed branch's lineage if our branch list is empty. The target is the
	// lowest-slot branch any capped attacher needs (global registry) — NOT the LRB. We
	// reached here only because the registry is non-empty (trigger above), so a target exists.
	if len(s.branchList) == 0 {
		target, ok := global.LowestNeededBranch()
		if !ok {
			// registry drained between the trigger check and here — handoff complete, idle next tick
			return
		}
		branches, topSlot, err := s.requestBranchChain(target, healthySlot)
		if err != nil {
			s.Log().Warnf("[%s] %v", Name, err)
			s.syncStalled(gap, healthySlot)
			return
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] sync source returned empty branch list for needed branch %s", Name, target.StringShort())
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
		s.Log().Infof("[%s] received %d branches on needed branch %s lineage (slots %d..%d, top=%d), %d new",
			Name, len(branches), target.StringShort(), branches[0].Slot(), branches[len(branches)-1].Slot(), topSlot, len(newBranches))
		if len(newBranches) == 0 {
			s.Log().Warnf("[%s] all %d branches on needed branch %s lineage already committed locally (frontier=%d) — yet the attacher is still capped; will retry",
				Name, len(branches), target.StringShort(), healthySlot)
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

	// set current target so NotifyBranchCommitted only wakes the loop on the awaited branch
	s.currentTarget.Store(target.Slot())

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
		if len(s.sources) == 0 {
			s.Log().Errorf("[%s] SYNC STALLED: node is %d slots behind (healthy slot=%d) and NO sync 'sources' are "+
				"configured. Forward-sync is enabled (sync.disable=false) but cannot catch up without a source. Set the "+
				"top-level 'sources' list to at least one fully-synced node API URL.", Name, gap, healthySlot)
			return
		}
		s.Log().Errorf("[%s] SYNC STALLED: node is %d slots behind (healthy slot=%d) but no sync source "+
			"has newer branches. This usually means all configured sync sources are also behind or have their API disabled. "+
			"Configured sources: %v. Ensure at least one source points to a node that is fully synced and has API enabled",
			Name, gap, healthySlot, s.sourceURLs)
	}
}
