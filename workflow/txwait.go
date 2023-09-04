package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/wait"
)

// TxStatusWaitingList implements waiting service for transactions asynchronously posted to the workflow
// The sender can provide a callback hook invoked after status of the posted transaction changes

type (
	TxStatusMsg struct {
		Appended bool   // true - appended to the UTX tangle, false - rejected
		Msg      string // reject reason
	}

	TxStatusWaitingList struct {
		w              *wait.WaitingRoom[*TxStatusMsg]
		mapMutex       sync.Mutex
		m              map[core.TransactionID]uint64
		defaultTimeout time.Duration
		stopOnce       sync.Once
	}
)

func (w *Workflow) StartTxStatusWaitingList(defaultTimeout time.Duration) *TxStatusWaitingList {
	ret := &TxStatusWaitingList{
		w:              wait.New[*TxStatusMsg](),
		m:              make(map[core.TransactionID]uint64),
		defaultTimeout: defaultTimeout,
	}
	err := w.OnEvent(EventNewVertex, func(inp *NewVertexEventData) {
		if ret.w.IsStopped() {
			return
		}
		if sn, ok := ret.removeIfPresent(inp.Tx.ID()); ok {
			ret.w.Release(sn, &TxStatusMsg{
				Appended: true,
			})
		}
	})
	util.AssertNoError(err)

	err = w.OnEvent(EventRejectedTx, func(inp *RejectInputData) {
		if ret.w.IsStopped() {
			return
		}
		if sn, ok := ret.removeIfPresent(&inp.TxID); ok {
			ret.w.Release(sn, &TxStatusMsg{
				Appended: false,
				Msg:      inp.Msg,
			})
		}
	})
	util.AssertNoError(err)
	return ret
}

func (w *TxStatusWaitingList) Cancel(txid *core.TransactionID, reason ...string) {
	msg := "cancelled waiting"
	if len(reason) > 0 {
		msg = reason[0]
	}
	if sn, ok := w.removeIfPresent(txid); ok {
		w.w.Release(sn, &TxStatusMsg{
			Appended: false,
			Msg:      msg,
		})
	}
}

func (w *TxStatusWaitingList) Stop() {
	w.stopOnce.Do(func() {
		w.mapMutex.Lock()
		defer w.mapMutex.Unlock()

		w.w.Stop()
		w.m = nil
	})
}

func (w *TxStatusWaitingList) removeIfPresent(txid *core.TransactionID) (uint64, bool) {
	w.mapMutex.Lock()
	defer w.mapMutex.Unlock()

	sn, ok := w.m[*txid]
	if !ok {
		return 0, false
	}
	delete(w.m, *txid)
	return sn, true
}

// OnTransactionStatus installs callback to be called when transaction is either included into the tangle or rejected.
// Only one transaction at a time
func (w *TxStatusWaitingList) OnTransactionStatus(txid *core.TransactionID, callback func(statusMsg *TxStatusMsg), timeout ...time.Duration) error {
	var deadline time.Time
	if len(timeout) > 0 {
		deadline = time.Now().Add(timeout[0])
	} else {
		deadline = time.Now().Add(w.defaultTimeout)
	}

	w.mapMutex.Lock()
	defer w.mapMutex.Unlock()

	if w.m == nil {
		// stopped
		return fmt.Errorf("OnTransactionStatus: waiting list already stopped")
	}
	_, already := w.m[*txid]
	if already {
		return fmt.Errorf("already waiting for %s", txid.Short())
	}
	w.m[*txid] = w.w.RunAtOrBefore(deadline, callback)
	return nil
}
