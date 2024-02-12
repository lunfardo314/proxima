package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestPrintTimeConstants(t *testing.T) {
	t.Log(ledger.L().ID.TimeConstantsToString())
}

func TestLoad(t *testing.T) {
	t.Logf("---------- loading constraint library extensions -----------")
	t.Logf("------------------\n%s", ledger.L().ID.String())
	t.Logf("------------------\n" + string(ledger.L().ID.YAML()))
	t.Logf("------------------\n" + ledger.L().ID.TimeConstantsToString())
}

func TestTime(t *testing.T) {
	t.Run("time constants", func(t *testing.T) {
		t.Logf("%s", ledger.L().ID.TimeConstantsToString())
	})
	t.Run("1", func(t *testing.T) {
		nowis := time.Now()
		ts0 := ledger.TimeFromRealTime(nowis)
		ts1 := ledger.TimeFromRealTime(nowis.Add(1 * time.Second))
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
	})
	t.Run("2", func(t *testing.T) {
		ts0 := ledger.MustNewLedgerTime(100, 33)
		ts1 := ledger.MustNewLedgerTime(120, 55)
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
		require.EqualValues(t, 100, ts0.Slot())
		require.EqualValues(t, 120, ts1.Slot())
		require.EqualValues(t, 33, ts0.Tick())
		require.EqualValues(t, 55, ts1.Tick())

		diff := ledger.DiffTicks(ts0, ts1)
		require.EqualValues(t, -(20*ledger.L().ID.TicksPerSlot() + 22), diff)
		diff = ledger.DiffTicks(ts1, ts0)
		require.EqualValues(t, 20*ledger.L().ID.TicksPerSlot()+22, diff)
		diff = ledger.DiffTicks(ts1, ts1)
		require.EqualValues(t, 0, diff)
	})
	t.Run("3", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			ledger.MustNewLedgerTime(100, 120)
			return nil
		}, "assertion failed:: s.Valid()")
	})
	t.Run("4", func(t *testing.T) {
		ts0 := ledger.MustNewLedgerTime(100, 33)
		t.Logf("%s", ts0)
		b := ts0.Bytes()
		tsBack, err := ledger.TimeFromBytes(b)
		require.NoError(t, err)
		require.EqualValues(t, ts0, tsBack)
	})
	t.Run("5", func(t *testing.T) {
		ts := ledger.TimeFromRealTime(time.Now())
		t.Logf("ts: %s", ts)
		tsBack := ledger.TimeFromRealTime(ts.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts, tsBack)
	})
	t.Run("6", func(t *testing.T) {
		nowisNano := ledger.BaselineTime().UnixNano() + 1_000
		nowis := time.Unix(0, nowisNano)
		ts1 := ledger.TimeFromRealTime(nowis)
		t.Logf("ts1: %s", ts1)
		tsBack := ledger.TimeFromRealTime(ts1.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts1, tsBack)

		nowis = nowis.Add(ledger.TickDuration())
		ts2 := ledger.TimeFromRealTime(nowis)
		t.Logf("ts2: %s", ts2)
		tsBack = ledger.TimeFromRealTime(ts2.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts2, tsBack)

		d1 := ledger.DiffTicks(ts2, ts1)
		t.Logf("diff slots %s - %s = %d", ts2.String(), ts1.String(), d1)
		require.EqualValues(t, 1, d1)
		d2 := ledger.DiffTicks(ts1, ts2)
		t.Logf("diff slots %s - %s = %d", ts1.String(), ts2.String(), d2)
		require.EqualValues(t, d2, -1)

		nowis = nowis.Add(99 * ledger.TickDuration())
		ts3 := ledger.TimeFromRealTime(nowis)
		d3 := ledger.DiffTicks(ts3, ts1)
		t.Logf("diff slots %s - %s = %d", ts3.String(), ts1.String(), d3)
		require.EqualValues(t, d3, 100)
	})
	t.Run("7", func(t *testing.T) {
		ts := ledger.MustNewLedgerTime(100, 99)
		t.Logf("ts = %s", ts)
		ts1 := ts.AddTicks(20)
		t.Logf("ts1 = %s", ts1)
		tsExpect := ledger.MustNewLedgerTime(101, 19)
		t.Logf("tsExpect = %s", tsExpect)
		require.EqualValues(t, tsExpect, ts1)
	})
}

func TestLedgerIDYAML(t *testing.T) {
	id := ledger.L().ID
	yamlableStr := id.YAMLAble().YAML()
	t.Logf("\n" + string(yamlableStr))

	idBack, err := ledger.StateIdentityDataFromYAML(yamlableStr)
	require.NoError(t, err)
	require.EqualValues(t, id.Bytes(), idBack.Bytes())
}
