package core

import (
	"testing"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	t.Logf("---------- loading constraint library extensions -----------")
	easyfl.PrintLibraryStats()
}

func TestTime(t *testing.T) {
	t.Run("time constants", func(t *testing.T) {
		t.Logf("%s", TimeConstantsToString())
	})
	t.Run("time constants set", func(t *testing.T) {
		const d = 10 * time.Millisecond
		SetTimeTickDuration(d)
		t.Logf("\n%s", TimeConstantsToString())
		require.EqualValues(t, d, TimeTickDuration())
	})
	t.Run("1", func(t *testing.T) {
		nowis := time.Now()
		ts0 := LogicalTimeFromTime(nowis)
		ts1 := LogicalTimeFromTime(nowis.Add(1 * time.Second))
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
	})
	t.Run("2", func(t *testing.T) {
		ts0 := MustNewLogicalTime(100, 33)
		ts1 := MustNewLogicalTime(120, 55)
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
		require.EqualValues(t, 100, ts0.TimeSlot())
		require.EqualValues(t, 120, ts1.TimeSlot())
		require.EqualValues(t, 33, ts0.TimeTick())
		require.EqualValues(t, 55, ts1.TimeTick())

		diff := DiffTimeTicks(ts0, ts1)
		require.EqualValues(t, -(20*TimeTicksPerSlot + 22), diff)
		diff = DiffTimeTicks(ts1, ts0)
		require.EqualValues(t, 20*TimeTicksPerSlot+22, diff)
		diff = DiffTimeTicks(ts1, ts1)
		require.EqualValues(t, 0, diff)
	})
	t.Run("3", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			MustNewLogicalTime(100, 120)
			return nil
		}, "assertion failed:: s.Valid()")
	})
	t.Run("4", func(t *testing.T) {
		ts0 := MustNewLogicalTime(100, 33)
		t.Logf("%s", ts0)
		b := ts0.Bytes()
		tsBack, err := LogicalTimeFromBytes(b)
		require.NoError(t, err)
		require.EqualValues(t, ts0, tsBack)
	})
	t.Run("5", func(t *testing.T) {
		ts := LogicalTimeFromTime(time.Now())
		t.Logf("ts: %s", ts)
		tsBack := LogicalTimeFromTime(ts.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts, tsBack)
	})
	t.Run("6", func(t *testing.T) {
		nowisNano := BaselineTimeUnixNano + 1_000
		nowis := time.Unix(0, nowisNano)
		ts1 := LogicalTimeFromTime(nowis)
		t.Logf("ts1: %s", ts1)
		tsBack := LogicalTimeFromTime(ts1.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts1, tsBack)

		nowis = nowis.Add(TimeTickDuration())
		ts2 := LogicalTimeFromTime(nowis)
		t.Logf("ts2: %s", ts2)
		tsBack = LogicalTimeFromTime(ts2.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts2, tsBack)

		d1 := DiffTimeTicks(ts2, ts1)
		t.Logf("diff slots %s - %s = %d", ts2.String(), ts1.String(), d1)
		require.EqualValues(t, 1, d1)
		d2 := DiffTimeTicks(ts1, ts2)
		t.Logf("diff slots %s - %s = %d", ts1.String(), ts2.String(), d2)
		require.EqualValues(t, d2, -1)

		nowis = nowis.Add(99 * TimeTickDuration())
		ts3 := LogicalTimeFromTime(nowis)
		d3 := DiffTimeTicks(ts3, ts1)
		t.Logf("diff slots %s - %s = %d", ts3.String(), ts1.String(), d3)
		require.EqualValues(t, d3, 100)
	})
	t.Run("7", func(t *testing.T) {
		ts := MustNewLogicalTime(100, 99)
		t.Logf("ts = %s", ts)
		ts1 := ts.AddTimeTicks(20)
		t.Logf("ts1 = %s", ts1)
		tsExpect := MustNewLogicalTime(101, 19)
		t.Logf("tsExpect = %s", tsExpect)
		require.EqualValues(t, tsExpect, ts1)
	})
}
