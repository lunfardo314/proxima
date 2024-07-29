package syncmgr

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

// SyncManager is a daemon which monitors how far is the latest slot with coverage > totalSupply (1/2 of max possible coverage)
// in the state DB from the current time.
// If number of slots back becomes bigger than threshold, it starts pulling sync portions of
// branches from other nodes, while ignoring current flow of transactions
// SyncManager is optional optimization of the sync process. It can be enabled/disabled in the config

type (
	Environment interface {
		global.NodeGlobal
		StateStore() global.StateStore
		PullSyncPortion(startingFrom ledger.Slot, maxSlots int)
	}

	SyncManager struct {
		Environment
		ctx                         context.Context
		cancel                      context.CancelFunc
		cancelled                   atomic.Bool
		syncPortionSlots            int
		syncToleranceThresholdSlots int

		endOfPortionCh                       chan struct{}
		syncPortionRequestedAtLeastUntilSlot ledger.Slot
		syncPortionDeadline                  time.Time
		latestHealthySlotInDB                atomic.Uint32 // cache for IgnoreFutureTxID

		loggedWhen time.Time
	}
)

var FractionHealthyBranchCriterion = global.FractionHalf

func StartSyncManagerFromConfig(env Environment) *SyncManager {
	if !viper.GetBool("workflow.sync_manager.enable") {
		env.Infof0("[sync manager] is DISABLED")
		return nil
	}
	d := &SyncManager{
		Environment:                 env,
		syncPortionSlots:            viper.GetInt("workflow.sync_manager.sync_portion_slots"),
		syncToleranceThresholdSlots: viper.GetInt("workflow.sync_manager.sync_tolerance_threshold_slots"),
		endOfPortionCh:              make(chan struct{}, 1),
	}
	if d.syncPortionSlots < 1 || d.syncPortionSlots > global.MaxSyncPortionSlots {
		d.syncPortionSlots = global.MaxSyncPortionSlots
	}
	if d.syncToleranceThresholdSlots <= 5 || d.syncToleranceThresholdSlots > d.syncPortionSlots/2 {
		d.syncToleranceThresholdSlots = global.DefaultSyncToleranceThresholdSlots
	}

	d.ctx, d.cancel = context.WithCancel(env.Ctx())

	go d.syncManagerLoop()
	return d
}

func (d *SyncManager) _cancel() {
	d.cancelled.Store(true)
	d.cancel()
	d.Infof0("[sync manager] auto-cancelled")
}

const (
	checkSyncEvery = 500 * time.Millisecond
	// portionExpectedIn when repeat portion pull
	portionExpectedIn = 10 * time.Second
)

func (d *SyncManager) syncManagerLoop() {
	d.Infof0("[sync manager] has been started. Sync portion: %d slots. Sync tolerance: %d slots",
		d.syncPortionSlots, d.syncToleranceThresholdSlots)

	for {
		select {
		case <-d.ctx.Done():
			d.Infof0("[sync manager] stopped ")
			return

		case <-d.endOfPortionCh:
			d.Infof1("[sync manager] end of sync portion")
			d.checkSync(true)

		case <-time.After(checkSyncEvery):
			d.checkSync(false)
		}
	}
}

func (d *SyncManager) checkSync(endOfPortion bool) {
	latestHealthySlotInDB := multistate.FindLatestHealthySlot(d.StateStore(), FractionHealthyBranchCriterion)
	d.latestHealthySlotInDB.Store(uint32(latestHealthySlotInDB)) // cache

	slotNow := ledger.TimeNow().Slot()
	util.Assertf(latestHealthySlotInDB <= slotNow, "latestSlot (%d) <= slotNow (%d)", latestHealthySlotInDB, slotNow)

	behind := slotNow - latestHealthySlotInDB
	if int(behind) <= d.syncToleranceThresholdSlots {
		// synced or almost synced. Do not need to pull portions
		d.syncPortionRequestedAtLeastUntilSlot = 0
		d.syncPortionDeadline = time.Time{}
		return
	}
	if time.Since(d.loggedWhen) > time.Second {
		d.Infof1("[sync manager] latest synced slot %d is behind current slot %d by %d",
			latestHealthySlotInDB, slotNow, behind)
		d.loggedWhen = time.Now()
	}

	// above threshold, not synced
	if latestHealthySlotInDB < d.syncPortionRequestedAtLeastUntilSlot {
		// we already pulled portion, but it is not here yet, it seems
		if !endOfPortion && time.Now().Before(d.syncPortionDeadline) {
			// still waiting for the portion, do nothing
			return
		}
		// repeat pull portion
	}

	d.syncPortionRequestedAtLeastUntilSlot = latestHealthySlotInDB + ledger.Slot(d.syncPortionSlots)
	if d.syncPortionRequestedAtLeastUntilSlot > slotNow {
		d.syncPortionRequestedAtLeastUntilSlot = slotNow
	}
	d.syncPortionDeadline = time.Now().Add(portionExpectedIn)
	d.PullSyncPortion(latestHealthySlotInDB, d.syncPortionSlots)
}

func (d *SyncManager) NotifyEndOfPortion() {
	select {
	case d.endOfPortionCh <- struct{}{}:
	default:
	}
}

// IgnoreFutureTxID returns true if transaction is too far in the future from the latest synced branch in DB
// We want to ignore all the current flow of transactions while syncing the state with sync manager
// After the state become synced, the tx flow will be accepted
func (d *SyncManager) IgnoreFutureTxID(txid *ledger.TransactionID) bool {
	if d.cancelled.Load() {
		// all transactions pass
		return false
	}
	slotNow := int(ledger.TimeNow().Slot())
	latestSlot := int(d.latestHealthySlotInDB.Load())
	util.Assertf(latestSlot <= slotNow, "latestSlot <= slotNow")
	if slotNow-latestSlot < d.syncToleranceThresholdSlots {
		// auto-cancel sync manager. From now on node will be synced by attacher pull
		go d._cancel()
		return false
	}
	// not synced. Ignore all too close to the present time
	ignore := int(txid.Slot()) >= slotNow-2
	if ignore && txid.IsBranchTransaction() {
		d.Infof1("[sync manager] ignore transaction while syncing %s", txid.StringShort())
	}
	return ignore
}
