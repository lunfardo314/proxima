package dag

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
)

const (
	TxIDStatusGood  = "good"
	TxIDStatusBad   = "bad"
	TxIDStatusUndef = "undefined"
)

const (
	VertexModeNotFound  = "not found"
	VertexModeVertex    = "vertex"
	VertexModeVirtualTx = "virtualTx"
	VertexModeDeleted   = "deleted"
)

// QueryTxIDStatus returns vertex mode and tx status
func (d *DAG) QueryTxIDStatus(txid *ledger.TransactionID) (mode string, status string) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	status = TxIDStatusUndef
	mode = VertexModeNotFound
	vid, found := d.vertices[*txid]
	if !found {
		return
	}
	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			mode = VertexModeVertex
			switch vid.GetTxStatusNoLock() {
			case vertex.Good:
				status = TxIDStatusGood
			case vertex.Bad:
				status = TxIDStatusBad
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			mode = VertexModeVirtualTx
			switch vid.GetTxStatusNoLock() {
			case vertex.Good:
				status = TxIDStatusGood
			case vertex.Bad:
				status = TxIDStatusBad
			}
		},
		Deleted: func() {
			mode = VertexModeDeleted
		},
	})
	return
}

func (d *DAG) WaitTxIDDefined(txid *ledger.TransactionID, pollPeriod time.Duration, timeout ...time.Duration) (string, error) {
	deadline := time.Now().Add(time.Minute)
	if len(timeout) > 0 {
		deadline = time.Now().Add(timeout[0])
	}
	for {
		mode, status := d.QueryTxIDStatus(txid)
		if mode != VertexModeVertex {
			return TxIDStatusUndef, fmt.Errorf("vertex mode: %s", mode)
		}
		if status == TxIDStatusGood || status == TxIDStatusBad {
			return status, nil
		}
		time.Sleep(pollPeriod)

		if time.Now().After(deadline) {
			return TxIDStatusUndef, fmt.Errorf("timeout")
		}
	}
}
