// Package txmetadata holds the source-type enum used by the transaction
// receive path. The persistent TransactionMetadata struct, its bytes/JSON
// serialization, and the length-prefixed wire format have been removed —
// the deterministic aggregates that used to ride along (Supply,
// CoverageDelta, FrozenCoverage, SlotInflation, LedgerCoverage, StateRoot)
// now live inside the trie-committed stem output (see metadata-refactor
// plan §7). The two non-persistent fields (`SourceType` and `TxBytesReceived`)
// travel as plain Go function parameters on the receive path.
package txmetadata

import (
	"time"

	"github.com/lunfardo314/proxima/util"
)

type (
	// TransactionMetadata is the ephemeral context attached to incoming
	// transactions on the receive path. It is NEVER serialized.
	TransactionMetadata struct {
		SourceTypeNonPersistent SourceType
		TxBytesReceived         *time.Time
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

func (s SourceType) String() string {
	ret, ok := allSourceTypes[s]
	util.Assertf(ok, "unsupported source type")
	return ret
}

func (m *TransactionMetadata) String() string {
	if m == nil {
		return "<nil>"
	}
	return "source=" + m.SourceTypeNonPersistent.String()
}
