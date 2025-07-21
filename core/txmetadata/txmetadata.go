package txmetadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

// TransactionMetadata optional data which may be attached to the transaction
// Wrong metadata or absence of it entirely or in parts cannot damage the network
// When present, metadata is used for consistency checking and workflow optimization
type (
	TransactionMetadata struct {
		// persistent
		StateRoot      common.VCommitment // not nil may be for branch transactions
		CoverageDelta  *uint64            // not nil may be for sequencer transactions
		FrozenCoverage *uint64            // not nil may be for sequencer transactions
		LedgerCoverage *uint64            // not nil may be for sequencer transactions
		SlotInflation  *uint64            // not nil may be for sequencer transactions
		Supply         *uint64            // not nil may be for branch transactions
		// non-persistent
		SourceTypeNonPersistent SourceType // non-persistent, used for internal workflow
		TxBytesReceived         *time.Time // not-persistent, used for metrics
	}

	TransactionMetadataJSONAble struct {
		// persistent
		StateRoot      string `json:"state_root,omitempty"`
		CoverageDelta  uint64 `json:"coverage_delta,omitempty"`
		FrozenCoverage uint64 `json:"frozen_coverage,omitempty"`
		LedgerCoverage uint64 `json:"ledger_coverage,omitempty"`
		SlotInflation  uint64 `json:"slot_inflation,omitempty"`
		Supply         uint64 `json:"supply,omitempty"`
	}

	SourceType byte
)

const (
	SourceTypeUndef = SourceType(iota)
	SourceTypeSequencer
	SourceTypePeer
	SourceTypeAPI
	SourceTypeTxStore
	SourceTypePulled
)

var allSourceTypes = map[SourceType]string{
	SourceTypeUndef:     "undef",
	SourceTypeSequencer: "sequencer",
	SourceTypePeer:      "peer",
	SourceTypeAPI:       "API",
	SourceTypeTxStore:   "txStore",
	SourceTypePulled:    "pulled",
}

// persistent flags for (de)serialization
const (
	flagRootProvided           = 0b00000001
	flagCoverageDeltaProvided  = 0b00000010
	flagFrozenCoverageProvided = 0b00000100
	flagLedgerCoverageProvided = 0b00001000
	flagSlotInflationProvided  = 0b00010000
	flagSupplyProvided         = 0b00100000
)

func (s SourceType) String() string {
	ret, ok := allSourceTypes[s]
	util.Assertf(ok, "unsupported source type")
	return ret
}

func (m *TransactionMetadata) flags() (ret byte) {
	if !util.IsNil(m.StateRoot) {
		ret |= flagRootProvided
	}
	if m.CoverageDelta != nil {
		ret |= flagCoverageDeltaProvided
	}
	if m.FrozenCoverage != nil {
		ret |= flagFrozenCoverageProvided
	}
	if m.LedgerCoverage != nil {
		ret |= flagLedgerCoverageProvided
	}
	if m.SlotInflation != nil {
		ret |= flagSlotInflationProvided
	}
	if m.Supply != nil {
		ret |= flagSupplyProvided
	}
	return
}

// Bytes receiver of TransactionMetadata is nil-safe
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
	// size byte (will be filled in at the end
	buf.WriteByte(0)
	buf.WriteByte(flags)
	if !util.IsNil(m.StateRoot) {
		buf.Write(m.StateRoot.Bytes())
	}
	if m.CoverageDelta != nil {
		_ = binary.Write(&buf, binary.BigEndian, *m.CoverageDelta)
	}
	if m.FrozenCoverage != nil {
		_ = binary.Write(&buf, binary.BigEndian, *m.FrozenCoverage)
	}
	if m.LedgerCoverage != nil {
		_ = binary.Write(&buf, binary.BigEndian, *m.LedgerCoverage)
	}
	if m.SlotInflation != nil {
		_ = binary.Write(&buf, binary.BigEndian, *m.SlotInflation)
	}
	if m.Supply != nil {
		_ = binary.Write(&buf, binary.BigEndian, *m.Supply)
	}
	ret := buf.Bytes()
	util.Assertf(len(ret) <= 256, "too big TransactionMetadata")
	ret[0] = byte(len(ret) - 1)
	return ret
}

