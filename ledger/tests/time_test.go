package tests

import (
	"math/rand"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestPrintTimeConstants(t *testing.T) {
	t.Log(ledger.L().ID.TimeConstantsToString())
}

func TestTime(t *testing.T) {
	t.Run("time constants", func(t *testing.T) {
		t.Logf("%s", ledger.L().ID.TimeConstantsToString())
	})
	t.Run("0", func(t *testing.T) {
		require.True(t, ledger.ValidTime(ledger.TimeNow()))
	})
	t.Run("1", func(t *testing.T) {
		nowis := time.Now()
		ts0 := ledger.TimeFromClockTime(nowis)
		ts1 := ledger.TimeFromClockTime(nowis.Add(1 * time.Second))
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
	})
	t.Run("2", func(t *testing.T) {
		ts0 := ledger.NewLedgerTime(100, 33)
		ts1 := ledger.NewLedgerTime(120, 55)
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
		require.EqualValues(t, 100, ts0.Slot())
		require.EqualValues(t, 120, ts1.Slot())
		require.EqualValues(t, 33, ts0.Tick())
		require.EqualValues(t, 55, ts1.Tick())

		diff := ledger.DiffTicks(ts0, ts1)
		require.EqualValues(t, -(20*ledger.TicksPerSlot + 22), diff)
		diff = ledger.DiffTicks(ts1, ts0)
		require.EqualValues(t, 20*ledger.TicksPerSlot+22, diff)
		diff = ledger.DiffTicks(ts1, ts1)
		require.EqualValues(t, 0, diff)
	})
	t.Run("3", func(t *testing.T) {
		ts := ledger.NewLedgerTime(100, 120)
		require.EqualValues(t, 100, int(ts.Slot()))
		require.EqualValues(t, 120, int(ts.Tick()))
	})
	t.Run("4", func(t *testing.T) {
		ts0 := ledger.NewLedgerTime(100, 33)
		t.Logf("%s", ts0)
		b := ts0.Bytes()
		tsBack, err := ledger.TimeFromBytes(b)
		require.NoError(t, err)
		require.EqualValues(t, ts0, tsBack)
	})
	t.Run("5", func(t *testing.T) {
		ts := ledger.TimeFromClockTime(time.Now())
		t.Logf("ts: %s", ts)
		tsBack := ledger.TimeFromClockTime(ts.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts, tsBack)
	})
	t.Run("ts add diff", func(t *testing.T) {
		nowis := time.Now()
		nowisTs := ledger.TimeFromClockTime(nowis)

		nowis1TickLater := nowis.Add(ledger.TickDuration())
		nowis1TickLaterTs := ledger.TimeFromClockTime(nowis1TickLater)
		require.EqualValues(t, 1, ledger.DiffTicks(nowis1TickLaterTs, nowisTs))
		require.EqualValues(t, nowisTs.AddTicks(1), nowis1TickLaterTs)

		nowis99TicksLater := nowis.Add(99 * ledger.TickDuration())
		nowis99TickLaterTs := ledger.TimeFromClockTime(nowis99TicksLater)
		require.EqualValues(t, 99, ledger.DiffTicks(nowis99TickLaterTs, nowisTs))
		require.EqualValues(t, nowisTs.AddTicks(99), nowis99TickLaterTs)

		rnd := rand.Intn(1_000_000)
		nowisRndTicksLater := nowis.Add(time.Duration(rnd) * ledger.TickDuration())
		nowisRndTickLaterTs := ledger.TimeFromClockTime(nowisRndTicksLater)
		require.EqualValues(t, rnd, ledger.DiffTicks(nowisRndTickLaterTs, nowisTs))
		require.EqualValues(t, nowisTs.AddTicks(rnd), nowisRndTickLaterTs)
	})
	t.Run("7", func(t *testing.T) {
		ts := ledger.NewLedgerTime(100, 99)
		t.Logf("ts = %s", ts)
		ts1 := ts.AddTicks(200)
		t.Logf("ts1 = %s", ts1)
		tsExpect := ledger.NewLedgerTime(101, 43)
		t.Logf("tsExpect = %s", tsExpect)
		require.EqualValues(t, tsExpect, ts1)
	})
}

func TestLedgerVsRealTime(t *testing.T) {
	nowis := time.Now()
	ts := ledger.TimeFromClockTime(nowis)
	nowisBack := ts.Time()
	diffNano := nowis.UnixNano() - nowisBack.UnixNano()
	diffTime := nowis.Sub(nowisBack)
	t.Logf("now:\n    real time: %s\n    nano: %d\n    ledger time: %s", nowis.Format(time.StampNano), nowis.UnixNano(), ts.String())
	t.Logf("now back:\n    real time: %s\n    nano: %d\n", nowisBack.Format(time.StampNano), nowisBack.UnixNano())

	t.Logf("now nano: %d, nowBack nano: %d", nowis.UnixNano(), nowisBack.UnixNano())
	t.Logf("diff nano: %d", diffNano)
	t.Logf("diff time: %v", diffTime)
	require.True(t, util.Abs(diffTime) <= ledger.TickDuration())
}

func TestRealTime(t *testing.T) {
	for i := 0; i < 5; i++ {
		ts := ledger.TimeNow()
		nowis := time.Now()
		require.True(t, !nowis.Before(ts.Time()))
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRealTimeValues(t *testing.T) {
	nowis := time.Now()
	ledgerTimeNow := ledger.TimeFromClockTime(nowis)
	slotNow := ledgerTimeNow.Slot()
	t.Logf("nowis: %s, ledger time now: %s", nowis.Format(time.StampNano), ledgerTimeNow.String())

	t.Run("1", func(t *testing.T) {
		for i := 0; i < 256; i++ {
			ts := ledger.NewLedgerTime(slotNow, byte(i))
			t.Logf("   %s -> %s", ts.String(), ts.Time().Format(time.StampNano))
		}
	})

	t.Run("2", func(t *testing.T) {
		now := nowis
		for i := 0; i < 256; i++ {
			ts := ledger.TimeFromClockTime(now)
			t.Logf("   %s -> %s", now.Format(time.StampNano), ts.String())
			now = now.Add(ledger.DefaultTickDuration)
		}
	})

}
