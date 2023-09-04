package core

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const (
	TransactionHashLength = 27
	TransactionIDLength   = LogicalTimeByteLength + TransactionHashLength
	OutputIDLength        = TransactionIDLength + 1

	SequencerTxFlagInTimeSlot = ^(TimeSlot(0xffffffff) >> 1)
	BranchTxFlagInTimeSlot    = SequencerTxFlagInTimeSlot >> 1
	SequencerTxFlagHigherByte = byte(0b10000000)
	BranchTxFlagHigherByte    = byte(0b01000000)
	TimeSlotMaskHigherByte    = SequencerTxFlagHigherByte | BranchTxFlagHigherByte
)

type (
	// TransactionHash is [0:28] of the blake2b 32-byte hash of transaction bytes
	TransactionHash [TransactionHashLength]byte
	// TransactionID :
	// [0:5] - timestamp bytes (4 bytes time slot big endian, 1 byte time tick)
	// [5:32] TransactionHash
	TransactionID [TransactionIDLength]byte
	OutputID      [OutputIDLength]byte
)

var All0TransactionHash = TransactionHash{}

func HashTransactionBytes(txBytes []byte) (ret TransactionHash) {
	h := blake2b.Sum256(txBytes)
	copy(ret[:], h[:TransactionHashLength])
	return
}

func NewTransactionID(ts LogicalTime, h TransactionHash, sequencerTxFlag, branchTxFlag bool) (ret TransactionID) {
	if sequencerTxFlag {
		ts[0] = ts[0] | SequencerTxFlagHigherByte
	}
	if branchTxFlag {
		ts[0] = ts[0] | BranchTxFlagHigherByte
	}
	copy(ret[:LogicalTimeByteLength], ts[:])
	copy(ret[LogicalTimeByteLength:], h[:])
	return
}

func TransactionIDFromBytes(data []byte) (ret TransactionID, err error) {
	if len(data) != TransactionIDLength {
		err = errors.New("TransactionIDFromBytes: wrong data length")
		return
	}
	if data[0]&TimeSlotMaskHigherByte == BranchTxFlagHigherByte {
		err = errors.New("TransactionIDFromBytes: inconsistent flags: branch flag can be on only if sequencer flag is on")
		return
	}
	copy(ret[:], data)
	return
}

func (txid *TransactionID) TransactionHash() (ret TransactionHash) {
	copy(ret[:], txid[LogicalTimeByteLength:])
	return
}

func (txid *TransactionID) Timestamp() (ret LogicalTime) {
	copy(ret[:], txid[:LogicalTimeByteLength])
	ret[0] &= 0b00111111 // erase 2 most significant bits of the first byte
	return
}

func (txid *TransactionID) TimeSlot() TimeSlot {
	return txid.Timestamp().TimeSlot()
}

func (txid *TransactionID) TimeTick() TimeTick {
	return txid.Timestamp().TimeTick()
}

func (txid *TransactionID) SequencerFlagON() bool {
	return txid[0]&SequencerTxFlagHigherByte != 0
}

func (txid *TransactionID) BranchFlagON() bool {
	return txid[0]&BranchTxFlagHigherByte != 0
}

func (txid *TransactionID) Bytes() []byte {
	return txid[:]
}

func timestampPrefixString(ts LogicalTime, seqMilestoneFlag, branchFlag bool, shortTimeSlot ...bool) string {
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
		return fmt.Sprintf("[%s%s]", ts.Short(), s)
	}
	return fmt.Sprintf("[%s%s]", ts.String(), s)
}

func TransactionIDString(ts LogicalTime, txHash TransactionHash, sequencerFlag, branchFlag bool) string {
	return fmt.Sprintf("%s%s", timestampPrefixString(ts, sequencerFlag, branchFlag), hex.EncodeToString(txHash[:]))
}

func TransactionIDShort(ts LogicalTime, txHash TransactionHash, sequencerFlag, branchFlag bool) string {
	return fmt.Sprintf("%s%s..", timestampPrefixString(ts, sequencerFlag, branchFlag), hex.EncodeToString(txHash[:3]))
}

func TransactionIDVeryShort(ts LogicalTime, txHash TransactionHash, sequencerFlag, branchFlag bool) string {
	return fmt.Sprintf("%s%s..", timestampPrefixString(ts, sequencerFlag, branchFlag, true), hex.EncodeToString(txHash[:3]))
}

func (txid *TransactionID) String() string {
	return TransactionIDString(txid.Timestamp(), txid.TransactionHash(), txid.SequencerFlagON(), txid.BranchFlagON())
}

func (txid *TransactionID) Short() string {
	return TransactionIDShort(txid.Timestamp(), txid.TransactionHash(), txid.SequencerFlagON(), txid.BranchFlagON())
}

func (txid *TransactionID) VeryShort() string {
	return TransactionIDVeryShort(txid.Timestamp(), txid.TransactionHash(), txid.SequencerFlagON(), txid.BranchFlagON())
}

func NewOutputID(id *TransactionID, idx byte) (ret OutputID) {
	copy(ret[:TransactionIDLength], id[:])
	ret[TransactionIDLength] = idx
	return
}

func OutputIDFromBytes(data []byte) (ret OutputID, err error) {
	if len(data) != OutputIDLength {
		err = errors.New("OutputIDFromBytes: wrong data length")
		return
	}
	copy(ret[:], data)
	return
}

func MustOutputIndexFromIDBytes(data []byte) byte {
	return data[TransactionIDLength]
}

// OutputIDIndexFromBytes optimizes memory usage
func OutputIDIndexFromBytes(data []byte) (ret byte, err error) {
	if len(data) != OutputIDLength {
		err = errors.New("OutputIDIndexFromBytes: wrong data length")
		return
	}
	return data[TransactionIDLength], nil
}

func MustOutputIDIndexFromBytes(data []byte) (ret byte) {
	var err error
	ret, err = OutputIDIndexFromBytes(data)
	util.AssertNoError(err)
	return
}

func (oid *OutputID) SequencerFlagON() bool {
	return oid[0]&SequencerTxFlagHigherByte != 0
}

func (oid *OutputID) BranchFlagON() bool {
	return oid[0]&BranchTxFlagHigherByte != 0
}

func (oid *OutputID) IsSequencerTransaction() bool {
	return oid.SequencerFlagON()
}

func (oid *OutputID) IsBranchTransaction() bool {
	return oid.SequencerFlagON() && oid.BranchFlagON()
}

func (oid *OutputID) String() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.String(), oid.Index())
}

func (oid *OutputID) Short() string {
	txid := oid.TransactionID()
	return fmt.Sprintf("%s[%d]", txid.Short(), oid.Index())
}

func (oid *OutputID) TransactionID() (ret TransactionID) {
	copy(ret[:], oid[:TransactionIDLength])
	return
}

func (oid *OutputID) Timestamp() LogicalTime {
	ret := oid.TransactionID()
	return ret.Timestamp()
}

func (oid *OutputID) TimeSlot() TimeSlot {
	ret := oid.TransactionID()
	return ret.TimeSlot()
}

func (oid *OutputID) TimeTick() TimeTick {
	ret := oid.TransactionID()
	return ret.TimeTick()
}

func (oid *OutputID) TransactionHash() (ret TransactionHash) {
	copy(ret[:], oid[LogicalTimeByteLength:TransactionIDLength])
	return
}

func (oid *OutputID) Index() byte {
	return oid[TransactionIDLength]
}

func (oid *OutputID) Bytes() []byte {
	return oid[:]
}
