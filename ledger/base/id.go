package base

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	rand2 "math/rand"
	"strconv"
	"strings"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const (
	TransactionHashLength        = 26
	TransactionIDShortLength     = TransactionHashLength + 1
	TransactionIDLength          = LedgerTimeByteLength + TransactionIDShortLength
	OutputIDLength               = TransactionIDLength + 1
	ChainIDLength                = 32
	MaxOutputIndexPositionInTxID = 5
)

type (
	// TransactionHash is last 26 bytes of the blake2b hash of the transaction essence bytes
	TransactionHash [TransactionHashLength]byte
	// TransactionIDShort
	// byte 0 is maximum index of produced outputs
	// the rest 26 bytes is bytes of the TransactionHash
	TransactionIDShort [TransactionIDShortLength]byte
	// TransactionIDVeryShort4 is first 4 bytes of TransactionIDShort.
	// Warning. Collisions cannot be ruled out
	TransactionIDVeryShort4 [4]byte
	// TransactionIDVeryShort8 is first 8 bytes of TransactionIDShort.
	// Warning. Collisions cannot be ruled out
	TransactionIDVeryShort8 [8]byte
	// TransactionID :
	// [0:5] - timestamp bytes
	// [5:32] TransactionIDShort

	// TransactionID is concatenation of <txid prefix> and TransactionIDShort
	// <txid prefix> is 5 bytes prefix = tx timestamp 5 bytes with sequencer flag bit set in last bit of the last bytes
	TransactionID [TransactionIDLength]byte
	OutputID      [OutputIDLength]byte
	// ChainID all-0 for origin
	ChainID [ChainIDLength]byte
)

func NewTransactionID(ts LedgerTime, h TransactionIDShort, sequencerTxFlag bool) (ret TransactionID) {
	copy(ret[:LedgerTimeByteLength], ts.Bytes())
	copy(ret[LedgerTimeByteLength:], h[:])
	if sequencerTxFlag {
		ret[TickByteIndex] |= SequencerBitMaskInTick
	}
	return
}

func MustTransactionIDFromBytes(data []byte) (ret TransactionID) {
	util.Assertf(len(data) == TransactionIDLength, "MustTransactionIDFromBytes: 32 bytes expected, got %d", len(data))
	copy(ret[:], data)
	return
}

func TransactionIDFromBytes(data []byte) (ret TransactionID, err error) {
	if len(data) != TransactionIDLength {
		err = fmt.Errorf("TransactionIDFromBytes: 32 bytes expected, got %d", len(data))
		return
	}
	copy(ret[:], data)
	return
}

func TransactionIDFromHexString(str string) (ret TransactionID, err error) {
	var data []byte
	if data, err = hex.DecodeString(str); err != nil {
		return
	}
	ret, err = TransactionIDFromBytes(data)
	return
}

// RandomTransactionID not completely random. For testing
func RandomTransactionID(sequencerFlag bool, maxOutIdx byte, timestamp ...LedgerTime) TransactionID {
	var hash TransactionIDShort
	_, _ = rand.Read(hash[:])
	hash[0] = maxOutIdx
	ts := RandomLedgerTime()
	if len(timestamp) > 0 {
		ts = timestamp[0]
	}
	return NewTransactionID(ts, hash, sequencerFlag)
}

func RandomOutputID(ts LedgerTime) OutputID {
	rndOutCount := byte(rand2.Intn(256))
	idx := byte(rand2.Intn(int(rndOutCount) + 1))
	return MustNewOutputID(RandomTransactionID(false, rndOutCount, ts), idx)
}

func (txid *TransactionID) NumProducedOutputs() int {
	return int(txid[MaxOutputIndexPositionInTxID]) + 1
}

func (txid *TransactionID) TransactionHash() (ret TransactionHash) {
	copy(ret[:], txid[TransactionIDLength-TransactionHashLength:TransactionHashLength])
	return
}

// ShortID return hash part of id
func (txid *TransactionID) ShortID() (ret TransactionIDShort) {
	copy(ret[:], txid[LedgerTimeByteLength:])
	return
}

// VeryShortID4 returns last 4 bytes of the ShortID, i.e. of the hash
// Collisions cannot be ruled out! Intended use is in Bloom filtering, when false positives are acceptable
func (txid *TransactionID) VeryShortID4() (ret TransactionIDVeryShort4) {
	copy(ret[:], txid[TransactionIDLength-4:])
	return
}

