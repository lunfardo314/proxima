package base

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const (
	TransactionIDShortLength     = 27
	TransactionIDLength          = LedgerTimeByteLength + TransactionIDShortLength
	OutputIDLength               = TransactionIDLength + 1
	ChainIDLength                = 32
	MaxOutputIndexPositionInTxID = 5
)

type (
	// TransactionIDShort
	// byte 0 is maximum index of produced outputs
	// the rest 26 bytes is bytes [1:28] (26 bytes) of the blake2b 32-byte hash of transaction bytes
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

var NilTransactionID TransactionID

func NewTransactionID(ts LedgerTime, h TransactionIDShort, sequencerTxFlag bool) (ret TransactionID) {
	copy(ret[:LedgerTimeByteLength], ts.Bytes())
	copy(ret[LedgerTimeByteLength:], h[:])
	if sequencerTxFlag {
		ret[TickByteIndex] |= SequencerBitMaskInTick
	}
	return
}

func MustTransactionIDFromBytes(data []byte) (ret TransactionID) {
	util.Assertf(len(data) == TransactionIDLength, "32 bytes exected")
	copy(ret[:], data)
	return
}

func TransactionIDFromBytes(data []byte) (ret TransactionID, err error) {
	if len(data) != TransactionIDLength {
		err = errors.New("TransactionIDFromBytes: wrong data length")
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

func (txid *TransactionID) NumProducedOutputs() int {
	return int(txid[MaxOutputIndexPositionInTxID]) + 1
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

func (txid *TransactionID) Slot() Slot {
	return Slot(binary.BigEndian.Uint32(txid[:SlotByteLength]))
}

func (txid *TransactionID) Tick() Tick {
	return Tick(txid[TickByteIndex] >> 1)
}

func (txid *TransactionID) IsSequencerMilestone() bool {
	return txid[TickByteIndex]&SequencerBitMaskInTick != 0
}

func (txid *TransactionID) IsBranchTransaction() bool {
	return txid.IsSequencerMilestone() && txid.Tick() == 0
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
	return fmt.Sprintf("[%s]%s..", timestampPrefixString(ts, sequencerFlag, true), hex.EncodeToString(txHash[:4]))
}

func TransactionIDAsFileName(ts LedgerTime, txHash []byte, sequencerFlag, branchFlag bool) string {
	return fmt.Sprintf("%s_%s", timestampPrefixStringAsFileName(ts, sequencerFlag, branchFlag), hex.EncodeToString(txHash))
}

func (txid *TransactionID) String() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDString(txid.Timestamp(), txid.ShortID(), txid.IsSequencerMilestone())
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
	return TransactionIDStringShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerMilestone())
}

func (txid *TransactionID) StringVeryShort() string {
	if txid == nil {
		return "<nil>"
	}
	return TransactionIDStringVeryShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerMilestone())
}

func (txid *TransactionID) AsFileName() string {
	id := txid.ShortID()
	return TransactionIDAsFileName(txid.Timestamp(), id[:], txid.IsSequencerMilestone(), txid.IsBranchTransaction())
}

func (txid *TransactionID) AsFileNameShort() string {
	id := txid.ShortID()
	prefix4 := id[:4]
	return TransactionIDAsFileName(txid.Timestamp(), prefix4[:], txid.IsSequencerMilestone(), txid.IsBranchTransaction())
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
	return ret, nil
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

func (oid *OutputID) Slot() Slot {
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
