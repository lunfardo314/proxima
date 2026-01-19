package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
)

type (
	// SequencerTransactionData represents sequencer and stem data on the transaction
	SequencerTransactionData struct {
		SequencerOutputData *SequencerOutputData
		StemOutputData      *StemLock    // nil if does not contain stem output
		SequencerID         base.ChainID // adjusted for chain origin
		SequencerDataBytes
	}

	SequencerDataBytes struct {
		AttachmentBudget     uint16
		SequencerOutputIndex byte
		StemOutputIndex      byte
	}
)

func (m *SequencerTransactionData) Short() string {
	return fmt.Sprintf("SEQ(%s)", m.SequencerID.StringVeryShort())
}

func (m *SequencerTransactionData) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("seq output index: %d, branch output index: %d, attachment budget: %d", m.SequencerOutputIndex, m.SequencerOutputIndex, m.AttachmentBudget)
	ret.Add("seq ID: %s", m.SequencerID.String())
	return ret
}

const sequencerDataBytesLength = 4

func SequencerDataBytesFromBytes(data []byte) (*SequencerDataBytes, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) != sequencerDataBytesLength {
		return nil, fmt.Errorf("SequencerDataBytesFromBytes: invalid data length")
	}
	ret := MustSequencerDataBytesFromBytes(data)
	return &ret, nil
}

func MustSequencerDataBytesFromBytes(data []byte) (ret SequencerDataBytes) {
	ret = SequencerDataBytes{
		SequencerOutputIndex: data[0],
		StemOutputIndex:      data[1],
		AttachmentBudget:     binary.BigEndian.Uint16(data[2:4]),
	}
	return
}

func (b *SequencerDataBytes) Bytes() []byte {
	ret := make([]byte, sequencerDataBytesLength)
	ret[0] = b.SequencerOutputIndex
	ret[1] = b.StemOutputIndex
	binary.BigEndian.PutUint16(ret[2:4], b.AttachmentBudget)
	return ret
}