// VeryShortID8 returns last 8 bytes of the ShortID, i.e. of the hash
// Collisions cannot be ruled out! Intended use is in Bloom filtering, when false positives are acceptable
func (txid *TransactionID) VeryShortID8() (ret TransactionIDVeryShort8) {
	copy(ret[:], txid[TransactionIDLength-8:])
	return
}

func (txid *TransactionID) Timestamp() (ret LedgerTime) {
	ret.Slot = txid.Slot()
	ret.Tick = txid.Tick()
	return
}

func (txid *TransactionID) Slot() uint32 {
	return binary.BigEndian.Uint32(txid[:SlotByteLength])
}

func (txid *TransactionID) Tick() byte {
	return txid[TickByteIndex] >> 1
}

func (txid *TransactionID) IsSequencerTransaction() bool {
	return txid[TickByteIndex]&SequencerBitMaskInTick != 0
}

func (txid *TransactionID) IsBranchTransaction() bool {
	return txid.IsSequencerTransaction() && txid.Tick() == 0
}

func (txid *TransactionID) Bytes() []byte {
	return txid[:]
}

func timestampPrefixString(ts LedgerTime, seqMilestoneFlag bool, shortTimeSlot ...bool) string {
	var s string
	if seqMilestoneFlag {
		if ts.Tick == 0 {
			s = "br"
		} else {
			s = "sq"
		}
	}
	if len(shortTimeSlot) > 0 && shortTimeSlot[0] {
		return fmt.Sprintf("%s%s", ts.Short(), s)
	}
	return fmt.Sprintf("%s%s", ts.String(), s)
}

func timestampPrefixStringAsFileName(ts LedgerTime, seqMilestoneFlag bool, shortTimeSlot ...bool) string {
	var s string
	if seqMilestoneFlag {
		if ts.Tick == 0 {
			s = "br"
		} else {
			s = "sq"
		}
	}
	if len(shortTimeSlot) > 0 && shortTimeSlot[0] {
		return fmt.Sprintf("%s%s", ts.AsFileName(), s)
	}
	return fmt.Sprintf("%s%s", ts.AsFileName(), s)
}

func TransactionIDString(ts LedgerTime, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s", timestampPrefixString(ts, sequencerFlag), hex.EncodeToString(txHash[:]))
}

// prefix of 3 makes collisions

func TransactionIDStringShort(ts LedgerTime, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s..", timestampPrefixString(ts, sequencerFlag), hex.EncodeToString(txHash[:6]))
}

func TransactionIDStringVeryShort(ts LedgerTime, txHash TransactionIDShort, sequencerFlag bool) string {
	//return fmt.Sprintf("[%s]%s..", timestampPrefixString(ts, sequencerFlag, true), hex.EncodeToString(txHash[:4]))
	return fmt.Sprintf("[%s]%s..", timestampPrefixString(ts, sequencerFlag, false), hex.EncodeToString(txHash[:4]))
}

func TransactionIDAsFileName(ts LedgerTime, txHash []byte, sequencerFlag, branchFlag bool) string {
	return fmt.Sprintf("%s_%s", timestampPrefixStringAsFileName(ts, sequencerFlag, branchFlag), hex.EncodeToString(txHash))
}

func (txid *TransactionID) String() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDString(txid.Timestamp(), txid.ShortID(), txid.IsSequencerTransaction())
}

func (txid *TransactionID) StringHex() string {
	if txid == nil {
		return "00"
	}
	return hex.EncodeToString(txid[:])
}

func (txid *TransactionID) StringShort() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDStringShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerTransaction())
}

func (txid *TransactionID) StringVeryShort() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDStringVeryShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerTransaction())
}

func (txid *TransactionID) AsFileName() string {
	id := txid.ShortID()
	return TransactionIDAsFileName(txid.Timestamp(), id[:], txid.IsSequencerTransaction(), txid.IsBranchTransaction())
}

func (txid *TransactionID) AsFileNameShort() string {
	id := txid.ShortID()
	prefix4 := id[:4]
	return TransactionIDAsFileName(txid.Timestamp(), prefix4[:], txid.IsSequencerTransaction(), txid.IsBranchTransaction())
}

