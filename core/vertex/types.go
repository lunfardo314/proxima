package vertex

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
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
		sequencerOutputs *[2]byte // if nil, it is unknown
	}

	// WrappedTx value of *WrappedTx is used as transaction identity on the UTXO tangle, a vertex
	// Behind this identity can be wrapped usual vertex or virtual transactions
	WrappedTx struct {
		// immutable ID. It does not change with the change of the underlying wrapped vertex type
		ID       ledger.TransactionID
		mutex    sync.RWMutex // protects _genericVertex
		flags    Flags
		err      error
		coverage *multistate.LedgerCoverage // nil for non-sequencer or if not set yet
		// keeping track of references for orphaning/GC
		references uint32
		// dontPruneUntil interpreted depending on value of references
		// - if references > 1, dontPruneUntil is the deadline until when the past cone should not be un-referenced
		// - if references == 1, dontPruneUntil is clock time, until which it should not be deleted
		// - if references == 0 (deleted) it is not interpreted
		// valid when references == 1. It is needed to prevent immediate pruning after adding to the DAG
		dontPruneUntil time.Time
		tmpRefBy       []string // TODO for tracing only, remove
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
		_hasOutputAt(idx byte) (bool, bool)
	}

	_vertex struct {
		*Vertex
		whenWrapped time.Time
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
)

const (
	FlagVertexDefined          = Flags(0b00000001)
	FlagVertexConstraintsValid = Flags(0b00000010)
	FlagVertexTxBytesPersisted = Flags(0b00000100)
	FlagVertexAttacherInvoked  = Flags(0b00001000)
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

func (f *Flags) FlagsUp(fl Flags) bool {
	return *f&fl == fl
}

func (f *Flags) SetFlagsUp(fl Flags) {
	*f = *f | fl
}
