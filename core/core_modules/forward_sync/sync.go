// Package forward_sync implements forward-syncing: uncapped, in-order branch catch-up
// that hands off to recursive sync.
//
// Always-on background process (when enabled). Trigger (no hysteresis): forward sync
// runs exactly while a sync target is pending — i.e. while some attacher stopped at the
// recursion depth cap and added the branch it would not pull as a target (global.SyncTargetsPending).
// There is no "slots behind" threshold: the target set is a draining quantity, so a single
// level test cannot flap. A node at the tip never reaches the cap, so the set is empty there.
//
// Uncapped, hands off to recursion: forward sync has NO reach cap of its own. It drives the
// committed frontier up the lowest-slot target's own lineage, committing branches in order —
// each committed branch becomes rooted, which is precisely what lets the AGNOSTIC attacher
// (which knows nothing about forward sync — its only depth cap is a pure config constant)
// terminate its recursion on it. When a target commits it is retired from the set and the
// waiting attachers resume; when the set empties, forward sync goes idle, handing the
// remaining tail (within the cap) back to recursive sync / gossip. Because the target IS
// the branch recursion stopped at, the two waves meet on the same lineage and no gap opens.
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
	"github.com/lunfardo314/proxima/ledger"
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
	// reanchorBatchSlots: width (in slots) of the fork-detection probe window. The first probe asks
	// for the target's lineage from (own LRB slot − batch); each older probe steps back by 2 batches
	// (an overlapping window so the fork point can't fall into a boundary gap). Kept ≤ the server
	// response cap so a probe window is returned whole.
	reanchorBatchSlots = 100
	// defaultMaxSyncSlotsBehind: refuse to forward-sync if the latest committed (precheck) or the
	// latest common (after probing) branch is more than this many slots behind the current slot.
	// This is a node-local catch-up policy (config sync.max_slots_behind), NOT a ledger constant —
	// it bounds how heavy a forward build to attempt before preferring a fresh snapshot. The default
	// matches half the branch txID retention (claude/txid_ttl_tiered.md), the depth to which branch
	// baselines remain resolvable.
	defaultMaxSyncSlotsBehind = 8740
	// stallWarningTicks: after this many consecutive sync ticks where no source is ahead,
	// emit an ERROR-level warning. At 1s per tick this is ~30 seconds.
	stallWarningTicks = 30
	// stallWarningRepeat: repeat the stall warning every N ticks (~60 seconds)
	stallWarningRepeat = 60
	// canonicalCheckInterval throttles the "is the LRB on the canonical lineage" probe, which runs in
	// its own monitor goroutine (kept off the catch-up loop so a slow source can't stall catch-up).
	canonicalCheckInterval = 5 * time.Second
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
		driveTarget   base.TransactionID // the sync target branch currently driven toward (for adopt-change logging)
		syncedToSlot  uint32             // highest slot committed on driveTarget's lineage so far (the from_slot floor); set by the fork probe on adopt, advanced as we commit
		refused       bool               // true after a refuse decision for the current driveTarget (avoids re-probing/log spam)
		currentTarget atomic.Uint32      // slot of the branch we're waiting for
		windowPulled  bool          // true when all branches in the current window have been pulled
		lastPullTime  time.Time     // when the current window was last pulled
		wakeup        chan struct{} // signaled when the target branch commits
		stallCounter  int          // consecutive sync ticks where no source was ahead
		// onCanonicalLineage: true iff the node's committed LRB was last found on a source's canonical
		// lineage (refreshCanonicalLineage, in its own monitor goroutine). Read by the sequencer start
		// gate via OnCanonicalLineage() so the sequencer never builds on a fork; NOT re-derived by the
		// sequencer (no extra source traffic). Fail-open: cleared ONLY on a positively detected fork.
		onCanonicalLineage atomic.Bool
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
// Activation is governed by whether the `sources` list is populated (after self-filtering): with
// at least one source the module runs (forward-sync catch-up + fork detection); with none it is
// DISABLED and returns nil, and the node relies on recursive solidification alone (the larger
// attachment depth cap). There is no separate on/off flag. `sources` is shared with snapshot
// acquisition (see snapshot_restore.tryDownloadRemoteSnapshot) — the single list of trusted node
// API endpoints, owned by neither subsystem.
func Start(env environment) *Sync {
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

	if len(sourceURLs) == 0 {
		// No usable 'sources' → forward sync DISABLED (nil module). Catch-up relies on recursive
		// solidification only (attachers use the larger depth cap) and there is NO fork detection:
		// OnCanonicalLineage() reads true for a nil module, so the sequencer start gate falls back to
		// plain IsSynced(). If the local state is farther behind than recursion can bridge, an attacher
		// hitting the depth cap graceful-shuts-down rather than silently wedging. This is the normal
		// bootstrap / standalone configuration. See claude/fork_detection_recovery.md.
		env.Log().Warnf("[%s] DISABLED: no 'sources' configured — no forward-sync catch-up and NO fork "+
			"detection. Catch-up relies on recursive solidification only; if local state is too far behind, "+
			"the node will shut down and require 'sources' or a fresher snapshot.", Name)
		return nil
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
		environment: env,
		sources:     sources,
		sourceURLs:  sourceURLs,
		pullAhead:   pullAhead,
		commitBatch: commitBatch,
		wakeup:      make(chan struct{}, 1),
	}
	ret.onCanonicalLineage.Store(true) // fail-open until the first canonical probe runs

	go ret.syncLoop()
	go ret.canonicalMonitorLoop()

	env.Log().Infof("[%s] started, sources: %v, pull ahead: %d, commit batch: %d (trigger: attacher at depth cap)",
		Name, sourceURLs, pullAhead, commitBatch)
	return ret
}

