package memdag

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
)

// QueryTxIDStatus returns vertex mode, tx status and error of the vertex
func (d *MemDAG) QueryTxIDStatus(txid *ledger.TransactionID) (ret vertex.TxIDStatus) {
	ret.ID = *txid
	vid := d.GetVertex(txid)
	if vid == nil {
		return
	}

	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret.Status = vid.GetTxStatusNoLock()
			ret.Flags = vid.FlagsNoLock()
			ret.Coverage = vid.GetLedgerCoverageNoLock()
			ret.Err = vid.GetErrorNoLock()
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			ret.Status = vid.GetTxStatusNoLock()
			ret.Flags = vid.FlagsNoLock()
			ret.VirtualTx = true
			ret.Err = vid.GetErrorNoLock()
		},
		Deleted: func() {
			ret.Deleted = true
		},
	})
	return
}

//
//func (d *MemDAG) WaitTxIDDefined(txid *ledger.TransactionID, pollPeriod time.Duration, timeout ...time.Duration) (string, error) {
//	deadline := time.Now().Add(time.Minute)
//	if len(timeout) > 0 {
//		deadline = time.Now().Add(timeout[0])
//	}
//	for {
//		mode, status, err := d.QueryTxIDStatus(txid)
//		if mode != VertexModeVertex {
//			return TxIDStatusUndef, fmt.Errorf("vertex mode: %s", mode)
//		}
//		if status == TxIDStatusGood || status == TxIDStatusBad {
//			return status, err
//		}
//		time.Sleep(pollPeriod)
//
//		if time.Now().After(deadline) {
//			return TxIDStatusUndef, fmt.Errorf("WaitTxIDDefined: timeout")
//		}
//	}
//}
