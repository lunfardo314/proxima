package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/util"
)

const (
	SlotByteLength = 4
	TickByteLength = 1
	TimeByteLength = SlotByteLength + TickByteLength // bytes
)

func SlotsPerDay() time.Duration {
	return 24 * time.Hour / SlotDuration()
}

func SlotsPerHour() int {
	return int(time.Hour / SlotDuration())
}

func TicksPerYear() int {
	return int(SlotsPerLedgerEpoch() * DefaultTicksPerSlot)
}

func TicksPerHour() int {
	return int(SlotsPerHour() * DefaultTicksPerSlot)
}

func BaselineTime() time.Time {
	return L().ID.BaselineTime
}

func TickDuration() time.Duration {
	return L().ID.TickDuration
}

func SlotDuration() time.Duration {
	return DefaultTicksPerSlot * TickDuration()
}

func SlotsPerLedgerEpoch() int64 {
	return int64(L().ID.SlotsPerLedgerEpoch)
}

func TransactionTimePaceDuration() time.Duration {
	return time.Duration(TransactionPace()) * TickDuration()
}

type (
	// Slot represents a particular time slot.
	// Starting slot 0 at genesis
	// 3.1 mil slots per year (for 100 msec/tick and slot is 100 ticks)
	// uint32 enough for approx 1361 years
	Slot uint32

	// Tick is enforced to be < DefaultTicksPerSlot, which is normally 100 ms
	Tick byte

	// Time (ledger.Time)
	// bytes 0:3 represents time slot (bigendian)
	// byte 4 represents slot

	Time [TimeByteLength]byte
)

var (
	NilLedgerTime      Time
	errWrongDataLength = fmt.Errorf("wrong data length")
)

// SlotFromBytes enforces 2 most significant bits of the first byte are 0
func SlotFromBytes(data []byte) (ret Slot, err error) {
	if len(data) != 4 {
		err = errWrongDataLength
		return
	}
	if 0b11000000&data[0] != 0 {
		return 0, fmt.Errorf("2 most significant bits must be 0")
	}
	return Slot(binary.BigEndian.Uint32(data)), nil
}

func MustSlotFromBytes(data []byte) Slot {
	ret, err := SlotFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func TickFromByte(d byte) (ret Tick, err error) {
	if ret = Tick(d); ret.Valid() {
		return
	}
	err = fmt.Errorf("wrong tick value")
	return
}

func MustNewLedgerTime(e Slot, s Tick) (ret Time) {
	util.Assertf(s.Valid(), "s.Valid()")
	binary.BigEndian.PutUint32(ret[:4], uint32(e))
	ret[4] = byte(s)
	return
}

func TimeFromRealTime(nowis time.Time) Time {
	return L().ID.TimeFromRealTime(nowis)
}

func TimeNow() Time {
	return TimeFromRealTime(time.Now())
}

func TimeFromBytes(data []byte) (ret Time, err error) {
	if len(data) != TimeByteLength {
		err = errWrongDataLength
		return
	}
	var e Slot
	e, err = SlotFromBytes(data[:4])
	if err != nil {
		return
	}
	var s Tick
	s, err = TickFromByte(data[4])
	if err != nil {
		return
	}
	ret = MustNewLedgerTime(e, s)
	return
}

func (s Tick) Valid() bool {
	return byte(s) < DefaultTicksPerSlot
}

func (s Tick) String() string {
	return fmt.Sprintf("%d", s)
}

func (e Slot) Bytes() []byte {
	ret := make([]byte, SlotByteLength)
	binary.BigEndian.PutUint32(ret, uint32(e))
	return ret
}

func (e Slot) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(e.Bytes()))
}

func (t Time) Slot() Slot {
	return Slot(binary.BigEndian.Uint32(t[:4]))
}

func (t Time) Tick() Tick {
	ret := Tick(t[4])
	util.Assertf(ret.Valid(), "invalid slot value")
	return ret
}

func (t Time) IsSlotBoundary() bool {
	return t != NilLedgerTime && t.Tick() == 0
}

func (t Time) UnixNano() int64 {
	return BaselineTime().UnixNano() +
		int64(t.Slot())*int64(SlotDuration()) + int64(TickDuration())*int64(t.Tick())
}

func (t Time) Time() time.Time {
	return time.Unix(0, t.UnixNano())
}

func (t Time) NextTimeSlotBoundary() Time {
	if t.Tick() == 0 {
		return t
	}
	return MustNewLedgerTime(t.Slot()+1, 0)
}

func (t Time) TimesTicksToNextSlotBoundary() int {
	if t.Tick() == 0 {
		return 0
	}
	return DefaultTicksPerSlot - int(t.Tick())
}

func (t Time) Bytes() []byte {
	ret := t
	return ret[:]
}

func (t Time) String() string {
	return fmt.Sprintf("%d|%d", t.Slot(), t.Tick())
}

func (t Time) Source() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t.Bytes()))
}

func (t Time) AsFileName() string {
	return fmt.Sprintf("%d_%d", t.Slot(), t.Tick())
}

func (t Time) Short() string {
	e := t.Slot() % 1000
	return fmt.Sprintf(".%d|%d", e, t.Tick())
}

func (t Time) After(t1 Time) bool {
	return DiffTicks(t, t1) > 0
}

func (t Time) Before(t1 Time) bool {
	return DiffTicks(t, t1) < 0
}

func (t Time) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t[:]))
}

// DiffTicks returns difference in slots between two timestamps:
// < 0 is t is before t1
// > 0 if t1 is before t
func DiffTicks(t1, t2 Time) int64 {
	slots1 := int64(t1.Slot())*DefaultTicksPerSlot + int64(t1.Tick())
	slots2 := int64(t2.Slot())*DefaultTicksPerSlot + int64(t2.Tick())
	return slots1 - slots2
}

// ValidTimePace checks if 2 timestamps have at least time pace slots in between
func ValidTimePace(t1, t2 Time) bool {
	return DiffTicks(t2, t1) >= int64(TransactionPace())
}

func (t Time) AddTicks(s int) Time {
	util.Assertf(s >= 0, "AddTicks: can't be negative argument")
	s1 := int(t.Tick()) + int(s)
	ticksPerSlot := L().ID.TicksPerSlot()
	eRet := s1 / ticksPerSlot // DefaultTicksPerSlot
	sRet := s1 % ticksPerSlot // DefaultTicksPerSlot
	return MustNewLedgerTime(t.Slot()+Slot(eRet), Tick(sRet))
}

func (t Time) AddSlots(e int) Time {
	return MustNewLedgerTime(t.Slot()+Slot(e), t.Tick())
}

func (t Time) AddDuration(d time.Duration) Time {
	return TimeFromRealTime(t.Time().Add(d))
}

func MaxTime(ts ...Time) Time {
	return util.Maximum(ts, func(ts1, ts2 Time) bool {
		return ts1.Before(ts2)
	})
}

// SleepDurationUntilFutureLedgerTime returns duration to sleep until the clock becomes to ts.Time()
func SleepDurationUntilFutureLedgerTime(ts Time) (ret time.Duration) {
	realTs := ts.Time()
	nowis := time.Now()
	if realTs.After(nowis) {
		ret = realTs.Sub(nowis)
	}
	return
}