// IsSyncing returns true when forward-sync is actively catching up.
func (s *Sync) IsSyncing() bool {
	if s == nil {
		return false
	}
	return s.currentTarget.Load() > 0
}

// OnCanonicalLineage reports whether the node's committed LRB was last found on a source's canonical
// lineage. Nil receiver (forward sync disabled) → true: with no sync there is no determination, so the
// sequencer gate does not block on this. See claude/fork_detection_recovery.md §1, §3.
func (s *Sync) OnCanonicalLineage() bool {
	if s == nil {
		return true
	}
	return s.onCanonicalLineage.Load()
}

// canonicalMonitorLoop periodically probes whether the committed LRB is on the canonical lineage,
// independent of the catch-up loop so a slow source cannot stall catch-up. Runs once promptly, then
// every canonicalCheckInterval.
func (s *Sync) canonicalMonitorLoop() {
	s.refreshCanonicalLineage()
	timer := time.NewTimer(canonicalCheckInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.Ctx().Done():
			return
		case <-timer.C:
			s.refreshCanonicalLineage()
			timer.Reset(canonicalCheckInterval)
		}
	}
}

// refreshCanonicalLineage updates onCanonicalLineage by asking sources whether the committed LRB txid
// appears in their canonical branch chain at its slot. Fail-open: the flag is cleared ONLY when a source
// that is committed AHEAD of us returns a canonical chain that does NOT contain our LRB (a positively
// detected fork). Indeterminate results — no committed reliable branch (genesis), no reachable source,
// or every reachable source at/behind our LRB (we are at the tip) — leave the flag TRUE, so a healthy
// tip / genesis / bootstrap node is never spuriously blocked. Iterates sources in order (no shared
// index with the catch-up loop) so it is safe to run concurrently.
func (s *Sync) refreshCanonicalLineage() {
	if len(s.sources) == 0 {
		return // nothing to check against; stay fail-open
	}
	localLRB := multistate.FindLatestReliableBranch(s.StateStore(), global.FractionHealthyBranch())
	if localLRB == nil {
		s.onCanonicalLineage.Store(true) // nothing committed yet — nothing to fork
		return
	}
	localID := localLRB.TxID()
	localSlot := localLRB.Slot()

	for idx := range s.sources {
		_, srcLRBID, err := s.sources[idx].GetLatestReliableBranch()
		if err != nil {
			continue // try next source
		}
		if srcLRBID.Slot() <= localSlot {
			// source is not ahead of us — we are at/ahead of its committed tip, cannot detect a fork
			// against it. Fail-open.
			s.onCanonicalLineage.Store(true)
			return
		}
		chain, _, err := s.sources[idx].GetBranchChainTo(srcLRBID, saturatingSub(localSlot, 1))
		if err != nil {
			continue // try next source
		}
		onCanonical := false
		for _, b := range chain {
			if b == localID {
				onCanonical = true
				break
			}
		}
		s.onCanonicalLineage.Store(onCanonical)
		if !onCanonical {
			// Proactively drive recovery: register the source's canonical LRB as a sync target so the
			// catch-up loop (syncTick) re-anchors from the common ancestor and commits the canonical
			// lineage forward — overtaking the fork, which stays frozen because the sequencer is gated
			// off (OnCanonicalLineage()==false) — without waiting for an attacher to stall at the depth
			// cap. If the common ancestor is unreachable within the horizon, findCommonStartSlot refuses
			// and surfaces "restore from a younger snapshot". A node that is merely behind ON canonical
			// reads onCanonical==true here, so this fires only for a genuine fork.
			if global.AddSyncTarget(srcLRBID) {
				s.Log().Warnf("[%s] committed LRB %s (slot %d) is NOT on the canonical lineage of source %s "+
					"(source LRB slot %d): node is on a FORK — driving re-anchor toward canonical; sequencer held off until re-rooted",
					Name, localID.StringShort(), localSlot, s.sourceURLs[idx], srcLRBID.Slot())
			}
		}
		return
	}
	// no source reachable — leave the flag unchanged (fail-open)
}

