package syncmgr

import (
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

// SyncManager is a daemon which monitors how war is the latest slot in the state DB from the
// current time. If difference becomes bigger than threshold, it starts pulling sync portions of
// branches from other nodes. During sync current flow of transactions is ignored

type (
	Environment interface {
		global.NodeGlobal
		StateStore() global.StateStore
		PullSyncPortion(startingFrom ledger.Slot, maxSlots int)
	}

	SyncManager struct {
		Environment
		pokeCh                           chan struct{}
		syncPortionRequestedAtLeastUntil ledger.Slot
		syncPortionDeadline              time.Time
		latestSlotInDB                   atomic.Uint32 // caching for IgnoreFutureTxID
	}
)

func StartSyncManager(env Environment) *SyncManager {
	d := &SyncManager{
		Environment: env,
		pokeCh:      make(chan struct{}, 1),
	}
	go d.syncLoop()
	return d
}

const (
	checkSyncEvery    = 500 * time.Millisecond
	portionExpectedIn = 2 * time.Second
)

func (d *SyncManager) syncLoop() {
	d.Log().Infof("sync manager started")

	for {
		select {
		case <-d.Ctx().Done():
			d.Log().Infof("sync manager stopped ")
			return

		case <-d.pokeCh:
			d.checkSync()

		case <-time.After(checkSyncEvery):
			d.checkSync()
		}
	}
}

func (d *SyncManager) checkSync() {
	latestSlotInDB := multistate.FetchLatestSlot(d.StateStore())
	d.latestSlotInDB.Store(uint32(latestSlotInDB))

	slotNow := ledger.TimeNow().Slot()
	util.Assertf(latestSlotInDB <= slotNow, "latestSlot (%d) <= slotNow (%d)", latestSlotInDB, slotNow)

	if slotNow-latestSlotInDB < global.PullSyncPortionThresholdSlots {
		// synced or almost synced. No need to pull portions
		return
	}
	// not synced, below threshold
	if latestSlotInDB < d.syncPortionRequestedAtLeastUntil {
		// we already pulled portion
		if time.Now().Before(d.syncPortionDeadline) {
			// still waiting for the portion, do nothing
			return
		}
	}
	d.syncPortionRequestedAtLeastUntil = latestSlotInDB + global.MaxSyncPortionInSlots
	if d.syncPortionRequestedAtLeastUntil > slotNow {
		d.syncPortionRequestedAtLeastUntil = slotNow
	}
	d.syncPortionDeadline = time.Now().Add(portionExpectedIn)
	d.PullSyncPortion(latestSlotInDB, global.MaxSyncPortionInSlots)
}

func (d *SyncManager) Poke() {
	select {
	case d.pokeCh <- struct{}{}:
	default:
	}
}

// IgnoreFutureTxID returns true if transaction is too far in the future from the latest synced branch in DB
// We want to ignore all the current flow of transactions while syncing the state
// When the state will become synced, the flow will be accepted and synced by tx pull if needed
func (d *SyncManager) IgnoreFutureTxID(txid *ledger.TransactionID) bool {
	latestSlotInDB := ledger.Slot(d.latestSlotInDB.Load())
	txSlot := txid.Slot()
	return txSlot > latestSlotInDB && txSlot-latestSlotInDB > global.PullSyncPortionThresholdSlots
}
