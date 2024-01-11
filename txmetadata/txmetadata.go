package txmetadata

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

const MaxTransactionMetadataByteSize = 255

// TransactionMetadata auxiliary data which may be sent together with the transaction over the network
type TransactionMetadata struct {
	// one of TransactionMetadataMode*
	SendType byte
	// nil of root of the state for the branch transaction.
	// If not nil, it is used to check for consistency of the ledger state of the branch
	StateRoot common.VCommitment
}

const (
	SendTypeFromSequencer = byte(iota)
	SendTypeFromAPISource
	SendTypeFromTxStore
	SendTypeResponseToPull
	SendTypeForward
)

var allModes = set.New(
	SendTypeFromSequencer,
	SendTypeFromAPISource,
	SendTypeResponseToPull,
	SendTypeForward,
)

func (m *TransactionMetadata) Bytes() []byte {
	if util.IsNil(m.StateRoot) {
		return []byte{m.SendType, 0}
	}
	rootBytes := m.StateRoot.Bytes()
	util.Assertf(len(rootBytes)+2 <= MaxTransactionMetadataByteSize, "len(rootBytes) + 2 <= MaxTransactionMetadataByteSize")
	return common.Concat(m.SendType, byte(len(rootBytes)), rootBytes)
}

func TransactionMetadataFromBytes(data []byte) (*TransactionMetadata, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("error while parsing tx metadata: wrong data size")
	}
	if !allModes.Contains(data[0]) {
		return nil, fmt.Errorf("error while parsing tx metadata: wrong send type")
	}
	ret := &TransactionMetadata{SendType: data[0]}
	if data[1] > 0 {
		var err error
		if ret.StateRoot, err = common.VectorCommitmentFromBytes(ledger.CommitmentModel, data[2:]); err != nil {
			return nil, fmt.Errorf("error while parsing tx metadata: %w", err)
		}
	}
	return ret, nil
}