// LessTxID comparison is lexicographical. It coincides with the order of timestamps.
// Sorting by txid is equivalent to the topological sorting of vertices of the UTXO tangle
func LessTxID(txid1, txid2 TransactionID) bool {
	return bytes.Compare(txid1[:], txid2[:]) < 0
}

func NewOutputID(id TransactionID, idx byte) (ret OutputID, err error) {
	if int(idx) > id.NumProducedOutputs() {
		return OutputID{}, fmt.Errorf("wrong output index")
	}
	copy(ret[:TransactionIDLength], id[:])
	ret[TransactionIDLength] = idx
	return
}

func MustNewOutputID(id TransactionID, idx byte) OutputID {
	ret, err := NewOutputID(id, idx)
	util.AssertNoError(err)
	return ret
}

func OutputIDFromBytes(data []byte) (ret OutputID, err error) {
	if len(data) != OutputIDLength {
		err = fmt.Errorf("OutputIDFromBytes: wrong data length %d", len(data))
		return
	}
	copy(ret[:], data)

	if ret[OutputIDLength-1] > data[MaxOutputIndexPositionInTxID] {
		err = fmt.Errorf("OutputIDFromBytes: wrong output index in %s", ret.String())
		return
	}
	return
}

func OutputIDFromHexString(str string) (ret OutputID, err error) {
	var data []byte
	if data, err = hex.DecodeString(str); err != nil {
		return
	}
	return OutputIDFromBytes(data)
}

func MustOutputIndexFromIDBytes(data []byte) byte {
	ret, err := OutputIDIndexFromBytes(data)
	util.AssertNoError(err)
	return ret
}

// OutputIDIndexFromBytes optimizes memory usage
func OutputIDIndexFromBytes(data []byte) (ret byte, err error) {
	if len(data) != OutputIDLength {
		err = errors.New("OutputIDIndexFromBytes: wrong data length")
		return
	}
	ret = data[TransactionIDLength]
	if ret > data[MaxOutputIndexPositionInTxID] {
		err = errors.New("OutputIDIndexFromBytes: wrong output index")
	}
	return
}

func (oid *OutputID) IsSequencerTransaction() bool {
	return oid[TickByteIndex]&SequencerBitMaskInTick != 0
}

func (oid *OutputID) IsBranchTransaction() bool {
	return oid.IsSequencerTransaction() && oid[TickByteIndex]>>1 == 0
}

func (oid *OutputID) String() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.String(), oid.Index())
}

func (oid *OutputID) StringHex() string {
	return hex.EncodeToString(oid[:])
}

func (oid *OutputID) StringShort() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.StringShort(), oid.Index())
}

func (oid *OutputID) StringVeryShort() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.StringVeryShort(), oid.Index())
}

func (oid *OutputID) TransactionID() (ret TransactionID) {
	copy(ret[:], oid[:TransactionIDLength])
	return
}

func (oid *OutputID) Timestamp() LedgerTime {
	ret := oid.TransactionID()
	return ret.Timestamp()
}

func (oid *OutputID) Slot() uint32 {
	ret := oid.TransactionID()
	return ret.Slot()
}

func (oid *OutputID) TransactionHash() (ret TransactionIDShort) {
	copy(ret[:], oid[LedgerTimeByteLength:TransactionIDLength])
	return
}

func (oid *OutputID) Index() byte {
	return oid[TransactionIDLength]
}

func (oid *OutputID) Valid() bool {
	txid := oid.TransactionID()
	return int(oid.Index()) < txid.NumProducedOutputs()
}

func (oid *OutputID) Bytes() []byte {
	return oid[:]
}

// ChainID

var NilChainID ChainID

func (id *ChainID) Bytes() []byte {
	return id[:]
}

func (id *ChainID) String() string {
	return fmt.Sprintf("$/%s", hex.EncodeToString(id[:]))
}

func (id *ChainID) StringHex() string {
	return hex.EncodeToString(id[:])
}

func (id *ChainID) StringShort() string {
	return fmt.Sprintf("$/%s..", hex.EncodeToString(id[:6]))
}

func (id *ChainID) StringVeryShort() string {
	return fmt.Sprintf("$/%s..", hex.EncodeToString(id[:3]))
}

func ChainIDFromBytes(data []byte) (ret ChainID, err error) {
	if len(data) != ChainIDLength {
		err = fmt.Errorf("ChainIDFromBytes: wrong data length %d", len(data))
		return
	}
	copy(ret[:], data)
	return
}

