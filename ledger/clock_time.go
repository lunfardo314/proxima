package ledger

import (
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
)

// TickDuration returns the tick duration from the genesis library.
// This is assumed constant across upgrades for time conversion consistency.
func TickDuration() time.Duration {
	return L(0).TickDuration
}

// SlotDuration returns the slot duration from the genesis library.
// This is assumed constant across upgrades for time conversion consistency.
func SlotDuration() time.Duration {
	return L(0).SlotDuration()
}

// TimeFromClockTime converts clock time to ledger time using genesis time parameters.
// Genesis time is immutable, so L(0) is correct here.
func TimeFromClockTime(nowis time.Time) base.LedgerTime {
	return L(0).LedgerTimeFromClockTime(nowis)
}

// UnixNanoFromLedgerTime converts ledger time to unix nano.
// Uses genesis time (immutable) and tick duration (assumed constant).
func UnixNanoFromLedgerTime(t base.LedgerTime) int64 {
	return L(0).GenesisTime().Add(time.Duration(t.TicksSinceGenesis()) * TickDuration()).UnixNano()
}

func TimeNow() base.LedgerTime {
	return TimeFromClockTime(time.Now())
}

func SlotNow() uint32 {
	return TimeNow().Slot
}

// ValidTransactionPace return true if input and target non-sequencer tx timestamps make a valid pace.
// Uses the target timestamp's slot to get the applicable TransactionPace constant.
func ValidTransactionPace(t1, t2 base.LedgerTime) bool {
	return base.DiffTicks(t2, t1) >= int64(L(t2.Slot).TransactionPace)
}

// ValidSequencerPace return true if input and target sequencer tx timestamps make a valid pace.
// Uses the target timestamp's slot to get the applicable TransactionPaceSequencer constant.
func ValidSequencerPace(t1, t2 base.LedgerTime) bool {
	return base.DiffTicks(t2, t1) >= int64(L(t2.Slot).TransactionPaceSequencer)
}

func ClockTime(t base.LedgerTime) time.Time {
	return time.Unix(0, UnixNanoFromLedgerTime(t))
}

func TooCloseOnTimeAxis(txid1, txid2 base.TransactionID) bool {
	if txid1.Timestamp().After(txid2.Timestamp()) {
		txid1, txid2 = txid2, txid1
	}
	// branches are exempt from the sequencer pace constraint (the final pre-branch
	// consolidation may land one tick before the branch).
	if txid1.IsBranchTransaction() || txid2.IsBranchTransaction() {
		return false
	}
	if txid1.IsSequencerTransaction() && txid2.IsSequencerTransaction() {
		return !ValidSequencerPace(txid1.Timestamp(), txid2.Timestamp()) && txid1 != txid2
	}
	return !ValidTransactionPace(txid1.Timestamp(), txid2.Timestamp()) && txid1 != txid2
}
