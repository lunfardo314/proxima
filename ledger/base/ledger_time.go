package base

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"

	"github.com/lunfardo314/easyfl/easyfl_util"
)

// ledger time-related definitions and functions

const (
	SlotByteLength = 4
	TickByteIndex
	SequencerBitMaskInTick = 0x01
	LedgerTimeByteLength   = SlotByteLength + 1 // bytes
	MaxSlot                = 0xffffffff         // the whole uint32 range is a valid slot
	MaxTickValue           = 0x7f               // 127
	MaxTime                = MaxSlot*TicksPerSlot + MaxTickValue
	TicksPerSlot           = MaxTickValue + 1
)

// serialized timestamp is 5 bytes:
// - bytes 0-3 is big-endian slot
// - byte 4 is ticks << 1, i.e. last bit of timestamp is always 0

type LedgerTime struct {
	Slot uint32
	Tick byte
}

var (
	NilLedgerTime      LedgerTime
	errWrongDataLength = fmt.Errorf("wrong data length")
	errWrongTickValue  = fmt.Errorf("wrong tick value")
)

// SlotFromBytes reads a big-endian slot. Every uint32 is a valid slot, so the
// length is the only thing to check.
func SlotFromBytes(data []byte) (ret uint32, err error) {
	if len(data) != 4 {
		err = errWrongDataLength
		return
	}
	return binary.BigEndian.Uint32(data), nil
}

// T creates new ledger time object
func T(slot uint32, t byte) (ret LedgerTime) {
	easyfl_util.Assertf(t <= MaxTickValue, "NewLedgerTime: invalid tick value %d", t)
	ret = LedgerTime{Slot: slot, Tick: t}
	return
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
	ret = T(binary.BigEndian.Uint32(data[:SlotByteLength]), data[TickByteIndex]>>1)
	return
}

// LedgerTimeFromTicksSinceGenesis converts absolute value of ticks since genesis into the time value
func LedgerTimeFromTicksSinceGenesis(ticks int64) (ret LedgerTime, err error) {
	if ticks < 0 || ticks > MaxTime {
		err = fmt.Errorf("TimeFromTicksSinceGenesis: wrong int64")
		return
	}
	ret = T(uint32(ticks/TicksPerSlot), byte(ticks%TicksPerSlot))
	return
}

func (t LedgerTime) IsSlotBoundary() bool {
	return t.Tick == 0 && t != NilLedgerTime
}

func (t LedgerTime) NextSlotBoundary() LedgerTime {
	if t.IsSlotBoundary() {
		return t
	}
	easyfl_util.Assertf(t.Slot < MaxSlot, "t.Slot < MaxSlot")
	return T(t.Slot+1, 0)
}

func (t LedgerTime) TicksToNextSlotBoundary() int {
	if t.IsSlotBoundary() {
		return 0
	}
	return TicksPerSlot - int(t.Tick)
}

func (t LedgerTime) Bytes() []byte {
	ret := make([]byte, LedgerTimeByteLength)
	binary.BigEndian.PutUint32(ret[:SlotByteLength], t.Slot)
	ret[TickByteIndex] = t.Tick << 1
	return ret[:]
}

// String returns the dashed form "<slot>-<tick>". The previous pipe form is StringLegacy.
func (t LedgerTime) String() string {
	return fmt.Sprintf("%d-%d", t.Slot, t.Tick)
}

// Short returns the dashed short form "<slot%1000>-<tick>". The previous dotted+pipe
// form is ShortLegacy.
func (t LedgerTime) Short() string {
	return fmt.Sprintf("%d-%d", t.Slot%1000, t.Tick)
}

// AsFileName matches String — the dashed form is filename-safe on Linux and Windows.
// The previous underscore form is AsFileNameLegacy.
func (t LedgerTime) AsFileName() string {
	return t.String()
}

// StringLegacy returns the original pipe-separated form "<slot>|<tick>".
func (t LedgerTime) StringLegacy() string {
	return fmt.Sprintf("%d|%d", t.Slot, t.Tick)
}

// ShortLegacy returns the original dot-prefixed short form ".<slot%1000>|<tick>".
func (t LedgerTime) ShortLegacy() string {
	return fmt.Sprintf(".%d|%d", t.Slot%1000, t.Tick)
}

// AsFileNameLegacy returns the original underscore-separated form "<slot>_<tick>".
func (t LedgerTime) AsFileNameLegacy() string {
	return fmt.Sprintf("%d_%d", t.Slot, t.Tick)
}

func (t LedgerTime) Source() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t.Bytes()))
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
	easyfl_util.AssertNoError(err)
	return ret
}

// AddSlots adds slots to timestamp
func (t LedgerTime) AddSlots(slot uint32) LedgerTime {
	return t.AddTicks(int(slot << 7))
}

func MaximumTime(ts ...LedgerTime) LedgerTime {
	// Inlined max-with-comparator: keeps base free of proxima/util,
	// which transitively drags x/text into the TinyGo wasm wallet
	// build. See claude/wasm_txbuilder.md Phase 6.
	var ret LedgerTime
	first := true
	for _, t := range ts {
		if first || ret.Before(t) {
			ret = t
			first = false
		}
	}
	return ret
}

func RandomSlot() uint32 {
	return rand.Uint32()
}

func RandomLedgerTime(ticks ...byte) (ret LedgerTime) {
	ret.Slot = RandomSlot()
	if len(ticks) > 0 {
		ret.Tick = ticks[0]
	}
	return
}

func Slot2Bytes(slot uint32) []byte {
	ret := make([]byte, SlotByteLength)
	binary.BigEndian.PutUint32(ret, slot)
	return ret
}
