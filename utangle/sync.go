package utangle

import (
	"time"

	"github.com/lunfardo314/proxima/core"
)

func SyncWindowDuration() time.Duration {
	return core.TimeSlotDuration() / 2
}

// IsInSyncWindow returns true if latest added transaction (by timestamp) is no more than 1/2 time slot back from now
func (ut *UTXOTangle) IsInSyncWindow() bool {
	return ut.latestTransactionTSTime.Load().Add(SyncWindowDuration()).After(time.Now())
}

func (ut *UTXOTangle) storeLatestTxTime(txid *core.TransactionID) {
	txTime := txid.Timestamp().Time()
	if ut.latestTransactionTSTime.Load().Before(txTime) {
		ut.latestTransactionTSTime.Store(txTime)
	}
}
