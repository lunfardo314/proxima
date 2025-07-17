package base

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"

	"github.com/lunfardo314/proxima/util"
)

// ledger time-related definitions and functions

const (
	SlotByteLength = 4
	TickByteIndex
	SequencerBitMaskInTick = 0x01
	LedgerTimeByteLength   = SlotByteLength + 1 // bytes
	MaxSlot                = 0xffffffff         // 1 most significant bit must be 0
	MaxTickValue           = 0x7f               // 127
	MaxTime                = MaxSlot*TicksPerSlot + MaxTickValue
	TicksPerSlot           = MaxTickValue + 1
)

// serialized timestamp is 5 bytes:
// - bytes 0-3 is big-endian slot
// - byte 4 is ticks << 1, i.e. last bit of timestamp is always 0

type (
	// Slot represents a particular time slot.
	// Starting slot 0 at genesis
	Slot       uint32
	Tick       uint8
	LedgerTime struct {
		Slot
		Tick
	}
)

var (
	NilLedgerTime      LedgerTime
	errWrongDataLength = fmt.Errorf("wrong data length")
	errWrongTickValue  = fmt.Errorf("wrong tick value")
)

// SlotFromBytes enforces 2 most significant bits of the first byte are 0
func SlotFromBytes(data []byte) (ret Slot, err error) {
	if len(data) != 4 {
		err = errWrongDataLength
		return
	}
	return Slot(binary.BigEndian.Uint32(data)), nil
}

func NewLedgerTime(slot Slot, t Tick) (ret LedgerTime) {
	util.Assertf(t <= MaxTickValue, "NewLedgerTime: invalid tick value %d", t)
	ret = LedgerTime{Slot: slot, Tick: t}
	return
}

func T(slot Slot, t Tick) LedgerTime {
	return NewLedgerTime(slot, t)
}

func ValidTime(ts LedgerTime) bool {
	return ts.Tick <= MaxTickValue
}

func LedgerTimeFromBytes(data []byte) (ret LedgerTime, err error) {
	if len(data) != LedgerTimeByteLength {
		err = errWrongDataLength
		return
	}
	if data[TickByteIndex]&SequencerBitMaskInTick != 0 {
		err = errWrongTickValue
		return
	}
	ret = LedgerTime{
		Slot: Slot(binary.BigEndian.Uint32(data[:SlotByteLength])),
		Tick: Tick(data[TickByteIndex] >> 1),
	}
	return
}

// LedgerTimeFromTicksSinceGenesis converts absolute value of ticks since genesis into the time value
func LedgerTimeFromTicksSinceGenesis(ticks int64) (ret LedgerTime, err error) {
	if ticks < 0 || ticks > MaxTime {
		err = fmt.Errorf("TimeFromTicksSinceGenesis: wrong int64")
		return
	}
	ret = NewLedgerTime(Slot(ticks/TicksPerSlot), Tick(ticks%TicksPerSlot))
	return
}

func (s Slot) PutBytes(b []byte) {
	binary.BigEndian.PutUint32(b, uint32(s))
}

func (s Slot) Bytes() []byte {
	ret := make([]byte, SlotByteLength)
	s.PutBytes(ret)
	return ret
}

func (s Slot) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(s.Bytes()))
}

func (s Slot) Uint32() uint32 {
	return uint32(s)
}

func (t LedgerTime) IsSlotBoundary() bool {
	return t.Tick == 0 && t != NilLedgerTime
}

func (t LedgerTime) NextSlotBoundary() LedgerTime {
	if t.IsSlotBoundary() {
		return t
	}
	util.Assertf(t.Slot < MaxSlot, "t.Slot < MaxSlot")
	return NewLedgerTime(t.Slot+1, 0)
}

func (t LedgerTime) TicksToNextSlotBoundary() int {
	if t.IsSlotBoundary() {
		return 0
	}
	return TicksPerSlot - int(t.Tick)
}

func (t LedgerTime) Bytes() []byte {
	ret := make([]byte, LedgerTimeByteLength)
	binary.BigEndian.PutUint32(ret[:SlotByteLength], uint32(t.Slot))
	ret[TickByteIndex] = uint8(t.Tick) << 1
	return ret[:]
}

func (t LedgerTime) String() string {
	return fmt.Sprintf("%d|%d", t.Slot, t.Tick)
}

func (t LedgerTime) Source() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t.Bytes()))
}

func (t LedgerTime) AsFileName() string {
	return fmt.Sprintf("%d_%d", t.Slot, t.Tick)
}

func (t LedgerTime) Short() string {
	e := t.Slot % 1000
	return fmt.Sprintf(".%d|%d", e, t.Tick)
}

func (t LedgerTime) After(t1 LedgerTime) bool {
	return t.TicksSinceGenesis() > t1.TicksSinceGenesis()
}

func (t LedgerTime) AfterOrEqual(t1 LedgerTime) bool {
	return !t.Before(t1)
}

func (t LedgerTime) Before(t1 LedgerTime) bool {
	return t.TicksSinceGenesis() < t1.TicksSinceGenesis()
}

func (t LedgerTime) BeforeOrEqual(t1 LedgerTime) bool {
	return !t.After(t1)
}

func (t LedgerTime) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t.Bytes()))
}

func (t LedgerTime) TicksSinceGenesis() int64 {
	return int64(t.Slot)*TicksPerSlot + int64(t.Tick)
}

// DiffTicks returns difference in ticks between two timestamps:
// < 0 is t1 is before t2
// > 0 if t2 is before t1
// (i.e. t1 - t2)
func DiffTicks(t1, t2 LedgerTime) int64 {
	return t1.TicksSinceGenesis() - t2.TicksSinceGenesis()
}

// AddTicks adds ticks to timestamp. Ticks can be negative
func (t LedgerTime) AddTicks(ticks int) LedgerTime {
	ret, err := LedgerTimeFromTicksSinceGenesis(t.TicksSinceGenesis() + int64(ticks))
	util.AssertNoError(err)
	return ret
}

// AddSlots adds slots to timestamp
func (t LedgerTime) AddSlots(slot Slot) LedgerTime {
	return t.AddTicks(int(slot << 8))
}

func MaximumTime(ts ...LedgerTime) LedgerTime {
	return util.Maximum(ts, func(ts1, ts2 LedgerTime) bool {
		return ts1.Before(ts2)
	})
}

func RandomSlot() Slot {
	return Slot(rand.Uint32())
}

func RandomTick() Tick {
	return Tick(rand.Intn(256))
}

func RandomLedgerTime(ticks ...Tick) (ret LedgerTime) {
	ret.Slot = RandomSlot()
	if len(ticks) > 0 {
		ret.Tick = ticks[0]
	}
	return
}
