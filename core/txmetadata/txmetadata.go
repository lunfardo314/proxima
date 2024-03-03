package txmetadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// TransactionMetadata optional data which may be attached to the transaction
// Wrong metadata or absence of it entirely or in parts cannot damage the network
// When present, metadata is used for consistency checking and workflow optimization
type (
	TransactionMetadata struct {
		StateRoot               common.VCommitment // not nil may be for branch transactions
		LedgerCoverageDelta     *uint64            // not nil may be for sequencer transactions
		SlotInflation           *uint64            // not nil may be for sequencer transactions
		Supply                  *uint64            // not nil may be for branch transactions
		SourceTypeNonPersistent SourceType         // non-persistent, used for internal workflow
		DoNotNeedGossiping      bool               // non-persistent flag, to prevent redundant gossiping
		IsResponseToPull        bool               // only used as a persistent flag in tx gossip
	}

	SourceType byte
)

const (
	SourceTypeUndef = SourceType(iota)
	SourceTypeSequencer
	SourceTypePeer
	SourceTypeAPI
	SourceTypeTxStore
)

var allSourceTypes = map[SourceType]string{
	SourceTypeUndef:     "undef",
	SourceTypeSequencer: "sequencer",
	SourceTypePeer:      "peer",
	SourceTypeAPI:       "API",
	SourceTypeTxStore:   "txStore",
}

// persistent flags for (de)serialization
const (
	flagIsResponseToPull      = 0b00000001
	flagRootProvided          = 0b00000010
	flagCoverageDeltaProvided = 0b00000100
	flagSlotInflationProvided = 0b00001000
	flagSupplyProvided        = 0b00010000
)

func (s SourceType) String() string {
	ret, ok := allSourceTypes[s]
	util.Assertf(ok, "unsupported source type")
	return ret
}

func (m *TransactionMetadata) flags() (ret byte) {
	if m.IsResponseToPull {
		ret |= flagIsResponseToPull
	}
	if !util.IsNil(m.StateRoot) {
		ret |= flagRootProvided
	}
	if m.LedgerCoverageDelta != nil {
		ret |= flagCoverageDeltaProvided
	}
	if m.SlotInflation != nil {
		ret |= flagSlotInflationProvided
	}
	if m.Supply != nil {
		ret |= flagSupplyProvided
	}
	return
}

// Bytes of TransactionMetadata is nil-safe
func (m *TransactionMetadata) Bytes() []byte {
	// flags == 0 means no persistent information is contained
	if m == nil {
		return []byte{0}
	}
	flags := m.flags()
	if flags == 0 {
		return []byte{0}
	}

	var buf bytes.Buffer
	// size byte (will be filled-in in the end
	buf.WriteByte(0)
	buf.WriteByte(flags)
	if !util.IsNil(m.StateRoot) {
		buf.Write(m.StateRoot.Bytes())
	}
	if m.LedgerCoverageDelta != nil {
		var coverageBin [8]byte
		binary.BigEndian.PutUint64(coverageBin[:], *m.LedgerCoverageDelta)
		buf.Write(coverageBin[:])
	}
	if m.SlotInflation != nil {
		var slotInflationBin [8]byte
		binary.BigEndian.PutUint64(slotInflationBin[:], *m.SlotInflation)
		buf.Write(slotInflationBin[:])
	}
	if m.Supply != nil {
		var supplyBin [8]byte
		binary.BigEndian.PutUint64(supplyBin[:], *m.Supply)
		buf.Write(supplyBin[:])
	}
	ret := buf.Bytes()
	util.Assertf(len(ret) <= 256, "too big TransactionMetadata")
	ret[0] = byte(len(ret) - 1)
	return ret
}

func _readUint64(r io.Reader) (uint64, error) {
	var uint64Bin [8]byte
	n, err := r.Read(uint64Bin[:])
	if err != nil {
		return 0, fmt.Errorf("TransactionMetadataFromBytes: %w", err)
	}
	if n != 8 {
		return 0, fmt.Errorf("TransactionMetadataFromBytes: unexpected EOF")
	}
	return binary.BigEndian.Uint64(uint64Bin[:]), nil
}

func TransactionMetadataFromBytes(data []byte) (*TransactionMetadata, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("txmetadata must be at least 1 byte")
	}
	if int(data[0]) != len(data)-1 {
		return nil, fmt.Errorf("txmetadata first byte (%d) not equal to the length of the remaining data (%d)",
			data[0], len(data)-1)
	}
	if len(data) == 1 {
		// empty metadata
		return nil, nil
	}
	ret := &TransactionMetadata{}
	rdr := bytes.NewReader(data[1:])
	flags, err := rdr.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("TransactionMetadataFromBytes: %w", err)
	}
	ret.IsResponseToPull = flags&flagIsResponseToPull != 0
	if flags&flagRootProvided != 0 {
		ret.StateRoot = ledger.CommitmentModel.NewVectorCommitment()
		if err = ret.StateRoot.Read(rdr); err != nil {
			return nil, fmt.Errorf("TransactionMetadataFromBytes: %w", err)
		}
	}
	if flags&flagCoverageDeltaProvided != 0 {
		ret.LedgerCoverageDelta = new(uint64)
		if *ret.LedgerCoverageDelta, err = _readUint64(rdr); err != nil {
			return nil, err
		}
	}
	if flags&flagSlotInflationProvided != 0 {
		ret.SlotInflation = new(uint64)
		if *ret.SlotInflation, err = _readUint64(rdr); err != nil {
			return nil, err
		}
	}
	if flags&flagSupplyProvided != 0 {
		ret.Supply = new(uint64)
		if *ret.Supply, err = _readUint64(rdr); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

// SplitTxBytesWithMetadata splits received bytes into two pieces
// Returns: metadata bytes, txBytes
func SplitTxBytesWithMetadata(txBytesWithMetadata []byte) ([]byte, []byte, error) {
	if len(txBytesWithMetadata) == 0 {
		return nil, nil, fmt.Errorf("SplitTxBytesWithMetadata: empty bytes")
	}
	if len(txBytesWithMetadata) <= int(txBytesWithMetadata[0]+1) {
		return nil, nil, fmt.Errorf("SplitTxBytesWithMetadata: wrong transaction metadata prefix length")
	}
	return txBytesWithMetadata[:txBytesWithMetadata[0]+1], txBytesWithMetadata[txBytesWithMetadata[0]+1:], nil
}

// String returns info of the persistent part
func (m *TransactionMetadata) String() string {
	if m == nil || m.flags() == 0 {
		return "<empty>"
	}
	lcStr := "<nil>"
	if m.LedgerCoverageDelta != nil {
		lcStr = util.GoTh(*m.LedgerCoverageDelta)
	}
	rootStr := "<nil>"
	if !util.IsNil(m.StateRoot) {
		rootStr = m.StateRoot.String()
	}
	inflationStr := "<nil>"
	if m.SlotInflation != nil {
		inflationStr = util.GoTh(*m.SlotInflation)
	}
	return fmt.Sprintf("coverage delta: %s, slot inflation: %s, root: %s, source type: '%s'", lcStr, inflationStr, rootStr, m.SourceTypeNonPersistent.String())
}
