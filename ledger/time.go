package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

const (
	tickDuration = 100 * time.Millisecond
	TicksPerSlot = 100

	TimeHorizonYears    = 30 // we want next N years to fit the timestamp
	TimeHorizonHours    = 24 * 365 * TimeHorizonYears
	TimeHorizonDuration = TimeHorizonHours * time.Hour
	MaxSlot             = 0xffffffff >> 2

	// TransactionPaceInTicks TODO for testing. Expected value 3 or 5
	TransactionPaceInTicks = 1 // number of ticks between two consecutive transactions

	SlotByteLength        = 4
	TickByteLength        = 1
	LogicalTimeByteLength = SlotByteLength + TickByteLength // bytes
)

var (
	timeTickDurationVar = tickDuration

	BaselineTime         = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	BaselineTimeUnixNano = BaselineTime.UnixNano()
)

func TickDuration() time.Duration {
	return timeTickDurationVar
}

// SetTimeTickDuration is used for testing
func SetTimeTickDuration(d time.Duration) {
	timeTickDurationVar = d
	util.Assertf(TicksPerSlot <= 0xff, "TicksPerSlot must fit the byte")
	util.Assertf(TimeHorizonSlots() <= MaxSlot, "TimeHorizonSlots <= MaxSlot")
	fmt.Printf("---- SetTimeTickDuration: time tick duration is set to %v ----\n", TickDuration())
}

func SlotDuration() time.Duration {
	return TicksPerSlot * TickDuration()
}

func TimeHorizonSlots() int64 {
	return int64(TimeHorizonDuration) / int64(SlotDuration())
}

func SlotsPerYear() int64 {
	return int64(24*365*time.Hour) / int64(SlotDuration())
}

func YearsPerMaxSlot() int64 {
	return MaxSlot / SlotsPerYear()
}

func TransactionTimePaceDuration() time.Duration {
	return TransactionPaceInTicks * TickDuration()
}

func TimeConstantsToString() string {
	return lines.New().
		Add("TickDuration = %v", TickDuration()).
		Add("TicksPerSlot = %d", TicksPerSlot).
		Add("SlotDuration = %v", SlotDuration()).
		Add("TimeHorizonYears = %d", TimeHorizonYears).
		Add("TimeHorizonHours = %d", TimeHorizonHours).
		Add("TimeHorizonDuration = %v", TimeHorizonDuration).
		Add("TimeHorizonSlots = %d", TimeHorizonSlots()).
		Add("MaxSlot = %d, %x", MaxSlot, MaxSlot).
		Add("SlotsPerYear = %d", SlotsPerYear()).
		Add("seconds per year = %d", 60*60*24*365).
		Add("YearsPerMaxSlot = %d", YearsPerMaxSlot()).
		Add("BaselineTime = %v, unix nano: %d, unix seconds: %d", BaselineTime, BaselineTimeUnixNano, BaselineTime.Unix()).
		Add("timestamp BaselineTime = %s", LogicalTimeFromTime(BaselineTime)).
		Add("timestamp now = %s, now is %v", LogicalTimeNow().String(), time.Now()).
		String()
}

type (
	// Slot represents a particular time slot.
	// Starting slot 0 at genesis
	// 3.1 mil slots per year (for 100 msec/tick and slot is 100 ticks)
	// uint32 enough for approx 1361 years
	Slot uint32

	// Tick is enforced to be < TicksPerSlot, which is normally 100 ms
	Tick byte

	// LogicalTime
	// bytes 0:3 represents time slot (bigendian)
	// byte 4 represents slot
	LogicalTime [LogicalTimeByteLength]byte
)

var (
	NilLogicalTime     LogicalTime
	errWrongDataLength = fmt.Errorf("wrong data length")
)

// TimeSlotFromBytes enforces 2 most significant bits of the first byte are 0
func TimeSlotFromBytes(data []byte) (ret Slot, err error) {
	if len(data) != 4 {
		err = errWrongDataLength
		return
	}
	if 0b11000000&data[0] != 0 {
		return 0, fmt.Errorf("2 most significant bits must be 0")
	}
	return Slot(binary.BigEndian.Uint32(data)), nil
}

