package vertex

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Vertex struct {
		Tx             *transaction.Transaction
		Inputs         []*WrappedTx
		Endorsements   []*WrappedTx
		BaselineBranch *WrappedTx
		Flags          uint8
	}

	VirtualTransaction struct {
		mutex            sync.RWMutex
		outputs          map[byte]*core.Output
		sequencerOutputs *[2]byte // if nil, it is unknown
	}

	// WrappedTx value of *WrappedTx is used as transaction identity on the UTXO tangle, a vertex
	// Behind this identity can be wrapped usual vertex, virtual or orphaned transactions
	WrappedTx struct {
		// immutable ID. It does not change with the change of the underlying wrapped vertex type
		ID    core.TransactionID
		mutex sync.RWMutex // protects _genericWrapper
		_genericWrapper
		// future cone references. Protected by global utangle_old lock
		// numConsumers contains number of consumed for outputs
		mutexDescendants sync.RWMutex
		consumed         map[byte]set.Set[*WrappedTx]

		txStatus Status
		reason   error
		coverage *multistate.LedgerCoverage // nil for non-sequencer
		// notification callback
		onPoke func(vid *WrappedTx)
	}

	WrappedOutput struct {
		VID   *WrappedTx
		Index byte
	}

	// _genericWrapper generic types of vertex hiding behind WrappedTx identity
	_genericWrapper interface {
		_time() time.Time
		_outputAt(idx byte) (*core.Output, error)
		_hasOutputAt(idx byte) (bool, bool)
	}

	_vertex struct {
		*Vertex
		whenWrapped time.Time
	}

	_virtualTx struct {
		*VirtualTransaction
	}

	_deletedTx struct{}

	UnwrapOptions struct {
		Vertex    func(v *Vertex)
		VirtualTx func(v *VirtualTransaction)
		Deleted   func()
	}

	UnwrapOptionsForTraverse struct {
		Vertex    func(vidCur *WrappedTx, v *Vertex) bool
		VirtualTx func(vidCur *WrappedTx, v *VirtualTransaction) bool
		TxID      func(txid *core.TransactionID)
		Orphaned  func(vidCur *WrappedTx) bool
	}

	Status byte
)

const (
	FlagBaselineSolid             = 0b00000001
	FlagEndorsementsSolid         = 0b00000010
	FlagAllInputsSolid            = 0b00000100
	FlagConstraintsValid          = 0b00001000
	FlagsSequencerVertexCompleted = FlagBaselineSolid | FlagEndorsementsSolid | FlagAllInputsSolid | FlagConstraintsValid
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
