package ledger

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestClockTimeSaturates checks the ledger time → clock time conversion for a timestamp whose
// nanosecond distance from genesis does not fit an int64 (slot 1_400_000_000, seen in gossip on
// 2026-11-15). Before the cap the product overflowed into the 19th century, so the timestamp counted as
// past on the wall clock while being far in the future in ledger ticks. Now it must land in the future
// on both scales, and an ordinary timestamp must convert exactly as before.
func TestClockTimeSaturates(t *testing.T) {
	InitWithTestingLedgerData()

	far := base.LedgerTime{Slot: 1_400_000_000, Tick: 0}
	require.True(t, TimeNow().Before(far))
	require.True(t, ClockTime(far).After(time.Now()), "far-future ledger time must not convert into the past")
	require.Equal(t, L(0).GenesisTime().Add(maxClockDistanceFromGenesis).UnixNano(), UnixNanoFromLedgerTime(far))

	near := base.LedgerTime{Slot: 1000, Tick: 5}
	expected := L(0).GenesisTime().Add(time.Duration(near.TicksSinceGenesis()) * TickDuration())
	require.Equal(t, expected.UnixNano(), UnixNanoFromLedgerTime(near))
	require.Equal(t, near, TimeFromClockTime(ClockTime(near)))
}
