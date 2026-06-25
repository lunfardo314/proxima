package vertex

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	// Vertex is a transaction with past cone dependencies
	Vertex struct {
		*transaction.Transaction
		Inputs       []*WrappedTx
		Endorsements []*WrappedTx
	}

	DetachedVertex struct {
		*transaction.Transaction
	}

	// VirtualTransaction is a collection of produced outputs
	VirtualTransaction struct {
		Created                time.Time
		mutex                  sync.RWMutex
		outputs                map[byte]*ledger.Output
		sequencerOutputIndices *[2]byte // if nil, it is unknown
		inflation              uint64
		// pull rules
		pullRulesDefined bool
		needsPull        bool
		nextPull         time.Time
		timesPulled      int
	}

	// WrappedTx value of *WrappedTx is used as transaction identity on the UTXO tangle, a vertex
	// Behind this identity can be wrapped usual vertex or virtual transactions
	WrappedTx struct {
		// immutable id. It does not change with the change of the underlying wrapped vertex type
		id base.TransactionID
		// sequencer id not nil for sequencer transactions only. Once it is set not nil, it is immutable since.
		// It is set whenever the transaction becomes available
		SequencerID atomic.Pointer[base.ChainID]
		mutex       sync.RWMutex // *sema.Sema // sync.RWMutex // protects _genericVertex
		flags       Flags
		err         error
		coverage atomic.Pointer[uint64] // nil for non-sequencer or not set yet. Atomic — no mutex needed for reads.

		// notification callback. Must be func(vid *WrappedTx)
		onPoke atomic.Value

		_genericVertex

		mutexDescendants sync.RWMutex
		consumed         map[byte]set.Set[*WrappedTx]
		attachmentDepth  int
		SlotWhenAdded    uint32 // immutable
		pastCone         *PastConeBase
		// baselineBranchID is the committed baseline branch of a sequencer transaction. It is a property
		// of the vid regardless of the underlying vertex type (full, detached or virtual), so a virtual
		// tx can carry a baseline provided by AttachTxID(WithBaseline) until its attacher starts.
		// Set either by baseline solidification (lock-free, before the vid becomes Good) or by
		// AttachTxID(WithBaseline) at creation (under the global lock). nil for branches (a branch is its
		// own baseline) and for not-yet-solidified milestones with no provided baseline.
		baselineBranchID *base.TransactionID
	}

	WrappedOutput struct {
		VID   *WrappedTx
		Index byte
	}

	// _genericVertex generic types of vertex hiding behind WrappedTx identity
	_genericVertex interface {
		_outputAt(idx byte) (*ledger.Output, error)
	}

	_vertex struct {
		*Vertex
	}

	_detachedVertex struct {
		*DetachedVertex
	}

	_virtualTx struct {
		*VirtualTransaction
	}

	UnwrapOptions struct {
		Vertex         func(v *Vertex)
		DetachedVertex func(v *DetachedVertex)
		VirtualTx      func(v *VirtualTransaction)
	}

	UnwrapOptionsForTraverse struct {
		Vertex         func(vidCur *WrappedTx, v *Vertex) bool
		DetachedVertex func(vidCur *WrappedTx, v *DetachedVertex) bool
		VirtualTx      func(vidCur *WrappedTx, v *VirtualTransaction) bool
		TxID           func(txid *base.TransactionID)
	}

	Status byte
	Flags  uint8

	TxIDStatus struct {
		ID                base.TransactionID
		OnDAG             bool
		InStorage         bool
		VirtualOrDetached bool
		Deleted           bool
		Status            Status
		Flags             Flags
		Coverage          *uint64
		Err               error
	}

	TxIDStatusJSONAble struct {
		ID                string `json:"id"`
		OnDAG             bool   `json:"on_dag"`
		InStorage         bool   `json:"in_storage"`
		VirtualOrDetached bool   `json:"virtual_or_detached"`
		Deleted           bool   `json:"deleted"`
		Status            string `json:"status"`
		Flags             byte   `json:"flags"`
		Coverage          uint64 `json:"coverage,omitempty"`
		Err               error  `json:"err"`
	}
)

const (
	FlagVertexDefined                 = Flags(0b00000001)
	FlagVertexConstraintsValid        = Flags(0b00000010)
	FlagVertexTxAttachmentStarted     = Flags(0b00000100)
	FlagVertexTxAttachmentFinished    = Flags(0b00001000)
	FlagVertexIgnoreAbsenceOfPastCone = Flags(0b00010000)
)

const (
	Undefined = Status(iota)
	Good
	Bad
)

func (s Status) String() string {
	switch s {
	case Undefined:
		return "UNDEF"
	case Good:
		return "GOOD"
	case Bad:
		return "BAD"
	}
	panic("wrong vertex status")
}

func StatusFromString(s string) Status {
	switch s {
	case "GOOD", "good":
		return Good
	case "BAD", "bad":
		return Bad
	default:
		return Undefined
	}
}

func (f *Flags) FlagsUp(fl Flags) bool {
	return *f&fl == fl
}

func (f *Flags) SetFlagsUp(fl Flags) {
	*f = *f | fl
}

func (f *Flags) String() string {
	return fmt.Sprintf("defined=%v, validated=%v, attachStarted=%v, attachFinished=%v",
		f.FlagsUp(FlagVertexDefined),
		f.FlagsUp(FlagVertexConstraintsValid),
		f.FlagsUp(FlagVertexTxAttachmentStarted),
		f.FlagsUp(FlagVertexTxAttachmentFinished),
	)
}

func (s *TxIDStatus) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	if !s.OnDAG {
		ret.Add("NOT FOUND")
	} else {
		if s.Status != Bad {
			ret.Add(s.Status.String())
		} else {
			ret.Add("BAD(%v)", s.Err)
		}
		ret.Add("flags: %s", s.Flags.String())
		if s.VirtualOrDetached {
			ret.Add("virtualTx: true")
		}
		if s.Deleted {
			ret.Add("deleted: true")
		}
	}

	ret.Add("in storage: %v", s.InStorage)
	return ret
}

func (s *TxIDStatus) JSONAble() (ret TxIDStatusJSONAble) {
	ret = TxIDStatusJSONAble{
		ID:                s.ID.StringHex(),
		OnDAG:             s.OnDAG,
		InStorage:         s.InStorage,
		VirtualOrDetached: s.VirtualOrDetached,
		Deleted:           s.Deleted,
		Status:            s.Status.String(),
		Flags:             byte(s.Flags),
		Err:               s.Err,
	}
	if s.Coverage != nil {
		ret.Coverage = *s.Coverage
	}
	return ret
}

func (s *TxIDStatusJSONAble) Parse() (*TxIDStatus, error) {
	ret := &TxIDStatus{
		OnDAG:             s.OnDAG,
		InStorage:         s.InStorage,
		VirtualOrDetached: s.VirtualOrDetached,
		Status:            StatusFromString(s.Status),
		Flags:             Flags(s.Flags),
		Coverage:          nil,
		Err:               s.Err,
	}
	var err error
	ret.ID, err = base.TransactionIDFromHexString(s.ID)
	if err != nil {
		return nil, err
	}
	if s.Coverage != 0 {
		ret.Coverage = new(uint64)
		*ret.Coverage = s.Coverage
	}
	return ret, nil
}
