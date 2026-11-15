package ledger

import (
	"math"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestClockTimeSaturates checks the ledger-time to clock-time conversion at the edge of the int64
// nanosecond range. The ledger time range (uint32 slots) is far wider, so the last ledger time that
// fits must convert exactly, and every later one, up to the largest slot, must saturate to the latest
// representable clock time instead of wrapping into the past. The wallet-side copy in txbuildercore
// must agree.
func TestClockTimeSaturates(t *testing.T) {
	InitWithTestingLedgerData()
	t.Cleanup(ResetForTesting)
	consts := ConstantsFromLibrary(L(0).Library)

	genesis := L(0).GenesisTime().UnixNano()
	tick := int64(TickDuration())
	lastTicks := (math.MaxInt64 - genesis) / tick // last tick count whose clock time fits in int64
	last, err := base.LedgerTimeFromTicksSinceGenesis(lastTicks)
	require.NoError(t, err)
	past, err := base.LedgerTimeFromTicksSinceGenesis(lastTicks + 1)
	require.NoError(t, err)

	// at the limit: exact
	require.Equal(t, genesis+lastTicks*tick, UnixNanoFromLedgerTime(last))
	require.Equal(t, ClockTime(last), consts.ClockTime(last))

	// past the limit, a slot like the one seen in the wild, and the largest slot: saturated, never in the past
	now := time.Now()
	for _, ts := range []base.LedgerTime{past, base.T(1_400_000_000, 0), base.T(base.MaxSlot, base.MaxTickValue)} {
		require.EqualValues(t, int64(math.MaxInt64), UnixNanoFromLedgerTime(ts), "%s", ts.String())
		require.True(t, ClockTime(ts).After(now), "%s", ts.String())
		require.Equal(t, ClockTime(ts), consts.ClockTime(ts), "%s", ts.String())
		require.False(t, ClockTime(ts).Before(ClockTime(last)), "%s", ts.String())
	}

	// an ordinary time converts as before: genesis plus ticks times the tick duration
	ts := base.T(1000, 7)
	require.Equal(t, L(0).GenesisTime().Add(time.Duration(ts.TicksSinceGenesis())*TickDuration()), ClockTime(ts))
	require.True(t, ClockTime(ts).Equal(consts.ClockTime(ts)))
}
