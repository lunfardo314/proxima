package snapshot_restore

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot"
	syncmod "github.com/lunfardo314/proxima/core/core_modules/forward_sync"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
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
//   - behind the network's committed state by more than ST, young-enough snapshot
//     available                                                              → replace
//     from that snapshot (scenario 6),
//   - behind the network's committed state by more than ST, no young snapshot → REFUSE
//     to sync and shut down with a clear message (scenario 6b): the state can no longer
//     be safely or feasibly synced and there is nothing to adopt,
//   - NOT behind the network but the network's committed state is far behind real time
//     (the whole network is stalled at an old state)                         → keep the
//     DB and FORCE the sequencer to start so it can issue bootstrap transactions
//     (scenario 7 — BootstrapFromOldState),
//   - otherwise (recent)                                                     → keep the
//     DB, normal sync.
//
// The tolerance ST is snapshot_restore.max_state_age_slots. It is ON by default: when
// unset it defaults to half the BRANCH txID retention (BranchTxIDStateTTLSlots/2) and is
// always clamped strictly below it. The bound is a correctness requirement, not a tuning
// choice: forward-building a state needs its branch baselines to still be in the trie, so
// the state must be replaced well before it ages past the branch retention horizon. See
// claude/txid_ttl_tiered.md.

// BootstrapFromOldState is set during startup when the node determines it is in
// scenario 7 (the whole network is at an old state with no fresher snapshot to adopt).
// The node reads it when creating the sequencer and forces the sequencer to start
// without waiting for sync. See sync_semantics.md §5.2.
var BootstrapFromOldState atomic.Bool

// checkStateTooOldDownload makes the §5 startup decision for a present, valid DB.
// Returns the downloaded snapshot file path when the DB must be replaced (scenario 6;
// caller deletes the DB and restores), or "" to keep the existing DB. It returns a
// non-nil error to REFUSE startup (scenario 6b: too old behind a live network with no
// snapshot to adopt); the caller propagates it so the node shuts down with a clear
// message. As a side effect it sets BootstrapFromOldState for scenario 7.
func checkStateTooOldDownload(log global.Logging) (string, error) {
	dbSlot, ttl, ok := latestCommittedSlotAndTTLInDB(global.MultiStateDBName)
	if !ok {
		return "", nil
	}
	// Old-state tolerance ST. ON by default (half the branch txID retention); always kept
	// strictly below it — see file header for why the bound is a correctness requirement.
	st := uint32(viper.GetInt("snapshot_restore.max_state_age_slots"))
	if st == 0 {
		st = ttl / 2
	}
	if st >= ttl {
		log.Log().Warnf("[%s] snapshot_restore.max_state_age_slots (%d) must be below the branch txID retention (%d); clamping to %d",
			Name, st, ttl, ttl/2)
		st = ttl / 2
	}

	netCurrent, netCommitted, snapSlot, snapAvailable := querySourcesForRecovery(log)
	if netCurrent == 0 {
		return "", nil // no source reachable — cannot judge; keep the DB (rely on explicit config)
	}

	if netCommitted > dbSlot && netCommitted-dbSlot > st {
		// Behind the network's committed state beyond tolerance. Adopt a fresher snapshot
		// iff it is newer than the DB and young enough that the post-restore remainder is
		// recursively bridgeable.
		if snapAvailable && snapSlot > dbSlot && (netCurrent <= snapSlot || netCurrent-snapSlot <= st) {
			log.Log().Warnf("[%s] STATE TOO OLD: DB committed slot %d is %d behind the network committed state %d "+
				"(tolerance %d); replacing from snapshot at slot %d", Name, dbSlot, netCommitted-dbSlot, netCommitted, st, snapSlot)
			if f := tryDownloadRemoteSnapshot(log, snapshot.SnapshotDirectory()); f != "" {
				return f, nil
			}
			log.Log().Warnf("[%s] STATE TOO OLD: snapshot download failed despite a source advertising one", Name)
		}
		// Behind a live network beyond tolerance, with no snapshot to adopt — refuse and
		// shut down rather than churn on a state that can no longer be safely synced.
		return "", fmt.Errorf("STATE TOO OLD: committed slot %d is %d slots behind the network committed state %d, "+
			"exceeding the tolerance ST=%d (half the branch txID retention %d), and no suitable younger snapshot is available. "+
			"Refusing to sync — provide a reachable snapshot source or restore a fresh snapshot manually",
			dbSlot, netCommitted-dbSlot, netCommitted, st, ttl)
	}

	// Even within the slot tolerance, the DB may be on an UNREACHABLE fork: its committed lineage shares
	// no branch with the network's canonical lineage within the horizon, so it cannot be re-anchored in
	// place (§2a). Replace it from a fresh snapshot, or refuse. A REACHABLE fork — some committed branch
	// still on canonical — is kept here and re-anchored at runtime. See claude/fork_detection_recovery.md §2b.
	if !forkReachable(log) {
		if snapAvailable && snapSlot > dbSlot && (netCurrent <= snapSlot || netCurrent-snapSlot <= st) {
			log.Log().Warnf("[%s] STATE ON UNREACHABLE FORK: DB committed state (slot %d) diverged from the canonical "+
				"lineage; replacing from snapshot at slot %d", Name, dbSlot, snapSlot)
			if f := tryDownloadRemoteSnapshot(log, snapshot.SnapshotDirectory()); f != "" {
				return f, nil
			}
			log.Log().Warnf("[%s] STATE ON UNREACHABLE FORK: snapshot download failed despite a source advertising one", Name)
		}
		return "", fmt.Errorf("STATE ON UNREACHABLE FORK: committed state (slot %d) diverged from the network's canonical "+
			"lineage and cannot be re-anchored, and no suitable younger snapshot is available. Refusing to sync — provide "+
			"a reachable snapshot source or restore a fresh snapshot manually", dbSlot)
	}

	// Not behind the network's committed state (we are at its level). If the network's
	// committed state is itself far behind real time, the whole network is stalled at an
	// old state — force the sequencer to start and help bootstrap it forward.
	if netCurrent > netCommitted && netCurrent-netCommitted > st {
		log.Log().Warnf("[%s] BOOTSTRAP-FROM-OLD-STATE: network committed state %d is %d behind real time %d and no "+
			"fresher snapshot exists — forcing the sequencer to start to issue bootstrap transactions",
			Name, netCommitted, netCurrent-netCommitted, netCurrent)
		BootstrapFromOldState.Store(true)
	}
	return "", nil
}