func ChainIDFromHexString(str string) (ret ChainID, err error) {
	data, err := hex.DecodeString(str)
	if err != nil {
		return [32]byte{}, err
	}
	return ChainIDFromBytes(data)
}

func RandomChainID() (ret ChainID) {
	_, _ = rand.Read(ret[:])
	return
}

func MakeOriginChainID(originOutputID OutputID) ChainID {
	return blake2b.Sum256(originOutputID[:])
}

// String2 returns a deterministically parseable human-readable form of the transaction ID:
// <prefix><slot>-<tick>-<maxOutputIndex>-<hash hex 26 bytes>
// prefix: 'b' for branch, 's' for sequencer non-branch, 't' for non-sequencer
func (txid *TransactionID) String2() string {
	if txid == nil {
		return "<nil>"
	}
	ts := txid.Timestamp()
	isSeq := txid.IsSequencerTransaction()
	var prefix byte
	switch {
	case isSeq && ts.Tick == 0:
		prefix = 'b'
	case isSeq:
		prefix = 's'
	default:
		prefix = 't'
	}
	maxOutIdx := txid[MaxOutputIndexPositionInTxID]
	hash := txid[TransactionIDLength-TransactionHashLength:]
	return fmt.Sprintf("%c%d-%d-%d-%s", prefix, ts.Slot, ts.Tick, maxOutIdx, hex.EncodeToString(hash))
}

// TransactionIDFromString2 parses the format produced by String2
func TransactionIDFromString2(s string) (ret TransactionID, err error) {
	if len(s) < 3 {
		return ret, errors.New("TransactionIDFromString2: string too short")
	}
	prefix := s[0]
	var isSeq bool
	switch prefix {
	case 'b':
		isSeq = true
	case 's':
		isSeq = true
	case 't':
		isSeq = false
	default:
		return ret, fmt.Errorf("TransactionIDFromString2: invalid prefix '%c'", prefix)
	}

	parts := strings.SplitN(s[1:], "-", 4)
	if len(parts) != 4 {
		return ret, errors.New("TransactionIDFromString2: expected 4 dash-separated fields after prefix")
	}

	slot, e := strconv.ParseUint(parts[0], 10, 32)
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromString2: bad slot: %w", e)
	}
	tick, e := strconv.ParseUint(parts[1], 10, 8)
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromString2: bad tick: %w", e)
	}
	maxOutIdx, e := strconv.ParseUint(parts[2], 10, 8)
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromString2: bad maxOutputIndex: %w", e)
	}
	hashBytes, e := hex.DecodeString(parts[3])
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromString2: bad hash hex: %w", e)
	}
	if len(hashBytes) != TransactionHashLength {
		return ret, fmt.Errorf("TransactionIDFromString2: hash must be %d bytes, got %d", TransactionHashLength, len(hashBytes))
	}

	// validate prefix vs tick consistency
	if prefix == 'b' && tick != 0 {
		return ret, fmt.Errorf("TransactionIDFromString2: branch prefix 'b' but tick=%d (must be 0)", tick)
	}

	ts := T(uint32(slot), byte(tick))
	var h TransactionIDShort
	h[0] = byte(maxOutIdx)
	copy(h[1:], hashBytes)
	return NewTransactionID(ts, h, isSeq), nil
}

// String2 returns a deterministically parseable human-readable form of the output ID:
// <txid String2>-<output index>
func (oid *OutputID) String2() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s-%d", txid.String2(), oid.Index())
}

// OutputIDFromString2 parses the format produced by OutputID.String2
func OutputIDFromString2(s string) (ret OutputID, err error) {
	// find last '-' — that's the output index separator
	lastDash := strings.LastIndex(s, "-")
	if lastDash < 0 {
		return ret, errors.New("OutputIDFromString2: no dash found")
	}
	txidStr := s[:lastDash]
	idxStr := s[lastDash+1:]

	txid, e := TransactionIDFromString2(txidStr)
	if e != nil {
		return ret, fmt.Errorf("OutputIDFromString2: %w", e)
	}
	idx, e := strconv.ParseUint(idxStr, 10, 8)
	if e != nil {
		return ret, fmt.Errorf("OutputIDFromString2: bad output index: %w", e)
	}
	return MustNewOutputID(txid, byte(idx)), nil
}
