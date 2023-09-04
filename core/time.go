package core

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
	timeTickDuration = 100 * time.Millisecond
	TimeTicksPerSlot = 100

	TimeHorizonYears    = 30 // we want next N years to fit the timestamp
	TimeHorizonHours    = 24 * 365 * TimeHorizonYears
	TimeHorizonDuration = TimeHorizonHours * time.Hour
	MaxTimeSlot         = 0xffffffff >> 2

	// TransactionTimePaceInTicks TODO for testing. Expected value 3 or 5
	TransactionTimePaceInTicks = 1 // number of ticks between two consecutive transactions

	TimeSlotByteLength    = 4
	TimeTickByteLength    = 1
	LogicalTimeByteLength = TimeSlotByteLength + TimeTickByteLength // bytes
)

var (
	timeTickDurationVar = timeTickDuration

	BaselineTime         = time.Date(2023, 8, 23, 5, 0, 0, 0, time.UTC)
	BaselineTimeUnixNano = BaselineTime.UnixNano()
)

func TimeTickDuration() time.Duration {
	return timeTickDurationVar
}

// SetTimeTickDuration is used for testing
func SetTimeTickDuration(d time.Duration) {
	timeTickDurationVar = d
	util.Assertf(TimeTicksPerSlot <= 0xff, "TimeTicksPerSlot must fit the byte")
	util.Assertf(TimeHorizonSlots() <= MaxTimeSlot, "TimeHorizonSlots <= MaxTimeSlot")
	fmt.Printf("time slot duration is set to %v", TimeTickDuration())
}

func TimeSlotDuration() time.Duration {
	return TimeTicksPerSlot * TimeTickDuration()
}

func TimeHorizonSlots() int64 {
	return int64(TimeHorizonDuration) / int64(TimeSlotDuration())
}

func TimeSlotsPerYear() int64 {
	return int64(24*365*time.Hour) / int64(TimeSlotDuration())
}

func YearsPerMaxTimeSlot() int64 {
	return MaxTimeSlot / TimeSlotsPerYear()
}

func TransactionTimePaceDuration() time.Duration {
	return TransactionTimePaceInTicks * TimeTickDuration()
}

func TimeConstantsToString() string {
	return lines.New().
		Add("TimeTickDuration = %v", TimeTickDuration()).
		Add("TimeTicksPerSlot = %d", TimeTicksPerSlot).
		Add("TimeSlotDuration = %v", TimeSlotDuration()).
		Add("TimeHorizonYears = %d", TimeHorizonYears).
		Add("TimeHorizonHours = %d", TimeHorizonHours).
		Add("TimeHorizonDuration = %v", TimeHorizonDuration).
		Add("TimeHorizonSlots = %d", TimeHorizonSlots()).
		Add("MaxTimeSlot = %d, %x", MaxTimeSlot, MaxTimeSlot).
		Add("TimeSlotsPerYear = %d", TimeSlotsPerYear()).
		Add("seconds per year = %d", 60*60*24*365).
		Add("YearsPerMaxTimeSlot = %d", YearsPerMaxTimeSlot()).
		Add("BaselineTime = %v, unix nano: %d, unix seconds: %d", BaselineTime, BaselineTimeUnixNano, BaselineTime.Unix()).
		Add("timestamp BaselineTime = %s", LogicalTimeFromTime(BaselineTime)).
		Add("timestamp now = %s, now is %v", LogicalTimeNow().String(), time.Now()).
		String()
}

