package ledger

import (
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
		SequencerOutputIndex byte
		StemOutputIndex      byte
	}
)

func (m *SequencerTransactionData) Short() string {
	return fmt.Sprintf("SEQ(%s)", m.SequencerID.StringVeryShort())
}

func (m *SequencerTransactionData) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("seq output index: %d, branch output index: %d", m.SequencerOutputIndex, m.SequencerOutputIndex)
	ret.Add("seq ID: %s", m.SequencerID.String())
	return ret
}

const sequencerDataBytesLength = 2

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
	}
	return
}

func (b *SequencerDataBytes) Bytes() []byte {
	ret := make([]byte, sequencerDataBytesLength)
	ret[0] = b.SequencerOutputIndex
	ret[1] = b.StemOutputIndex
	return ret
}
