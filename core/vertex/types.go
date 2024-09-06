package vertex

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	// Vertex is a transaction with past cone dependencies
	Vertex struct {
		Tx             *transaction.Transaction
		Inputs         []*WrappedTx
		Endorsements   []*WrappedTx
		BaselineBranch *WrappedTx
	}

	// VirtualTransaction is a collection of produced outputs
	VirtualTransaction struct {
		mutex            sync.RWMutex
		outputs          map[byte]*ledger.Output
		sequencerOutputs *[2]byte   // if nil, it is unknown
		pullDeadline     *time.Time // if nil, does not need pull
		lastPull         time.Time
	}

	// WrappedTx value of *WrappedTx is used as transaction identity on the UTXO tangle, a vertex
	// Behind this identity can be wrapped usual vertex or virtual transactions
	WrappedTx struct {
		// immutable ID. It does not change with the change of the underlying wrapped vertex type
		ID ledger.TransactionID
		// sequencer ID not nil for sequencer transactions only. Once it is set not nil, it is immutable since.
		// It is set whenever transaction becomes available
		SequencerID atomic.Pointer[ledger.ChainID]
		mutex       sync.RWMutex // *sema.Sema // sync.RWMutex // protects _genericVertex
		flags       Flags
		err         error
		coverage    *uint64 // nil for non-sequencer or if not set yet

		// keeping track of references for orphaning/GC
		numReferences uint32
		// dontPruneUntil interpreted depending on value of references
		// - if references > 1, dontPruneUntil is the deadline until when the past cone should not be un-referenced
		// - if references == 1, dontPruneUntil is clock time, until which it should not be deleted
		// - if references == 0 (deleted) it is not interpreted
		// valid when references == 1. It is needed to prevent immediate pruning after adding to the MemDAG
		dontPruneUntil time.Time

		// notification callback. Must be func(vid *WrappedTx)
		onPoke atomic.Value

		_genericVertex

		mutexDescendants sync.RWMutex
		consumed         map[byte]set.Set[*WrappedTx]
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

	_virtualTx struct {
		*VirtualTransaction
	}

	UnwrapOptions struct {
		Vertex    func(v *Vertex)
		VirtualTx func(v *VirtualTransaction)
		Deleted   func()
	}

	UnwrapOptionsForTraverse struct {
		Vertex    func(vidCur *WrappedTx, v *Vertex) bool
		VirtualTx func(vidCur *WrappedTx, v *VirtualTransaction) bool
		TxID      func(txid *ledger.TransactionID)
		Deleted   func(vidCur *WrappedTx) bool
	}

	Status byte
	Flags  uint8

	TxIDStatus struct {
		ID        ledger.TransactionID
		OnDAG     bool
		InStorage bool
		VirtualTx bool
		Deleted   bool
		Status    Status
		Flags     Flags
		Coverage  *uint64
		Err       error
	}

	TxIDStatusJSONAble struct {
		ID        string `json:"id"`
		OnDAG     bool   `json:"on_dag"`
		InStorage bool   `json:"in_storage"`
		VirtualTx bool   `json:"virtual_tx"`
		Deleted   bool   `json:"deleted"`
		Status    string `json:"status"`
		Flags     byte   `json:"flags"`
		Coverage  uint64 `json:"coverage,omitempty"`
		Err       error  `json:"err"`
	}
)

const (
	FlagVertexDefined              = Flags(0b00000001)
	FlagVertexConstraintsValid     = Flags(0b00000010)
	FlagVertexTxAttachmentStarted  = Flags(0b00000100)
	FlagVertexTxAttachmentFinished = Flags(0b00001000)
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
		if s.VirtualTx {
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
		ID:        s.ID.StringHex(),
		OnDAG:     s.OnDAG,
		InStorage: s.InStorage,
		VirtualTx: s.VirtualTx,
		Deleted:   s.Deleted,
		Status:    s.Status.String(),
		Flags:     byte(s.Flags),
		Err:       s.Err,
	}
	if s.Coverage != nil {
		ret.Coverage = *s.Coverage
	}
	return ret
}

func (s *TxIDStatusJSONAble) Parse() (*TxIDStatus, error) {
	ret := &TxIDStatus{
		OnDAG:     s.OnDAG,
		InStorage: s.InStorage,
		VirtualTx: s.VirtualTx,
		Deleted:   s.Deleted,
		Status:    StatusFromString(s.Status),
		Flags:     Flags(s.Flags),
		Coverage:  nil,
		Err:       s.Err,
	}
	var err error
	ret.ID, err = ledger.TransactionIDFromHexString(s.ID)
	if err != nil {
		return nil, err
	}
	if s.Coverage != 0 {
		ret.Coverage = new(uint64)
		*ret.Coverage = s.Coverage
	}
	return ret, nil
}
