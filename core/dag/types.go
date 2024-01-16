package dag

import (
	"sync"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
)

type (
	DAG struct {
		mutex      sync.RWMutex
		stateStore global.StateStore
		vertices   map[ledger.TransactionID]*vertex.WrappedTx
		branches   map[*vertex.WrappedTx]global.IndexedStateReader
	}
)

func New(stateStore global.StateStore) *DAG {
	return &DAG{
		stateStore: stateStore,
		vertices:   make(map[ledger.TransactionID]*vertex.WrappedTx),
		branches:   make(map[*vertex.WrappedTx]global.IndexedStateReader),
	}
}