type (
	// TimeSlot represents a particular time slot.
	// Starting slot 0 at genesis
	// 3.1 mil slots per year (for 100 msec/tick and slot is 100 ticks)
	// uint32 enough for approx 1361 years
	TimeSlot uint32

	// TimeTick is enforced to be < TimeTicksPerSlot, which is normally 100 ms
	TimeTick byte

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
func TimeSlotFromBytes(data []byte) (ret TimeSlot, err error) {
	if len(data) != 4 {
		err = errWrongDataLength
		return
	}
	if 0b11000000&data[0] != 0 {
		return 0, fmt.Errorf("2 most significant bits must be 0")
	}
	return TimeSlot(binary.BigEndian.Uint32(data)), nil
}

func MustTimeSlotFromBytes(data []byte) TimeSlot {
	ret, err := TimeSlotFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func TimeSlotFromByte(d byte) (ret TimeTick, err error) {
	if ret = TimeTick(d); ret.Valid() {
		return
	}
	err = fmt.Errorf("wrong slot value")
	return
}

func MustNewLogicalTime(e TimeSlot, s TimeTick) (ret LogicalTime) {
	util.Assertf(s.Valid(), "s.Valid()")
	binary.BigEndian.PutUint32(ret[:4], uint32(e))
	ret[4] = byte(s)
	return
}

func LogicalTimeFromTime(nowis time.Time) LogicalTime {
	util.Assertf(!nowis.Before(BaselineTime), "!nowis.Before(BaselineTime)")
	i := nowis.UnixNano() - BaselineTimeUnixNano
	e := i / int64(TimeSlotDuration())
	util.Assertf(e <= math.MaxUint32, "LogicalTimeFromTime: e <= math.MaxUint32")
	util.Assertf(uint32(e)&0xc0000000 == 0, "LogicalTimeFromTime: two highest bits must be 0. Wrong constants")
	s := i % int64(TimeSlotDuration())
	s = s / int64(TimeTickDuration())
	util.Assertf(s < TimeTicksPerSlot, "LogicalTimeFromTime: s < TimeTicksPerSlot")
	return MustNewLogicalTime(TimeSlot(uint32(e)), TimeTick(byte(s)))
}

func LogicalTimeNow() LogicalTime {
	return LogicalTimeFromTime(time.Now())
}

func LogicalTimeFromBytes(data []byte) (ret LogicalTime, err error) {
	if len(data) != LogicalTimeByteLength {
		err = errWrongDataLength
		return
	}
	var e TimeSlot
	e, err = TimeSlotFromBytes(data[:4])
	if err != nil {
		return
	}
	var s TimeTick
	s, err = TimeSlotFromByte(data[4])
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

func (s TimeTick) Valid() bool {
	return byte(s) < TimeTicksPerSlot
}

func (s TimeTick) String() string {
	return fmt.Sprintf("%d", s)
}

func (e TimeSlot) Bytes() []byte {
	ret := make([]byte, TimeSlotByteLength)
	binary.BigEndian.PutUint32(ret, uint32(e))
	return ret
}

func (e TimeSlot) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(e.Bytes()))
}

func (e TimeSlot) LogicalTimeAtBeginning() LogicalTime {
	return MustNewLogicalTime(e, 0)
}

func (t LogicalTime) TimeSlot() TimeSlot {
	return TimeSlot(binary.BigEndian.Uint32(t[:4]))
}

func (t LogicalTime) TimeTick() TimeTick {
	ret := TimeTick(t[4])
	util.Assertf(ret.Valid(), "invalid slot value")
	return ret
}

func (t LogicalTime) UnixNano() int64 {
	return BaselineTimeUnixNano +
		int64(t.TimeSlot())*int64(TimeSlotDuration()) + int64(TimeTickDuration())*int64(t.TimeTick())
}

func (t LogicalTime) Time() time.Time {
	return time.Unix(0, t.UnixNano())
}

func (t LogicalTime) NextTimeSlotBoundary() LogicalTime {
	if t.TimeTick() == 0 {
		return t
	}
	return MustNewLogicalTime(t.TimeSlot()+1, 0)
}

func (t LogicalTime) TimesTicksToNextSlotBoundary() int {
	if t.TimeTick() == 0 {
		return 0
	}
	return TimeTicksPerSlot - int(t.TimeTick())
}

func (t LogicalTime) Bytes() []byte {
	ret := t
	return ret[:]
}

func (t LogicalTime) String() string {
	return fmt.Sprintf("%d|%d", t.TimeSlot(), t.TimeTick())
}

func (t LogicalTime) Short() string {
	e := t.TimeSlot() % 1000
	return fmt.Sprintf(".%d|%d", e, t.TimeTick())
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
	slots1 := int64(t1.TimeSlot())*TimeTicksPerSlot + int64(t1.TimeTick())
	slots2 := int64(t2.TimeSlot())*TimeTicksPerSlot + int64(t2.TimeTick())
	return slots1 - slots2
}

// ValidTimePace checks if 2 timestamps have at least time pace slots in between
func ValidTimePace(t1, t2 LogicalTime) bool {
	return DiffTimeTicks(t2, t1) >= int64(TransactionTimePaceInTicks)
}

func (t LogicalTime) AddTimeTicks(s int) LogicalTime {
	util.Assertf(s >= 0, "AddTimeTicks: can't be negative argument")
	s1 := int64(t.TimeTick()) + int64(s)
	eRet := s1 / TimeTicksPerSlot
	sRet := s1 % TimeTicksPerSlot
	return MustNewLogicalTime(t.TimeSlot()+TimeSlot(eRet), TimeTick(sRet))
}

func (t LogicalTime) AddTimeSlots(e int) LogicalTime {
	return MustNewLogicalTime(t.TimeSlot()+TimeSlot(e), t.TimeTick())
}

func (t LogicalTime) AddDuration(d time.Duration) LogicalTime {
	return LogicalTimeFromTime(t.Time().Add(d))
}

func MaxLogicalTime(ts ...LogicalTime) LogicalTime {
	ret := MustNewLogicalTime(0, 0)
	for _, t := range ts {
		if t.After(ret) {
			ret = t
		}
	}
	return ret
}