// forkReachable opens the multistate DB read-only and reports whether its committed state can be
// re-anchored onto the canonical lineage in place (forward_sync.StartupForkReachable) rather than
// needing a fresh snapshot. On any open/parse error it returns true — never replace the DB on a hunch.
// The healthy-branch fraction is read from the DB's own library JSON, NOT the ledger singleton, which
// is not yet initialized at this startup stage (same constraint as latestCommittedSlotAndTTLInDB).
func forkReachable(log global.Logging) bool {
	dbPath := global.MultiStateDBName
	db, err := badger_adaptor.OpenBadgerDB(dbPath, badger.DefaultOptions(dbPath).WithReadOnly(true))
	if err != nil {
		return true
	}
	store := badger_adaptor.New(db)
	defer func() { _ = store.Close() }()

	fraction, ok := healthyFractionFromDB(store)
	if !ok {
		return true // can't derive the fraction — don't replace on a hunch
	}
	return syncmod.StartupForkReachable(store, fraction, log)
}

// healthyFractionFromDB derives the healthy-branch coverage fraction from the DB's latest upgrade
// library (JSON), avoiding the not-yet-initialized ledger singleton at startup.
func healthyFractionFromDB(store global.StoreReader) (global.Fraction, bool) {
	upgradeSlot, found := multistate.GetLatestUpgradeSlot(store)
	if !found {
		return global.Fraction{}, false
	}
	jsonData, found := multistate.GetUpgradeLibraryDirect(store, upgradeSlot)
	if !found {
		return global.Fraction{}, false
	}
	lib, err := ledger.ParseLibraryFromJSON(jsonData, ledger.GetEmbeddedFunctionResolver)
	if err != nil {
		return global.Fraction{}, false
	}
	c := ledger.ConstantsFromLibrary(lib)
	if c.HealthyCoverageDenominator == 0 {
		return global.Fraction{}, false
	}
	return global.Fraction{Numerator: int(c.HealthyCoverageNumerator), Denominator: int(c.HealthyCoverageDenominator)}, true
}

// latestCommittedSlotAndTTLInDB opens the multistate DB read-only and reads the latest
// committed slot together with the TXID state TTL (from the latest upgrade library — the
// global ledger singleton is not yet initialized at this startup stage). Returns
// ok=false if the DB is absent, unopenable, or its ledger definitions can't be read.
func latestCommittedSlotAndTTLInDB(dbPath string) (slot, ttl uint32, ok bool) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return 0, 0, false
	}
	db, err := badger_adaptor.OpenBadgerDB(dbPath, badger.DefaultOptions(dbPath).WithReadOnly(true))
	if err != nil {
		return 0, 0, false
	}
	store := badger_adaptor.New(db)
	defer func() { _ = store.Close() }()

	slot = multistate.FetchLatestCommittedSlot(store)

	upgradeSlot, found := multistate.GetLatestUpgradeSlot(store)
	if !found {
		return 0, 0, false
	}
	jsonData, found := multistate.GetUpgradeLibraryDirect(store, upgradeSlot)
	if !found {
		return 0, 0, false
	}
	lib, err := ledger.ParseLibraryFromJSON(jsonData, ledger.GetEmbeddedFunctionResolver)
	if err != nil {
		return 0, 0, false
	}
	// Use the BRANCH txID retention: forward-building a state needs branch baselines to be
	// resolvable, which is exactly what branch retention bounds (the short non-branch TTL is
	// irrelevant here). See claude/txid_ttl_tiered.md.
	ttl = ledger.ConstantsFromLibrary(lib).BranchTxIDStateTTLSlots
	if ttl == 0 {
		return 0, 0, false // unusable definitions — can't derive ST; skip the too-old check
	}
	return slot, ttl, true
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
