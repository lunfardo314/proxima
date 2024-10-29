package ledger

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const (
	TransactionIDShortLength     = 27
	TransactionIDLength          = TimeByteLength + TransactionIDShortLength
	OutputIDLength               = TransactionIDLength + 1
	ChainIDLength                = 32
	MaxOutputIndexPositionInTxID = 5

	SequencerTxFlagHigherByte = byte(0b10000000)
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
	TransactionID [TransactionIDLength]byte
	OutputID      [OutputIDLength]byte
	// ChainID all-0 for origin
	ChainID [ChainIDLength]byte
)

func TransactionIDShortFromTxBytes(txBytes []byte, maxOutputIndex byte) (ret TransactionIDShort) {
	h := blake2b.Sum256(txBytes)
	ret[0] = maxOutputIndex
	copy(ret[1:], h[:TransactionIDShortLength-1])
	return
}

func NewTransactionID(ts Time, h TransactionIDShort, sequencerTxFlag bool) (ret TransactionID) {
	if sequencerTxFlag {
		ts[0] = ts[0] | SequencerTxFlagHigherByte
	}
	copy(ret[:TimeByteLength], ts[:])
	copy(ret[TimeByteLength:], h[:])
	return
}

// NewTransactionIDPrefix used for database iteration by prefix, i.e. all transaction IDs of specific slot
func NewTransactionIDPrefix(slot Slot, sequencerTxFlag bool) (ret [4]byte) {
	copy(ret[:], slot.Bytes())
	if sequencerTxFlag {
		ret[0] = ret[0] | SequencerTxFlagHigherByte
	}
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
func RandomTransactionID(sequencerFlag bool) TransactionID {
	var hash TransactionIDShort
	_, _ = rand.Read(hash[:])
	return NewTransactionID(TimeNow(), hash, sequencerFlag)
}

func (txid *TransactionID) NumProducedOutputs() int {
	return int(txid[MaxOutputIndexPositionInTxID]) + 1
}

// ShortID return hash part of ID
func (txid *TransactionID) ShortID() (ret TransactionIDShort) {
	copy(ret[:], txid[TimeByteLength:])
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

func (txid *TransactionID) Timestamp() (ret Time) {
	copy(ret[:], txid[:TimeByteLength])
	ret[0] &= ^SequencerTxFlagHigherByte // erase 1 most significant bit of the first byte
	return
}

func (txid *TransactionID) Slot() Slot {
	return txid.Timestamp().Slot()
}

func (txid *TransactionID) IsSequencerMilestone() bool {
	return txid[0]&SequencerTxFlagHigherByte != 0
}

func (txid *TransactionID) IsBranchTransaction() bool {
	return txid[0]&SequencerTxFlagHigherByte != 0 && txid[4] == 0
}

func (txid *TransactionID) Bytes() []byte {
	return txid[:]
}

func timestampPrefixString(ts Time, seqMilestoneFlag bool, shortTimeSlot ...bool) string {
	isBranch := seqMilestoneFlag && ts.Tick() == 0
	var s string
	switch {
	case seqMilestoneFlag && isBranch:
		s = "br"
	case seqMilestoneFlag && !isBranch:
		s = "sq"
	case !seqMilestoneFlag && isBranch:
		s = "??"
	case !seqMilestoneFlag && !isBranch:
		s = ""
	}
	if len(shortTimeSlot) > 0 && shortTimeSlot[0] {
		return fmt.Sprintf("%s%s", ts.Short(), s)
	}
	return fmt.Sprintf("%s%s", ts.String(), s)
}

func timestampPrefixStringAsFileName(ts Time, seqMilestoneFlag, branchFlag bool, shortTimeSlot ...bool) string {
	var s string
	switch {
	case seqMilestoneFlag && branchFlag:
		s = "br"
	case seqMilestoneFlag && !branchFlag:
		s = "sq"
	case !seqMilestoneFlag && branchFlag:
		s = "??"
	case !seqMilestoneFlag && !branchFlag:
		s = ""
	}
	if len(shortTimeSlot) > 0 && shortTimeSlot[0] {
		return fmt.Sprintf("%s%s", ts.AsFileName(), s)
	}
	return fmt.Sprintf("%s%s", ts.AsFileName(), s)
}

func TransactionIDString(ts Time, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s", timestampPrefixString(ts, sequencerFlag), hex.EncodeToString(txHash[:]))
}

func TransactionIDStringShort(ts Time, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s..", timestampPrefixString(ts, sequencerFlag), hex.EncodeToString(txHash[:3]))
}

func TransactionIDStringVeryShort(ts Time, txHash TransactionIDShort, sequencerFlag bool) string {
	return fmt.Sprintf("[%s]%s..", timestampPrefixString(ts, sequencerFlag, true), hex.EncodeToString(txHash[:3]))
}

func TransactionIDAsFileName(ts Time, txHash []byte, sequencerFlag, branchFlag bool) string {
	return fmt.Sprintf("%s_%s", timestampPrefixStringAsFileName(ts, sequencerFlag, branchFlag), hex.EncodeToString(txHash))
}

func (txid *TransactionID) String() string {
	return TransactionIDString(txid.Timestamp(), txid.ShortID(), txid.IsSequencerMilestone())
}

func (txid *TransactionID) StringHex() string {
	return hex.EncodeToString(txid[:])
}

func (txid *TransactionID) StringShort() string {
	return TransactionIDStringShort(txid.Timestamp(), txid.ShortID(), txid.IsSequencerMilestone())
}

func (txid *TransactionID) StringVeryShort() string {
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

// LessTxID compares tx IDs b timestamp and by tx hash
func LessTxID(txid1, txid2 TransactionID) bool {
	if txid1.Timestamp().Before(txid2.Timestamp()) {
		return true
	}
	h1 := txid1.ShortID()
	h2 := txid2.ShortID()
	return bytes.Compare(h1[:], h2[:]) < 0
}

func TooCloseOnTimeAxis(txid1, txid2 *TransactionID) bool {
	if txid1.Timestamp().After(txid2.Timestamp()) {
		txid1, txid2 = txid2, txid1
	}
	if txid1.IsSequencerMilestone() && txid2.IsSequencerMilestone() {
		return !ValidSequencerPace(txid1.Timestamp(), txid2.Timestamp()) && *txid1 != *txid2
	}
	return !ValidTransactionPace(txid1.Timestamp(), txid2.Timestamp()) && *txid1 != *txid2
}

func NewOutputID(id *TransactionID, idx byte) (ret OutputID, err error) {
	if int(idx) > id.NumProducedOutputs() {
		return OutputID{}, fmt.Errorf("wrong output index")
	}
	copy(ret[:TransactionIDLength], id[:])
	ret[TransactionIDLength] = idx
	return
}

func MustNewOutputID(id *TransactionID, idx byte) OutputID {
	ret, err := NewOutputID(id, idx)
	util.AssertNoError(err)
	return ret
}

func OutputIDFromBytes(data []byte) (ret OutputID, err error) {
	if len(data) != OutputIDLength {
		err = errors.New("OutputIDFromBytes: wrong data length")
		return
	}
	if ret[OutputIDLength-1] > data[MaxOutputIndexPositionInTxID] {
		err = errors.New("OutputIDFromBytes: wrong output index")
		return
	}
	copy(ret[:], data)
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
	return oid[0]&SequencerTxFlagHigherByte != 0
}

func (oid *OutputID) IsBranchTransaction() bool {
	return oid[0]&SequencerTxFlagHigherByte != 0 && oid[4] == 0
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

func (oid *OutputID) Timestamp() Time {
	ret := oid.TransactionID()
	return ret.Timestamp()
}

func (oid *OutputID) Slot() Slot {
	ret := oid.TransactionID()
	return ret.Slot()
}

func (oid *OutputID) TransactionHash() (ret TransactionIDShort) {
	copy(ret[:], oid[TimeByteLength:TransactionIDLength])
	return
}

func (oid *OutputID) Index() byte {
	return oid[TransactionIDLength]
}

func (oid *OutputID) Bytes() []byte {
	return oid[:]
}

func EqualTransactionIDs(txid1, txid2 *TransactionID) bool {
	if txid1 == txid2 {
		return true
	}
	if txid1 == nil || txid2 == nil {
		return false
	}
	return *txid1 == *txid2
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

func (id *ChainID) AsChainLock() ChainLock {
	return ChainLockFromChainID(*id)
}

func (id *ChainID) AsAccountID() AccountID {
	return id.AsChainLock().AccountID()
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

func MakeOriginChainID(originOutputID *OutputID) ChainID {
	return blake2b.Sum256(originOutputID[:])
}
