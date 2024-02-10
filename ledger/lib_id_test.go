package ledger

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestPrintTimeConstants(t *testing.T) {
	InitWithTestingLedgerIDData()
	t.Log(L().ID.TimeConstantsToString())
}

func TestLoad(t *testing.T) {
	t.Logf("---------- loading constraint library extensions -----------")
	genesisPrivateKey := testutil.GetTestingPrivateKey(314)
	id := DefaultIdentityData(genesisPrivateKey)
	Init(id)
	t.Logf("------------------\n%s", id.String())
	t.Logf("------------------\n" + string(id.YAML()))
	t.Logf("------------------\n" + L().ID.TimeConstantsToString())
}

func TestTimeConstSet(t *testing.T) {
	const d = 10 * time.Millisecond
	id, _ := GetTestingIdentityData()
	id.SetTickDuration(d)
	Init(id)
	t.Logf("\n%s", L().ID.TimeConstantsToString())
	require.EqualValues(t, d, TickDuration())
	t.Logf("------------------\n%s", id.String())
	t.Logf("------------------\n" + string(id.YAML()))
	t.Logf("------------------\n" + L().ID.TimeConstantsToString())
}

func TestTime(t *testing.T) {
	InitWithTestingLedgerIDData()
	t.Run("time constants", func(t *testing.T) {
		t.Logf("%s", L().ID.TimeConstantsToString())
	})
	t.Run("1", func(t *testing.T) {
		nowis := time.Now()
		ts0 := TimeFromRealTime(nowis)
		ts1 := TimeFromRealTime(nowis.Add(1 * time.Second))
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
	})
	t.Run("2", func(t *testing.T) {
		ts0 := MustNewLedgerTime(100, 33)
		ts1 := MustNewLedgerTime(120, 55)
		t.Logf("%s", ts0)
		t.Logf("%s", ts1)
		require.EqualValues(t, 100, ts0.Slot())
		require.EqualValues(t, 120, ts1.Slot())
		require.EqualValues(t, 33, ts0.Tick())
		require.EqualValues(t, 55, ts1.Tick())

		diff := DiffTicks(ts0, ts1)
		require.EqualValues(t, -(20*DefaultTicksPerSlot + 22), diff)
		diff = DiffTicks(ts1, ts0)
		require.EqualValues(t, 20*DefaultTicksPerSlot+22, diff)
		diff = DiffTicks(ts1, ts1)
		require.EqualValues(t, 0, diff)
	})
	t.Run("3", func(t *testing.T) {
		util.RequirePanicOrErrorWith(t, func() error {
			MustNewLedgerTime(100, 120)
			return nil
		}, "assertion failed:: s.Valid()")
	})
	t.Run("4", func(t *testing.T) {
		ts0 := MustNewLedgerTime(100, 33)
		t.Logf("%s", ts0)
		b := ts0.Bytes()
		tsBack, err := TimeFromBytes(b)
		require.NoError(t, err)
		require.EqualValues(t, ts0, tsBack)
	})
	t.Run("5", func(t *testing.T) {
		ts := TimeFromRealTime(time.Now())
		t.Logf("ts: %s", ts)
		tsBack := TimeFromRealTime(ts.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts, tsBack)
	})
	t.Run("6", func(t *testing.T) {
		nowisNano := BaselineTime().UnixNano() + 1_000
		nowis := time.Unix(0, nowisNano)
		ts1 := TimeFromRealTime(nowis)
		t.Logf("ts1: %s", ts1)
		tsBack := TimeFromRealTime(ts1.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts1, tsBack)

		nowis = nowis.Add(TickDuration())
		ts2 := TimeFromRealTime(nowis)
		t.Logf("ts2: %s", ts2)
		tsBack = TimeFromRealTime(ts2.Time())
		t.Logf("tsBack: %s", tsBack)
		require.EqualValues(t, ts2, tsBack)

		d1 := DiffTicks(ts2, ts1)
		t.Logf("diff slots %s - %s = %d", ts2.String(), ts1.String(), d1)
		require.EqualValues(t, 1, d1)
		d2 := DiffTicks(ts1, ts2)
		t.Logf("diff slots %s - %s = %d", ts1.String(), ts2.String(), d2)
		require.EqualValues(t, d2, -1)

		nowis = nowis.Add(99 * TickDuration())
		ts3 := TimeFromRealTime(nowis)
		d3 := DiffTicks(ts3, ts1)
		t.Logf("diff slots %s - %s = %d", ts3.String(), ts1.String(), d3)
		require.EqualValues(t, d3, 100)
	})
	t.Run("7", func(t *testing.T) {
		ts := MustNewLedgerTime(100, 99)
		t.Logf("ts = %s", ts)
		ts1 := ts.AddTicks(20)
		t.Logf("ts1 = %s", ts1)
		tsExpect := MustNewLedgerTime(101, 19)
		t.Logf("tsExpect = %s", tsExpect)
		require.EqualValues(t, tsExpect, ts1)
	})
}

func TestLedgerIDYAML(t *testing.T) {
	privateKey := testutil.GetTestingPrivateKey()
	id := DefaultIdentityData(privateKey)
	yamlableStr := id.YAMLAble().YAML()
	t.Logf("\n" + string(yamlableStr))

	idBack, err := StateIdentityDataFromYAML(yamlableStr)
	require.NoError(t, err)
	require.EqualValues(t, id.Bytes(), idBack.Bytes())
}
