package txmetadata

import (
	"bytes"
	"encoding/binary"
	"fmt"

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
		IsResponseToPull        bool
		SourceTypeNonPersistent SourceType
	}

	SourceType byte
)

const (
	SourceTypeUndef = SourceType(iota)
	SourceTypeSequencer
	SourceTypePeer
	SourceTypeAPI
	SourceTypeTxStore
	SourceTypeForward
)

var allSourceTypes = map[SourceType]string{
	SourceTypeUndef:     "undef",
	SourceTypeSequencer: "sequencer",
	SourceTypeAPI:       "API",
	SourceTypeTxStore:   "txStore",
	SourceTypeForward:   "forward",
}

// persistent flags for (de)serialization

const (
	flagIsResponseToPull      = 0b00000001
	flagRootProvided          = 0b00000010
	flagCoverageDeltaProvided = 0b00000100
)

const (
	MaxTransactionMetadataByteSize = 255
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
	ret := buf.Bytes()
	util.Assertf(len(ret) <= 256, "too big TransactionMetadata")
	ret[0] = byte(len(ret) - 1)
	return ret
}

func TransactionMetadataFromBytes(data []byte) (*TransactionMetadata, error) {
	if len(data) == 0 || int(data[0]) != len(data)-1 {
		return nil, fmt.Errorf("wrong data length")
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
		var uint64Bin [8]byte
		n, err := rdr.Read(uint64Bin[:])
		if err != nil {
			return nil, fmt.Errorf("TransactionMetadataFromBytes: %w", err)
		}
		if n != 8 {
			return nil, fmt.Errorf("TransactionMetadataFromBytes: unexpected EOF")
		}
		ret.LedgerCoverageDelta = new(uint64)
		*ret.LedgerCoverageDelta = binary.BigEndian.Uint64(uint64Bin[:])
	}
	return ret, nil
}

// SplitBytesWithMetadata returns:
// - metadata bytes
// - txBytes
func SplitBytesWithMetadata(txBytesWithMetadata []byte) ([]byte, []byte, error) {
	if len(txBytesWithMetadata) == 0 {
		return nil, nil, fmt.Errorf("SplitBytesWithMetadata: empty bytes")
	}
	if len(txBytesWithMetadata) <= int(txBytesWithMetadata[0]+1) {
		return nil, nil, fmt.Errorf("SplitBytesWithMetadata: wrong transaction metadata prefix length")
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
	return fmt.Sprintf("coverage delta: %s, root: %s", lcStr, rootStr)
}
