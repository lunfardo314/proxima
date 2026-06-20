package snapshot_restore

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot"
	syncmod "github.com/lunfardo314/proxima/core/core_modules/forward_sync"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/viper"
)

// Too-old-state startup decision (sync_semantics.md §5).
//
// "Too old" is RELATIVE — meaningful only if a younger state exists somewhere. The
// decision is made DIRECTLY on the DB at startup (the live node's IsSynced answers a
// different question — §5): open the DB read-only, read the latest committed slot,
// query trusted sources for the network's current slot, committed state, and newest
// snapshot. Then:
//   - behind the network's committed state, young-enough snapshot available  → replace
//     from that snapshot (scenario 6),
//   - behind the network's committed state, no young snapshot                → keep the
//     DB, no force (we are genuinely behind a live network — never fork),
//   - NOT behind the network but the network's committed state is far behind real time
//     (the whole network is stalled at an old state)                         → keep the
//     DB and FORCE the sequencer to start so it can issue bootstrap transactions
//     (scenario 7 — BootstrapFromOldState),
//   - otherwise (recent)                                                     → keep the
//     DB, normal sync.
//
// Opt-in via snapshot_restore.max_state_age_slots (>0; set near the recursion depth cap).

// BootstrapFromOldState is set during startup when the node determines it is in
// scenario 7 (the whole network is at an old state with no fresher snapshot to adopt).
// The node reads it when creating the sequencer and forces the sequencer to start
// without waiting for sync. See sync_semantics.md §5.2.
var BootstrapFromOldState atomic.Bool

// checkStateTooOldDownload makes the §5 startup decision for a present, valid DB.
// Returns the downloaded snapshot file path when the DB must be replaced (scenario 6;
// caller deletes the DB and restores), or "" to keep the existing DB. As a side effect
// it sets BootstrapFromOldState for scenario 7. Never refuses for a valid DB.
func checkStateTooOldDownload(log global.Logging) string {
	maxAge := uint32(viper.GetInt("snapshot_restore.max_state_age_slots"))
	if maxAge == 0 {
		return "" // disabled
	}
	dbSlot, ok := latestCommittedSlotInDB(global.MultiStateDBName)
	if !ok {
		return ""
	}
	netCurrent, netCommitted, snapSlot, snapAvailable := querySourcesForRecovery(log)
	if netCurrent == 0 {
		return "" // no source reachable — cannot judge; keep the DB (rely on explicit config)
	}

	if netCommitted > dbSlot && netCommitted-dbSlot > maxAge {
		// Behind the network's committed state. Adopt a fresher snapshot iff it is newer
		// than the DB and young enough that the post-restore remainder is recursively
		// bridgeable.
		if snapAvailable && snapSlot > dbSlot && (netCurrent <= snapSlot || netCurrent-snapSlot <= maxAge) {
			log.Log().Warnf("[%s] STATE TOO OLD: DB committed slot %d is %d behind the network committed state %d; "+
				"replacing from snapshot at slot %d", Name, dbSlot, netCommitted-dbSlot, netCommitted, snapSlot)
			return tryDownloadRemoteSnapshot(log, snapshot.SnapshotDirectory())
		}
		log.Log().Warnf("[%s] DB committed slot %d is %d behind the network committed state %d, but no young-enough "+
			"newer snapshot is available — starting from the existing DB (will keep trying to sync)",
			Name, dbSlot, netCommitted-dbSlot, netCommitted)
		return "" // genuinely behind a live network, no snapshot — do NOT force the sequencer
	}

	// Not behind the network's committed state (we are at its level). If the network's
	// committed state is itself far behind real time, the whole network is stalled at an
	// old state — force the sequencer to start and help bootstrap it forward.
	if netCurrent > netCommitted && netCurrent-netCommitted > maxAge {
		log.Log().Warnf("[%s] BOOTSTRAP-FROM-OLD-STATE: network committed state %d is %d behind real time %d and no "+
			"fresher snapshot exists — forcing the sequencer to start to issue bootstrap transactions",
			Name, netCommitted, netCurrent-netCommitted, netCurrent)
		BootstrapFromOldState.Store(true)
	}
	return ""
}

// latestCommittedSlotInDB opens the multistate DB read-only, reads the latest committed
// slot, and closes it. Returns ok=false if the DB is absent or unopenable.
func latestCommittedSlotInDB(dbPath string) (uint32, bool) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return 0, false
	}
	db, err := badger_adaptor.OpenBadgerDB(dbPath, badger.DefaultOptions(dbPath).WithReadOnly(true))
	if err != nil {
		return 0, false
	}
	store := badger_adaptor.New(db)
	slot := multistate.FetchLatestCommittedSlot(store)
	_ = store.Close()
	return slot, true
}

// querySourcesForRecovery queries the trusted `sources` and returns, across all reachable
// sources: the max current slot (wall-clock-driven), the max committed (LRB) slot — used
// here only advisorily, to tell "behind a live network" apart from "network stalled at an
// old state" — and the newest snapshot slot that is at least minSnapshotAgeSlots old.
func querySourcesForRecovery(log global.Logging) (currentSlot, committedSlot, snapshotSlot uint32, snapshotAvailable bool) {
	sourceURLs := viper.GetStringSlice("sources")
	selfAPIPort := viper.GetInt("api.port")
	for _, url := range sourceURLs {
		if syncmod.IsSelfURL(url, selfAPIPort) {
			continue
		}
		c := client.NewWithGoogleDNS(url, 15*time.Second)
		si, err := c.GetSyncInfo()
		if err != nil {
			continue
		}
		if si.CurrentSlot > currentSlot {
			currentSlot = si.CurrentSlot
		}
		if si.LrbSlot > committedSlot {
			committedSlot = si.LrbSlot
		}
		info, err := c.GetSnapshotInfo()
		if err != nil {
			continue
		}
		if si.CurrentSlot >= info.Slot+minSnapshotAgeSlots { // old enough per the source's own clock
			if !snapshotAvailable || info.Slot > snapshotSlot {
				snapshotSlot = info.Slot
				snapshotAvailable = true
			}
		}
	}
	return
}
