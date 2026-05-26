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

	"github.com/lunfardo314/easyfl/easyfl_util"
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
	easyfl_util.Assertf(len(data) == TransactionIDLength, "MustTransactionIDFromBytes: 32 bytes expected, got %d", len(data))
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

// timestampPrefixStringLegacy is the prefix used by the bracket-form Legacy renderers.
// It uses the legacy "<slot>|<tick>" / ".<slot>|<tick>" time forms so the bracket form
// keeps its historical appearance regardless of how LedgerTime.String evolves.
func timestampPrefixStringLegacy(ts LedgerTime, seqMilestoneFlag bool, shortTimeSlot ...bool) string {
	var s string
	if seqMilestoneFlag {
		if ts.Tick == 0 {
			s = "br"
		} else {
			s = "sq"
		}
	}
	if len(shortTimeSlot) > 0 && shortTimeSlot[0] {
		return fmt.Sprintf("%s%s", ts.ShortLegacy(), s)
	}
	return fmt.Sprintf("%s%s", ts.StringLegacy(), s)
}

func timestampPrefixStringAsFileNameLegacy(ts LedgerTime, seqMilestoneFlag bool) string {
	var s string
	if seqMilestoneFlag {
		if ts.Tick == 0 {
			s = "br"
		} else {
			s = "sq"
		}
	}
	return fmt.Sprintf("%s%s", ts.AsFileNameLegacy(), s)
}

// Legacy bracket-form helpers. The default String/StringShort/StringVeryShort surface
// is now the dashed form (see StringDashed below); these helpers and the *Legacy*
// methods preserve the older "[<ts>]<hex>" form for tooling that still parses it.

func TransactionIDStringLegacy(ts LedgerTime, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s", timestampPrefixStringLegacy(ts, sequencerFlag), hex.EncodeToString(txHash[:]))
}

// prefix of 3 makes collisions

func TransactionIDStringLegacyShort(ts LedgerTime, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s..", timestampPrefixStringLegacy(ts, sequencerFlag), hex.EncodeToString(txHash[:6]))
}

func TransactionIDStringLegacyVeryShort(ts LedgerTime, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s..", timestampPrefixStringLegacy(ts, sequencerFlag, false), hex.EncodeToString(txHash[:4]))
}

// TransactionIDAsFileNameLegacy renders the original underscore-separated file-name form
// (e.g. "12345_0br_<hex>"). Kept for tooling that still parses or writes that layout.
func TransactionIDAsFileNameLegacy(ts LedgerTime, txHash []byte, sequencerFlag bool) string {
	return fmt.Sprintf("%s_%s", timestampPrefixStringAsFileNameLegacy(ts, sequencerFlag), hex.EncodeToString(txHash))
}

// String returns the dashed form (see StringDashed). The previous bracket form is
// available as StringLegacy.
func (txid *TransactionID) String() string {
	return txid.StringDashed()
}

func (txid *TransactionID) StringHex() string {
	if txid == nil {
		return "00"
	}
	return hex.EncodeToString(txid[:])
}

// StringShort delegates to StringDashedShort. The previous bracket form is StringLegacyShort.
func (txid *TransactionID) StringShort() string {
	return txid.StringDashedShort()
}

// StringVeryShort delegates to StringDashedVeryShort. The previous bracket form is StringLegacyVeryShort.
func (txid *TransactionID) StringVeryShort() string {
	return txid.StringDashedVeryShort()
}

// StringLegacy returns the original bracket form: [<ts>]<27-byte hex>.
func (txid *TransactionID) StringLegacy() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDStringLegacy(txid.Timestamp(), txid.ShortID(), txid.IsSequencerTransaction())
}

// StringLegacyShort returns the original bracket-form short variant.
func (txid *TransactionID) StringLegacyShort() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDStringLegacyShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerTransaction())
}

// StringLegacyVeryShort returns the original bracket-form very-short variant.
func (txid *TransactionID) StringLegacyVeryShort() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDStringLegacyVeryShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerTransaction())
}

// AsFileName returns the dashed full form, which is safe as a filename on both Linux and
// Windows (no reserved characters, no leading dash, no trailing dot).
func (txid *TransactionID) AsFileName() string {
	return txid.StringDashed()
}

// AsFileNameShort returns a shortened, filename-safe dashed form: <prefix><slot>-<tick>-<8 hex chars>.
// Unlike StringDashedVeryShort it omits the trailing ".." (Windows strips trailing dots).
func (txid *TransactionID) AsFileNameShort() string {
	ts := txid.Timestamp()
	short := txid.ShortID()
	return fmt.Sprintf("%s%d-%d-%s", dashedSeqPrefix(txid.IsSequencerTransaction()), ts.Slot, ts.Tick, hex.EncodeToString(short[:4]))
}

// AsFileNameLegacy returns the original underscore-separated form: <slot>_<tick>[br|sq]_<hex>.
func (txid *TransactionID) AsFileNameLegacy() string {
	id := txid.ShortID()
	return TransactionIDAsFileNameLegacy(txid.Timestamp(), id[:], txid.IsSequencerTransaction())
}

// AsFileNameLegacyShort returns the legacy form with the 4-byte hash prefix.
func (txid *TransactionID) AsFileNameLegacyShort() string {
	id := txid.ShortID()
	return TransactionIDAsFileNameLegacy(txid.Timestamp(), id[:4], txid.IsSequencerTransaction())
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
	easyfl_util.AssertNoError(err)
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
	easyfl_util.AssertNoError(err)
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

// String returns the dashed form (see StringDashed). The previous bracket form is
// available as StringLegacy.
func (oid *OutputID) String() string {
	return oid.StringDashed()
}

func (oid *OutputID) StringHex() string {
	return hex.EncodeToString(oid[:])
}

// StringShort delegates to StringDashedShort. The previous bracket form is StringLegacyShort.
func (oid *OutputID) StringShort() string {
	return oid.StringDashedShort()
}

// StringVeryShort delegates to StringDashedVeryShort. The previous bracket form is StringLegacyVeryShort.
func (oid *OutputID) StringVeryShort() string {
	return oid.StringDashedVeryShort()
}

// StringLegacy returns the original bracket form: <txid StringLegacy>[<idx>].
func (oid *OutputID) StringLegacy() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.StringLegacy(), oid.Index())
}

