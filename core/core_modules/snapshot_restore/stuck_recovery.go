package snapshot_restore

import (
	"time"

	"github.com/lunfardo314/proxima/api/client"
	syncmod "github.com/lunfardo314/proxima/core/core_modules/forward_sync"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/spf13/viper"
)

// Stuck-state recovery ("too old / divergent lineage" orchestration).
//
// A node whose committed state is too far behind the network and NOT advancing is
// beyond what recursive sync can bridge (gap > the attachment depth cap) and, in the
// worst case, sits on a lineage the network has abandoned (a fork) — which neither
// recursive nor forward sync can heal (sync_semantics.md §2.1, §6). The only recovery
// is to adopt a closer baseline: download a fresh snapshot from a synced node and
// replace the local state.
//
// This reuses the existing restore machinery: mark a restore in progress with an empty
// snapshot file, set CleanupRequestedFlag, and Stop(). main.go self-restarts; on
// startup CheckAndRestoreOnStartup sees cleanup-in-progress with no local file and
// downloads a fresh remote snapshot from the `sources` list, replacing the DB.
//
// Opt-in and conservative: it runs only when snapshot_restore.recover_when_stuck_slots
// is set > 0, and triggers only when the node is (a) not synced, (b) more than that
// many slots behind, (c) NOT advancing for stuckRecoveryCycles consecutive checks, and
// (d) a reachable source actually has a newer snapshot. A healthy/catching-up node
// never trips it.

const stuckRecoveryCycles = 3 // consecutive non-advancing checks (× checkPeriod) before triggering

type stuckEnvironment interface {
	global.NodeGlobal
	IsSynced() bool
	GetLatestReliableBranch() *multistate.BranchData
}

// StartStuckRecovery launches the stuck-state recovery watchdog. No-op unless
// snapshot_restore.recover_when_stuck_slots > 0. Independent of snapshot_restore.enable
// (the periodic compaction) and of sync.disable (forward sync) — this is a recovery
// safety net, not maintenance.
func StartStuckRecovery(env stuckEnvironment) {
	thresholdSlots := uint32(viper.GetInt("snapshot_restore.recover_when_stuck_slots"))
	if thresholdSlots == 0 {
		env.Log().Infof("[%s] stuck-state recovery disabled (snapshot_restore.recover_when_stuck_slots=0)", Name)
		return
	}
	env.Log().Infof("[%s] stuck-state recovery enabled: replace state from a fresh remote snapshot when "+
		"not synced and >%d slots behind without progress for %d×%v", Name, thresholdSlots, stuckRecoveryCycles, checkPeriod)

	var lastLRBSlot uint32
	stuckCount := 0
	triggered := false

	env.RepeatInBackground(Name+"_stuck_recovery", checkPeriod, func() bool {
		if triggered {
			return true
		}
		lrb := env.GetLatestReliableBranch()
		if lrb == nil {
			return true
		}
		lrbSlot := lrb.Slot()
		currentSlot := ledger.TimeNow().Slot

		// not far behind, or synced, or no clock yet → not stuck; reset.
		if env.IsSynced() || currentSlot <= lrbSlot || currentSlot-lrbSlot < thresholdSlots {
			stuckCount = 0
			lastLRBSlot = lrbSlot
			return true
		}
		// far behind but the LRB is still advancing → catching up, not stuck; reset.
		if lrbSlot > lastLRBSlot {
			stuckCount = 0
			lastLRBSlot = lrbSlot
			return true
		}
		// far behind AND not advancing
		stuckCount++
		env.Log().Warnf("[%s] STUCK: LRB slot %d is %d behind (current %d), not advancing for %d/%d checks",
			Name, lrbSlot, currentSlot-lrbSlot, currentSlot, stuckCount, stuckRecoveryCycles)
		lastLRBSlot = lrbSlot
		if stuckCount < stuckRecoveryCycles {
			return true
		}
		// confirmed stuck. Only nuke the DB if a synced source actually has a newer snapshot.
		if !remoteHasNewerSnapshot(env, lrbSlot) {
			env.Log().Warnf("[%s] stuck far behind but no source has a snapshot newer than slot %d — "+
				"staying put; operator intervention needed", Name, lrbSlot)
			stuckCount = 0 // re-evaluate from scratch
			return true
		}
		triggered = true
		triggerStuckRecovery(env, lrbSlot, currentSlot)
		return true
	}, true)
}

// remoteHasNewerSnapshot reports whether any reachable, non-self source advertises a
// snapshot at least minSnapshotAgeSlots newer than the node's stuck LRB slot. Mirrors
// the source query in tryDownloadRemoteSnapshot but only checks availability.
func remoteHasNewerSnapshot(env stuckEnvironment, lrbSlot uint32) bool {
	sourceURLs := viper.GetStringSlice("sources")
	selfAPIPort := viper.GetInt("api.port")
	for _, url := range sourceURLs {
		if syncmod.IsSelfURL(url, selfAPIPort) {
			continue
		}
		c := client.NewWithGoogleDNS(url, 15*time.Second)
		info, err := c.GetSnapshotInfo()
		if err != nil {
			continue
		}
		if info.Slot >= lrbSlot+minSnapshotAgeSlots {
			env.Log().Infof("[%s] source %s has snapshot at slot %d (> stuck LRB %d) — eligible for recovery",
				Name, url, info.Slot, lrbSlot)
			return true
		}
	}
	return false
}

// triggerStuckRecovery marks a force-remote restore and requests restart. On the next
// startup CheckAndRestoreOnStartup downloads a fresh snapshot from sources (empty local
// file) and replaces the DB.
func triggerStuckRecovery(env stuckEnvironment, lrbSlot, currentSlot uint32) {
	env.Log().Warnf("[%s] === STUCK-STATE RECOVERY TRIGGERED === LRB %d is %d slots behind; "+
		"restarting to replace state from a fresh remote snapshot", Name, lrbSlot, currentSlot-lrbSlot)

	sf, err := NewStateFileManager(DefaultStateFileName)
	if err != nil {
		env.Log().Errorf("[%s] stuck recovery: failed to open state file: %v", Name, err)
		return
	}
	// empty snapshot file → CheckAndRestoreOnStartup downloads a fresh remote snapshot
	if err := sf.StartCleanup(""); err != nil {
		env.Log().Errorf("[%s] stuck recovery: failed to mark restore: %v", Name, err)
		return
	}
	SnapshotFileForRestore.Store("")
	CleanupRequestedFlag.Store(true)
	env.Stop()
}