func _readUint64(r io.Reader) (ret uint64, err error) {
	err = binary.Read(r, binary.BigEndian, &ret)
	return
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
	if flags&flagRootProvided != 0 {
		ret.StateRoot = ledger.CommitmentModel.NewVectorCommitment()
		if err = ret.StateRoot.Read(rdr); err != nil {
			return nil, fmt.Errorf("TransactionMetadataFromBytes: %w", err)
		}
	}
	if flags&flagCoverageDeltaProvided != 0 {
		ret.CoverageDelta = new(uint64)
		if *ret.CoverageDelta, err = _readUint64(rdr); err != nil {
			return nil, err
		}
	}
	if flags&flagFrozenCoverageProvided != 0 {
		ret.FrozenCoverage = new(uint64)
		if *ret.FrozenCoverage, err = _readUint64(rdr); err != nil {
			return nil, err
		}
	}
	if flags&flagLedgerCoverageProvided != 0 {
		ret.LedgerCoverage = new(uint64)
		if *ret.LedgerCoverage, err = _readUint64(rdr); err != nil {
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
func SplitTxBytesWithMetadata(txBytesWithMetadata []byte) (metadataBytes []byte, txBytes []byte, err error) {
	if len(txBytesWithMetadata) == 0 {
		return nil, nil, fmt.Errorf("SplitTxBytesWithMetadata: empty bytes")
	}
	if len(txBytesWithMetadata) <= int(txBytesWithMetadata[0]+1) {
		return nil, nil, fmt.Errorf("SplitTxBytesWithMetadata: wrong transaction metadata prefix length")
	}
	return txBytesWithMetadata[:txBytesWithMetadata[0]+1], txBytesWithMetadata[txBytesWithMetadata[0]+1:], nil
}

func ParseTxMetadata(txBytesWithMetadata []byte) (txBytes []byte, metadata *TransactionMetadata, err error) {
	var metaBytes []byte
	metaBytes, txBytes, err = SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		return nil, nil, err
	}
	txMetadata, err := TransactionMetadataFromBytes(metaBytes)
	return txBytes, txMetadata, err
}

func (m *TransactionMetadata) Lines(prefix ...string) *lines.Lines {
	if m == nil || m.flags() == 0 {
		return lines.New(prefix...).Add("<empty>")
	}
	ret := lines.New(prefix...)
	if m.CoverageDelta != nil {
		ret.Add("coverage delta: %s", util.Th(*m.CoverageDelta))
	} else {
		ret.Add("coverage delta: n/a")
	}
	if m.FrozenCoverage != nil {
		ret.Add("frozen coverage: %s", util.Th(*m.CoverageDelta))
	} else {
		ret.Add("frozen coverage: n/a")
	}
	if m.LedgerCoverage != nil {
		ret.Add("coverage: %s", util.Th(*m.LedgerCoverage))
	} else {
		ret.Add("coverage: n/a")
	}
	if m.SlotInflation != nil {
		ret.Add("slot inflation: %s", util.Th(*m.SlotInflation))
	} else {
		ret.Add("slot inflation: n/a")
	}
	if m.Supply != nil {
		ret.Add("supply: %s", util.Th(*m.Supply))
	} else {
		ret.Add("supply: n/a")
	}
	if !util.IsNil(m.StateRoot) {
		ret.Add("root: %s", m.StateRoot.String())
	} else {
		ret.Add("root: n/a")
	}
	ret.Add("source type: %s", m.SourceTypeNonPersistent.String())
	return ret
}

// String returns info of the persistent part
func (m *TransactionMetadata) String() string {
	return m.Lines().Join(", ")
}

func (m *TransactionMetadata) JSONAble() *TransactionMetadataJSONAble {
	if m == nil {
		return nil
	}
	ret := &TransactionMetadataJSONAble{}
	notEmpty := false
	if !util.IsNil(m.StateRoot) {
		notEmpty = true
		ret.StateRoot = m.StateRoot.String()
	}
	if m.LedgerCoverage != nil {
		notEmpty = true
		ret.LedgerCoverage = *m.LedgerCoverage
	}
	if m.FrozenCoverage != nil {
		notEmpty = true
		ret.FrozenCoverage = *m.FrozenCoverage
	}
	if m.SlotInflation != nil {
		notEmpty = true
		ret.SlotInflation = *m.SlotInflation
	}
	if m.Supply != nil {
		notEmpty = true
		ret.Supply = *m.Supply
	}
	if notEmpty {
		return ret
	}
	return nil
}

// IsConsistentWithExceptCoverage check consistency of all fields except ledger coverage, because ledger coverage
// calculated for the transaction can be less than the one arrived with the transaction
func (m *TransactionMetadata) IsConsistentWithExceptCoverage(m1 *TransactionMetadata) bool {
	if m == nil || m1 == nil {
		return true
	}
	if !util.IsNil(m.StateRoot) && !util.IsNil(m1.StateRoot) && !ledger.CommitmentModel.EqualCommitments(m.StateRoot, m1.StateRoot) {
		return false
	}
	if m.CoverageDelta != nil && m1.CoverageDelta != nil && *m.CoverageDelta != *m1.CoverageDelta {
		return false
	}
	if m.SlotInflation != nil && m1.SlotInflation != nil && *m.SlotInflation != *m1.SlotInflation {
		return false
	}
	if m.Supply != nil && m1.Supply != nil && *m.Supply != *m1.Supply {
		return false
	}
	return true
}
