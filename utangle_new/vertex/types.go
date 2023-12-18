package vertex

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
)

type (
	Vertex struct {
		Tx             *transaction.Transaction
		Inputs         []*WrappedTx
		Endorsements   []*WrappedTx
		BaselineBranch *WrappedTx
		Forks          *ForkSet
	}

	VirtualTransaction struct {
		txid             core.TransactionID
		mutex            sync.RWMutex
		outputs          map[byte]*core.Output
		sequencerOutputs *[2]byte // if nil, it is unknown
	}

	Fork struct {
		ConflictSetID WrappedOutput
		SN            byte // max 256 double spends per output. Tx will be dropped if exceeded
	}

	ForkSet struct {
		m     map[WrappedOutput]byte
		mutex sync.RWMutex
	}

	// WrappedTx value of *WrappedTx is used as transaction identity on the UTXO tangle, a vertex
	// Behind this identity can be wrapped usual vertex, virtual or orphaned transactions
	WrappedTx struct {
		mutex sync.RWMutex // protects _genericWrapper
		_genericWrapper
		// future cone references. Protected by global utangle lock
		// numConsumers contains number of consumers for outputs
		consumers map[byte][]*WrappedTx
		// descendants is a list of consumers and endorsers, repeated once
		endorsers []*WrappedTx
		//
		txStatus TxStatus
		// notification callback
		onNotify func(vid *WrappedTx)
	}

	WrappedOutput struct {
		VID   *WrappedTx
		Index byte
	}

	// _genericWrapper generic types of vertex hiding behind WrappedTx identity
	_genericWrapper interface {
		_id() *core.TransactionID
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

	_deletedTx struct {
		core.TransactionID
	}

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

	TxStatus byte
)