// configuredSourceClients builds API clients for the configured `sources`, excluding this node's own
// API. Shared shape with Start; used by the startup fork-reachability check (which runs before the
// module is constructed).
func configuredSourceClients() []*client.APIClient {
	urls := viper.GetStringSlice("sources")
	selfURLs := localSelfURLs(viper.GetInt("api.port"))
	ret := make([]*client.APIClient, 0, len(urls))
	for _, url := range urls {
		self := false
		for s := range selfURLs {
			if strings.HasPrefix(url, s) {
				self = true
				break
			}
		}
		if !self {
			ret = append(ret, client.NewWithGoogleDNS(url, 10*time.Second))
		}
	}
	return ret
}

// StartupForkReachable reports whether the DB's committed state shares a branch with a source's
// canonical lineage that is still within the sync horizon — i.e. a fork, if any, can be re-anchored in
// place at runtime (§2a) rather than requiring a fresh snapshot. Called by the startup DB-state decision
// (snapshot_restore) BEFORE the module is constructed, so it is a package-level function that builds its
// own source clients and walks canonical windows back (reusing findCommonStartSlot's logic), checking
// local commitment via the read-only store.
//
// Returns TRUE when the situation is indeterminate — empty DB, no reachable source, or no source ahead
// of us — so the DB is never replaced on a hunch. Returns FALSE only when a source that is committed
// ahead of us has a canonical lineage in which NONE of our committed branches appears within the horizon:
// an UNREACHABLE fork (e.g. the restored snapshot itself was on a fork, or a long-running forked node
// pruned past the fork point). See claude/fork_detection_recovery.md §2b.
//
// MUST be singleton-free: it runs at the pre-init startup stage where the ledger singleton is not yet
// initialized. Hence it takes the local committed slot as a parameter (the caller reads it via
// FetchLatestCommittedSlot, which does not parse branch data), and its only local reads are
// FetchRootRecord membership checks (root metadata, no output/lock parsing). It must NOT call anything
// that ends up in ledger.L() — e.g. FindLatestReliableBranch/FetchBranchData parse output locks and panic.
func StartupForkReachable(store global.StoreReader, localCommittedSlot uint32, log global.Logging) bool {
	if localCommittedSlot == 0 {
		return true // nothing committed — nothing to fork
	}
	for _, c := range configuredSourceClients() {
		_, srcLRBID, err := c.GetLatestReliableBranch()
		if err != nil {
			continue
		}
		if srcLRBID.Slot() <= localCommittedSlot {
			return true // source not ahead of us — cannot detect a fork against it
		}
		currentSlot := srcLRBID.Slot()
		toBranch := srcLRBID
		fromSlot := saturatingSub(localCommittedSlot, reanchorBatchSlots)
		for {
			branches, _, err := c.GetBranchChainTo(toBranch, fromSlot)
			if err != nil || len(branches) == 0 {
				break // this source failed mid-walk — try the next
			}
			for i := len(branches) - 1; i >= 0; i-- {
				if _, committed := multistate.FetchRootRecord(store, branches[i]); committed {
					return true // a locally-committed branch is on the canonical lineage → reachable
				}
			}
			oldest := branches[0]
			if currentSlot-oldest.Slot() > maxSyncSlotsBehind() {
				log.Log().Warnf("[%s] startup fork check: no committed branch on the canonical lineage within %d slots — DB is on an UNREACHABLE fork",
					Name, maxSyncSlotsBehind())
				return false
			}
			if oldest == toBranch {
				log.Log().Warnf("[%s] startup fork check: canonical lineage bottomed out at %s (slot %d) with no locally-committed branch — DB is on an UNREACHABLE fork",
					Name, oldest.StringShort(), oldest.Slot())
				return false
			}
			toBranch = oldest
			fromSlot = saturatingSub(oldest.Slot(), 2*reanchorBatchSlots)
		}
	}
	return true // no source usable — keep the DB (fail-safe)
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

// requestChain asks sources for the back-chain of toBranch (oldest-first, slot > fromSlot, server-capped),
// trying each source and cycling on failure. A source that lacks toBranch is on a different fork — skip it.
func (s *Sync) requestChain(toBranch base.TransactionID, fromSlot uint32) ([]base.TransactionID, error) {
	if len(s.sources) == 0 {
		return nil, fmt.Errorf("no sync sources configured")
	}
	n := len(s.sources)
	var lastErr error
	for i := 0; i < n; i++ {
		idx := (s.sourceIdx + i) % n
		branches, _, err := s.sources[idx].GetBranchChainTo(toBranch, fromSlot)
		if err != nil {
			lastErr = err
			s.Log().Warnf("[%s] source %d: %v, trying next", Name, idx, err)
			continue
		}
		if len(branches) == 0 {
			continue
		}
		s.sourceIdx = idx
		return branches, nil
	}
	s.sourceIdx = (s.sourceIdx + 1) % n
	if lastErr != nil {
		return nil, fmt.Errorf("all %d sync sources failed, last: %v", n, lastErr)
	}
	return nil, nil
}

// findCommonStartSlot probes sources for the latest branch on the target's lineage that is committed
// locally — the fork-safe slot from which forward sync starts committing. It walks older overlapping
// windows until one contains a locally-committed branch (guaranteed: lineages share genesis/snapshot).
// Returns (commonSlot, true), or false on refuse — node too far behind / no common within the horizon.
func (s *Sync) findCommonStartSlot(target base.TransactionID, lrbSlot, currentSlot uint32) (uint32, bool) {
	toBranch := target
	fromSlot := saturatingSub(lrbSlot, reanchorBatchSlots)
	for {
		if s.Ctx().Err() != nil {
			return 0, false
		}
		branches, err := s.requestChain(toBranch, fromSlot)
		if err != nil || len(branches) == 0 {
			if err != nil {
				s.Log().Warnf("[%s] fork probe for %s: %v", Name, toBranch.StringShort(), err)
			}
			return 0, false
		}
		// branches are oldest-first; scan from the newest down for the first committed locally
		for i := len(branches) - 1; i >= 0; i-- {
			if _, committed := multistate.FetchRootRecord(s.StateStore(), branches[i]); committed {
				common := branches[i].Slot()
				if currentSlot-common > maxSyncSlotsBehind() {
					s.refuseSync(currentSlot-common, "latest common branch")
					return 0, false
				}
				return common, true
			}
		}
		// no common branch in this window — step to an older, overlapping one
		oldest := branches[0]
		if currentSlot-oldest.Slot() > maxSyncSlotsBehind() {
			s.refuseSync(currentSlot-oldest.Slot(), "no common branch within the sync horizon")
			return 0, false
		}
		if oldest == toBranch {
			// The source's lineage bottoms out here: GetBranchChainTo(toBranch) returned toBranch as its
			// own oldest (can't go older), yet no returned branch is committed locally. The shared
			// ancestor is therefore at or below our snapshot floor, which is always committed — start
			// there. Defensive fallback: get_branch_list now includes the from_slot/snapshot-anchor
			// branch, so the scan above normally finds the common ancestor directly; but a source on an
			// older binary may omit it, which without this guard would spin the probe forever on the
			// oldest real branch (gap below the refuse horizon, so it never exits).
			earliest := multistate.FetchEarliestSlot(s.StateStore())
			s.Log().Infof("[%s] fork probe bottomed out at %s (slot %d), none committed locally; using snapshot floor slot %d as common start",
				Name, oldest.StringShort(), oldest.Slot(), earliest)
			return earliest, true
		}
		toBranch = oldest
		fromSlot = saturatingSub(oldest.Slot(), 2*reanchorBatchSlots)
	}
}

// maxSyncSlotsBehind returns the configured catch-up horizon (sync.max_slots_behind), or the
// default when unset/zero. Read fresh so config reloads take effect.
func maxSyncSlotsBehind() uint32 {
	if v := viper.GetInt("sync.max_slots_behind"); v > 0 {
		return uint32(v)
	}
	return defaultMaxSyncSlotsBehind
}

// refuseSync logs the refuse decision once per target. "Refuse" surfaces the situation to the operator;
// the automatic fall-back to a younger snapshot is handled by the startup path (sync_semantics.md §5).
func (s *Sync) refuseSync(slotsBehind uint32, what string) {
	if s.refused {
		return
	}
	s.refused = true
	s.Log().Errorf("[%s] REFUSING TO SYNC: %s is %d slots behind (> %d sync horizon). Local state is "+
		"too old to forward-sync onto the live lineage; restore from a younger snapshot.", Name, what, slotsBehind, maxSyncSlotsBehind())
}

func saturatingSub(a, b uint32) uint32 {
	if a < b {
		return 0
	}
	return a - b
}

func (s *Sync) syncTick() {
	if s.Ctx().Err() != nil {
		return // shutting down — DB may already be closed
	}

	// Trigger (no hysteresis): forward sync runs exactly while a sync target is pending — i.e.
	// while some attacher stopped at the depth cap. There is no "slots behind" threshold; the
	// target set is a draining quantity, so a single level test cannot flap. See claude/sync_semantics.md.
	if !global.SyncTargetsPending() {
		if s.syncing {
			s.Log().Infof("[%s] no sync targets pending — going idle", Name)
			s.syncing = false
			s.branchList = nil
			s.windowPulled = false
			s.stallCounter = 0
			s.driveTarget = base.TransactionID{}
			s.syncedToSlot = 0
			s.refused = false
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
	s.Tracef("sync", "syncTick: peerSlot=%d, healthySlot=%d, gap=%d, syncing=%v, branchList=%d",
		peerSlot, healthySlot, gap, s.syncing, len(s.branchList))

	if !s.syncing {
		s.Log().Infof("[%s] starting forward-sync (healthy slot=%d, peer slot=%d, gap=%d)",
			Name, healthySlot, peerSlot, gap)
		s.syncing = true
		s.branchList = nil
	}

	// the target forward sync drives toward: the lowest-slot pending sync target (a branch some
	// attacher stopped at). We reached here only because the set is non-empty (trigger above).
	target, ok := global.LowestSyncTarget()
	if !ok {
		return // drained between the trigger check and here
	}

	// reached the target: it is committed (by this loop's earlier commits, or by recursion).
	// Retire it and re-evaluate next tick with the remaining targets — this is the handoff.
	if _, committed := multistate.FetchRootRecord(s.StateStore(), target); committed {
		if global.RemoveSyncTarget(target) {
			s.Log().Infof("[%s] reached target %s (slot %d) — committed, handing off to recursive sync", Name, target.StringShort(), target.Slot())
		}
		s.branchList = nil
		s.driveTarget = base.TransactionID{}
		return
	}

	// On adopting a new target: refuse-precheck, then probe for the latest common branch — the
	// fork-safe slot from which to start committing (our own latest branch on the target's lineage).
	// syncedToSlot then advances as we commit; no re-probe until the target changes.
	if target != s.driveTarget {
		s.driveTarget = target
		s.branchList = nil
		s.refused = false
		currentSlot := ledger.TimeNow().Slot
		if currentSlot-healthySlot > maxSyncSlotsBehind() {
			s.refuseSync(currentSlot-healthySlot, "latest committed branch")
			return
		}
		common, ok := s.findCommonStartSlot(target, healthySlot, currentSlot)
		if !ok {
			s.syncStalled(gap, healthySlot)
			return
		}
		s.syncedToSlot = common
		s.Log().Infof("[%s] adopting target %s (slot %d): common start slot %d, %d branches to commit",
			Name, target.StringShort(), target.Slot(), common, saturatingSub(target.Slot(), common))
	}
	if s.refused {
		return // refused for this target; await operator / snapshot fallback
	}

	if len(s.branchList) == 0 {
		branches, err := s.requestChain(target, s.syncedToSlot)
		if err != nil {
			s.Log().Warnf("[%s] %v", Name, err)
			s.syncStalled(gap, healthySlot)
			return
		}
		// filter out branches already committed locally; advance the floor past them
		newBranches := make([]base.TransactionID, 0, len(branches))
		for _, b := range branches {
			if s.Ctx().Err() != nil {
				return // shutting down — DB may already be closed
			}
			if _, committed := multistate.FetchRootRecord(s.StateStore(), b); committed {
				if b.Slot() > s.syncedToSlot {
					s.syncedToSlot = b.Slot()
				}
			} else {
				newBranches = append(newBranches, b)
			}
		}
		if len(newBranches) == 0 {
			if len(branches) == 0 {
				s.syncStalled(gap, healthySlot) // source has nothing above our floor
			}
			return // else floor advanced past already-committed branches; re-request next tick
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
		if branchID.Slot() > s.syncedToSlot {
			s.syncedToSlot = branchID.Slot()
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
	windowTop := window[windowEnd-1]

	// set current target so NotifyBranchCommitted only wakes the loop on the awaited branch
	s.currentTarget.Store(windowTop.Slot())

	if !s.windowPulled {
		// pull all branches in the window in ascending slot order.
		// Each pull triggers an attacher goroutine that recursively solidifies the past cone.
		// Parallel attachers overlap their network round-trips, dramatically speeding up sync.
		s.Log().Infof("[%s] pulling window of %d branches (slots %d..%d), %d remaining",
			Name, windowEnd, window[0].Slot(), windowTop.Slot(), len(s.branchList)-1)
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