// StringLegacyShort returns the original bracket-form short variant.
func (oid *OutputID) StringLegacyShort() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.StringLegacyShort(), oid.Index())
}

// StringLegacyVeryShort returns the original bracket-form very-short variant.
func (oid *OutputID) StringLegacyVeryShort() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.StringLegacyVeryShort(), oid.Index())
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

// StringDashed returns a deterministically parseable human-readable form of the transaction ID:
//
//	[s]<slot>-<tick>-<hex of TransactionIDShort, 27 bytes = 54 hex chars>
//
// The leading 's' indicates the sequencer bit is set (sequencer transactions, including
// branches). Non-sequencer transactions have no prefix. The 27-byte hash part starts with
// the maxOutputIndex byte followed by the 26-byte blake2b hash tail.
func (txid *TransactionID) StringDashed() string {
	if txid == nil {
		return "<nil>"
	}
	ts := txid.Timestamp()
	short := txid.ShortID()
	return fmt.Sprintf("%s%d-%d-%s", dashedSeqPrefix(txid.IsSequencerTransaction()), ts.Slot, ts.Tick, hex.EncodeToString(short[:]))
}

// StringDashedShort is the non-parseable shortened form: keeps the first 6 bytes of
// the 27-byte short ID (maxOutputIndex byte + 5 hash bytes).
func (txid *TransactionID) StringDashedShort() string {
	if txid == nil {
		return "<nil>"
	}
	ts := txid.Timestamp()
	short := txid.ShortID()
	return fmt.Sprintf("%s%d-%d-%s..", dashedSeqPrefix(txid.IsSequencerTransaction()), ts.Slot, ts.Tick, hex.EncodeToString(short[:6]))
}

// StringDashedVeryShort is the non-parseable very-short form: keeps the first 4 bytes
// of the 27-byte short ID (maxOutputIndex byte + 3 hash bytes). Collisions possible.
func (txid *TransactionID) StringDashedVeryShort() string {
	if txid == nil {
		return "<nil>"
	}
	ts := txid.Timestamp()
	short := txid.ShortID()
	return fmt.Sprintf("%s%d-%d-%s..", dashedSeqPrefix(txid.IsSequencerTransaction()), ts.Slot, ts.Tick, hex.EncodeToString(short[:4]))
}

func dashedSeqPrefix(isSeq bool) string {
	if isSeq {
		return "s"
	}
	return ""
}

// TransactionIDFromStringDashed parses the format produced by StringDashed
func TransactionIDFromStringDashed(s string) (ret TransactionID, err error) {
	if len(s) == 0 {
		return ret, errors.New("TransactionIDFromStringDashed: empty string")
	}
	rest := s
	var isSeq bool
	if s[0] == 's' {
		isSeq = true
		rest = s[1:]
	}

	parts := strings.SplitN(rest, "-", 3)
	if len(parts) != 3 {
		return ret, errors.New("TransactionIDFromStringDashed: expected <slot>-<tick>-<hex> after optional 's' prefix")
	}

	slot, e := strconv.ParseUint(parts[0], 10, 32)
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromStringDashed: bad slot: %w", e)
	}
	tick, e := strconv.ParseUint(parts[1], 10, 8)
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromStringDashed: bad tick: %w", e)
	}
	shortBytes, e := hex.DecodeString(parts[2])
	if e != nil {
		return ret, fmt.Errorf("TransactionIDFromStringDashed: bad hash hex: %w", e)
	}
	if len(shortBytes) != TransactionIDShortLength {
		return ret, fmt.Errorf("TransactionIDFromStringDashed: hash must be %d bytes, got %d", TransactionIDShortLength, len(shortBytes))
	}

	ts := T(uint32(slot), byte(tick))
	var h TransactionIDShort
	copy(h[:], shortBytes)
	return NewTransactionID(ts, h, isSeq), nil
}

// StringDashed returns the parseable form of the output ID: <txid StringDashed>#<output index>
func (oid *OutputID) StringDashed() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s#%d", txid.StringDashed(), oid.Index())
}

// StringDashedShort is the non-parseable shortened form of the output ID
func (oid *OutputID) StringDashedShort() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s#%d", txid.StringDashedShort(), oid.Index())
}

// StringDashedVeryShort is the non-parseable very-short form of the output ID
func (oid *OutputID) StringDashedVeryShort() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s#%d", txid.StringDashedVeryShort(), oid.Index())
}

// OutputIDFromStringDashed parses the format produced by OutputID.StringDashed
func OutputIDFromStringDashed(s string) (ret OutputID, err error) {
	hashIdx := strings.LastIndex(s, "#")
	if hashIdx < 0 {
		return ret, errors.New("OutputIDFromStringDashed: no '#' found")
	}
	txid, e := TransactionIDFromStringDashed(s[:hashIdx])
	if e != nil {
		return ret, fmt.Errorf("OutputIDFromStringDashed: %w", e)
	}
	idx, e := strconv.ParseUint(s[hashIdx+1:], 10, 8)
	if e != nil {
		return ret, fmt.Errorf("OutputIDFromStringDashed: bad output index: %w", e)
	}
	return MustNewOutputID(txid, byte(idx)), nil
}
