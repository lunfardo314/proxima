package ledger

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/util"
)

const (
	SlotByteLength = 4
	TimeByteLength = SlotByteLength + 1 // bytes
	MaxSlot        = 0xffffffff >> 1    // 1 most significant bit must be 0
	MaxTick        = 0xff
	MaxTime        = (MaxSlot << 8) | MaxTick
	TicksPerSlot   = MaxTick + 1
)

func TickDuration() time.Duration {
	return L().ID.TickDuration
}

func SlotDuration() time.Duration {
	return L().ID.SlotDuration()
}

type (
	// Slot represents a particular time slot.
	// Starting slot 0 at genesis
	Slot uint32

	// Time (ledger time) is 5 bytes:
	// - bytes [0:3] is slot (big endian). Most significant of the byte 0 in the slot must be 0
	// - byte 4 is tick
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
	if 0b10000000&data[0] != 0 {
		return 0, fmt.Errorf("most significant bit must be 0")
	}
	return Slot(binary.BigEndian.Uint32(data)), nil
}

func NewLedgerTime(slot Slot, t uint8) (ret Time) {
	util.Assertf(slot <= MaxSlot, "NewLedgerTime: slot <= MaxSlot")
	binary.BigEndian.PutUint32(ret[:4], uint32(slot))
	ret[4] = t
	return
}

func TimeFromClockTime(nowis time.Time) Time {
	return L().ID.LedgerTimeFromClockTime(nowis)
}

func TimeNow() Time {
	return TimeFromClockTime(time.Now())
}

func ValidTime(ts Time) bool {
	_, err := TimeFromBytes(ts[:])
	return err == nil
}

func ValidSlot(slot Slot) bool {
	return slot <= MaxSlot
}

func TimeFromBytes(data []byte) (ret Time, err error) {
	if len(data) != TimeByteLength {
		err = errWrongDataLength
		return
	}
	var slot Slot
	slot, err = SlotFromBytes(data[:4])
	if err != nil {
		return
	}
	ret = NewLedgerTime(slot, data[TimeByteLength-1])
	return
}

// TimeFromTicksSinceGenesis converts absolute value of ticks since genesis into the time value
func TimeFromTicksSinceGenesis(ticks int64) (ret Time, err error) {
	if ticks < 0 || ticks > MaxTime {
		err = fmt.Errorf("TimeFromTicksSinceGenesis: wrong int64")
		return
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(ticks))
	copy(ret[:], buf[3:])
	return
}

func (s Slot) Bytes() []byte {
	ret := make([]byte, SlotByteLength)
	binary.BigEndian.PutUint32(ret, uint32(s))
	return ret
}

func (s Slot) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(s.Bytes()))
}

func (s Slot) TransactionIDPrefixes() (withSequencerFlag, withoutSequencerFlag []byte) {
	withSequencerFlag = s.Bytes()
	withoutSequencerFlag = s.Bytes()
	withSequencerFlag[0] |= SequencerTxFlagHigherByte
	return
}

func (t Time) Slot() Slot {
	return Slot(binary.BigEndian.Uint32(t[:4]))
}

func (t Time) Tick() uint8 {
	return t[TimeByteLength-1]
}

func (t Time) IsSlotBoundary() bool {
	return t.Tick() == 0 && t != NilLedgerTime
}

func (t Time) UnixNano() int64 {
	return L().ID.GenesisTimeUnixNano() +
		int64(t.Slot())*int64(SlotDuration()) + int64(TickDuration())*int64(t.Tick())
}

func (t Time) Time() time.Time {
	return time.Unix(0, t.UnixNano())
}

func (t Time) NextSlotBoundary() Time {
	if t.IsSlotBoundary() {
		return t
	}
	return NewLedgerTime(t.Slot()+1, 0)
}

func (t Time) TicksToNextSlotBoundary() int {
	if t.IsSlotBoundary() {
		return 0
	}
	return TicksPerSlot - int(t.Tick())
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
	return bytes.Compare(t[:], t1[:]) > 0
	//return DiffTicks(t, t1) > 0
}

func (t Time) AfterOrEqual(t1 Time) bool {
	return !t.Before(t1)
}

func (t Time) Before(t1 Time) bool {
	return bytes.Compare(t[:], t1[:]) < 0
	//return DiffTicks(t, t1) < 0
}

func (t Time) BeforeOrEqual(t1 Time) bool {
	return !t.After(t1)
}

func (t Time) Hex() string {
	return fmt.Sprintf("0x%s", hex.EncodeToString(t[:]))
}

func (t Time) TicksSinceGenesis() int64 {
	return int64(t.Slot())<<8 + int64(t.Tick())
}

// DiffTicks returns difference in ticks between two timestamps:
// < 0 is t1 is before t2
// > 0 if t2 is before t1
// (i.e. t1 - t2)
func DiffTicks(t1, t2 Time) int64 {
	return t1.TicksSinceGenesis() - t2.TicksSinceGenesis()
}

// ValidTransactionPace return true is subsequent input and target non-sequencer tx timestamps make a valid pace
func ValidTransactionPace(t1, t2 Time) bool {
	return DiffTicks(t2, t1) >= int64(TransactionPace())
}

// ValidSequencerPace return true is subsequent input and target sequencer tx timestamps make a valid pace
func ValidSequencerPace(t1, t2 Time) bool {
	return DiffTicks(t2, t1) >= int64(TransactionPaceSequencer())
}

// AddTicks adds ticks to timestamp. Ticks can be negative
func (t Time) AddTicks(ticks int) Time {
	ret, err := TimeFromTicksSinceGenesis(t.TicksSinceGenesis() + int64(ticks))
	util.AssertNoError(err)
	return ret
}

// AddSlots adds slots to timestamp
func (t Time) AddSlots(slot Slot) Time {
	util.Assertf(ValidSlot(slot), "ValidSlot(slot)")
	return t.AddTicks(int(slot << 8))
}

func MaximumTime(ts ...Time) Time {
	return util.Maximum(ts, func(ts1, ts2 Time) bool {
		return ts1.Before(ts2)
	})
}
