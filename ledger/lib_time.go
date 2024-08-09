package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/util"
)

// TODO refactor to 256 ticks per slot
// TODO implement equal 32 bytes transaction and output IDs

const (
	SlotByteLength = 4
	TickByteLength = 1
	TimeByteLength = SlotByteLength + TickByteLength // bytes
	MaxSlot        = 0xffffffff >> 1                 // 1 most significant bit must be 0
)

func SlotsPerDay() int {
	return int(24 * time.Hour / SlotDuration())
}

func SlotsPerHour() int {
	return int(time.Hour / SlotDuration())
}

func TicksPerHour() int {
	return L().ID.TicksPerSlot() * SlotsPerHour()
}

func TickDuration() time.Duration {
	return L().ID.TickDuration
}

func SlotDuration() time.Duration {
	return time.Duration(L().ID.TicksPerSlot()) * TickDuration()
}

type (
	// Slot represents a particular time slot.
	// Starting slot 0 at genesis
	Slot uint32

	// Tick is enforced to be <= MaxTickValueInSlot of the ledger identity
	// Usually it is 100, can be up to 255
	Tick byte

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

func ValidTime(ts Time) bool {
	_, err := TimeFromBytes(ts[:])
	return err == nil
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
	return int(s) < L().ID.TicksPerSlot()
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

func (e Slot) TransactionIDPrefixes() (withSequencerFlag, withoutSequencerFlag []byte) {
	withSequencerFlag = e.Bytes()
	withoutSequencerFlag = e.Bytes()
	withSequencerFlag[0] |= SequencerTxFlagHigherByte
	return
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
	return L().ID.GenesisTimeUnixNano() +
		int64(t.Slot())*int64(SlotDuration()) + int64(TickDuration())*int64(t.Tick())
}

func (t Time) Time() time.Time {
	return time.Unix(0, t.UnixNano())
}

func (t Time) NextSlotBoundary() Time {
	if t.Tick() == 0 {
		return t
	}
	return MustNewLedgerTime(t.Slot()+1, 0)
}

func (t Time) TicksToNextSlotBoundary() int {
	if t.Tick() == 0 {
		return 0
	}
	return L().ID.TicksPerSlot() - int(t.Tick())
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

// DiffTicks returns difference in ticks between two timestamps:
// < 0 is t1 is before t2
// > 0 if t2 is before t1
// (i.e. t1 - t2)
func DiffTicks(t1, t2 Time) int64 {
	ticksPerSlot := L().ID.TicksPerSlot()
	slots1 := int64(t1.Slot())*int64(ticksPerSlot) + int64(t1.Tick())
	slots2 := int64(t2.Slot())*int64(ticksPerSlot) + int64(t2.Tick())
	return slots1 - slots2
}

// ValidTransactionPace return true is subsequent input and target non-sequencer tx timestamps make a valid pace
func ValidTransactionPace(t1, t2 Time) bool {
	return DiffTicks(t2, t1) >= int64(TransactionPace())
}

// ValidSequencerPace return true is subsequent input and target sequencer tx timestamps make a valid pace
func ValidSequencerPace(t1, t2 Time) bool {
	return DiffTicks(t2, t1) >= int64(TransactionPaceSequencer())
}

// AddTicks adds ticks to timestamp. ticks can be negative
func (t Time) AddTicks(ticks int) Time {
	util.Assertf(ticks >= 0, "AddTicks: can't be negative argument")
	s1 := int(t.Tick()) + ticks
	ticksPerSlot := L().ID.TicksPerSlot()
	eRet := s1 / ticksPerSlot
	sRet := s1 % ticksPerSlot
	return MustNewLedgerTime(t.Slot()+Slot(eRet), Tick(sRet))
}

// AddSlots adds slots to timestamp
func (t Time) AddSlots(e int) Time {
	return MustNewLedgerTime(t.Slot()+Slot(e), t.Tick())
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