func MustTimeSlotFromBytes(data []byte) Slot {
	ret, err := TimeSlotFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func TimeTickFromByte(d byte) (ret Tick, err error) {
	if ret = Tick(d); ret.Valid() {
		return
	}
	err = fmt.Errorf("wrong time tick value")
	return
}

func MustNewLogicalTime(e Slot, s Tick) (ret LogicalTime) {
	util.Assertf(s.Valid(), "s.Valid()")
	binary.BigEndian.PutUint32(ret[:4], uint32(e))
	ret[4] = byte(s)
	return
}

func LogicalTimeFromTime(nowis time.Time) LogicalTime {
	util.Assertf(!nowis.Before(BaselineTime), "!nowis.Before(BaselineTime)")
	i := nowis.UnixNano() - BaselineTimeUnixNano
	e := i / int64(SlotDuration())
	util.Assertf(e <= math.MaxUint32, "LogicalTimeFromTime: e <= math.MaxUint32")
	util.Assertf(uint32(e)&0xc0000000 == 0, "LogicalTimeFromTime: two highest bits must be 0. Wrong constants")
	s := i % int64(SlotDuration())
	s = s / int64(TickDuration())
	util.Assertf(s < TicksPerSlot, "LogicalTimeFromTime: s < TicksPerSlot")
	return MustNewLogicalTime(Slot(uint32(e)), Tick(byte(s)))
}

func LogicalTimeNow() LogicalTime {
	return LogicalTimeFromTime(time.Now())
}

func LogicalTimeFromBytes(data []byte) (ret LogicalTime, err error) {
	if len(data) != LogicalTimeByteLength {
		err = errWrongDataLength
		return
	}
	var e Slot
	e, err = TimeSlotFromBytes(data[:4])
	if err != nil {
		return
	}
	var s Tick
	s, err = TimeTickFromByte(data[4])
	if err != nil {
		return
	}
	ret = MustNewLogicalTime(e, s)
	return
}

func MustLogicalTimeFromBytes(data []byte) LogicalTime {
	ret, err := LogicalTimeFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (s Tick) Valid() bool {
	return byte(s) < TicksPerSlot
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

func (e Slot) LogicalTimeAtBeginning() LogicalTime {
	return MustNewLogicalTime(e, 0)
}

func (t LogicalTime) Slot() Slot {
	return Slot(binary.BigEndian.Uint32(t[:4]))
}

func (t LogicalTime) Tick() Tick {
	ret := Tick(t[4])
	util.Assertf(ret.Valid(), "invalid slot value")
	return ret
}

func (t LogicalTime) IsSlotBoundary() bool {
	return t != NilLogicalTime && t.Tick() == 0
}

func (t LogicalTime) UnixNano() int64 {
	return BaselineTimeUnixNano +
		int64(t.Slot())*int64(SlotDuration()) + int64(TickDuration())*int64(t.Tick())
}

func (t LogicalTime) Time() time.Time {
	return time.Unix(0, t.UnixNano())
}

func (t LogicalTime) NextTimeSlotBoundary() LogicalTime {
	if t.Tick() == 0 {
		return t
	}
	return MustNewLogicalTime(t.Slot()+1, 0)
}

func (t LogicalTime) TimesTicksToNextSlotBoundary() int {
	if t.Tick() == 0 {
		return 0
	}
	return TicksPerSlot - int(t.Tick())
}

func (t LogicalTime) Bytes() []byte {
	ret := t
	return ret[:]
}

func (t LogicalTime) String() string {
	return fmt.Sprintf("%d|%d", t.Slot(), t.Tick())
}

func (t LogicalTime) AsFileName() string {
	return fmt.Sprintf("%d_%d", t.Slot(), t.Tick())
}

func (t LogicalTime) Short() string {
	e := t.Slot() % 1000
	return fmt.Sprintf(".%d|%d", e, t.Tick())
}

func (t LogicalTime) After(t1 LogicalTime) bool {
	return DiffTimeTicks(t, t1) > 0
}

func (t LogicalTime) Before(t1 LogicalTime) bool {
	return DiffTimeTicks(t, t1) < 0
}

func (t LogicalTime) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t[:]))
}

// DiffTimeTicks returns difference in slots between two timestamps:
// < 0 is t is before t1
// > 0 if t1 is before t
func DiffTimeTicks(t1, t2 LogicalTime) int64 {
	slots1 := int64(t1.Slot())*TicksPerSlot + int64(t1.Tick())
	slots2 := int64(t2.Slot())*TicksPerSlot + int64(t2.Tick())
	return slots1 - slots2
}

// ValidTimePace checks if 2 timestamps have at least time pace slots in between
func ValidTimePace(t1, t2 LogicalTime) bool {
	return DiffTimeTicks(t2, t1) >= int64(TransactionPaceInTicks)
}

func (t LogicalTime) AddTicks(s int) LogicalTime {
	util.Assertf(s >= 0, "AddTicks: can't be negative argument")
	s1 := int64(t.Tick()) + int64(s)
	eRet := s1 / TicksPerSlot
	sRet := s1 % TicksPerSlot
	return MustNewLogicalTime(t.Slot()+Slot(eRet), Tick(sRet))
}

func (t LogicalTime) AddTimeSlots(e int) LogicalTime {
	return MustNewLogicalTime(t.Slot()+Slot(e), t.Tick())
}

func (t LogicalTime) AddDuration(d time.Duration) LogicalTime {
	return LogicalTimeFromTime(t.Time().Add(d))
}

func MaxLogicalTime(ts ...LogicalTime) LogicalTime {
	return util.Maximum(ts, func(ts1, ts2 LogicalTime) bool {
		return ts1.Before(ts2)
	})
}
