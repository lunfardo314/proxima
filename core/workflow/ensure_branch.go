package workflow

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const maxTimeout = time.Minute

func (w *Workflow) EnsureBranch(txid base.TransactionID, timeout ...time.Duration) (*vertex.WrappedTx, error) {
	w.Assertf(txid.IsBranchTransaction(), "txid.IsBranchTransaction()")
	to := maxTimeout
	if len(timeout) > 0 {
		to = timeout[0]
	}

	deadline := time.Now().Add(to)

	vid := attacher.AttachTxID(txid, w, attacher.WithInvokedBy("EnsureBranch"))
	if vid.GetTxStatus() == vertex.Good {
		return vid, nil
	}

	if err := w.TxFromStoreIn(txid); err != nil {
		return nil, err
	}

	for vid.GetTxStatus() != vertex.Good {
		time.Sleep(10 * time.Millisecond)
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout(%v): branch %s is not in the state", to, txid.StringShort())
		}
	}
	return vid, nil
}

func (w *Workflow) MustEnsureBranch(txid base.TransactionID) *vertex.WrappedTx {
	ret, err := w.EnsureBranch(txid)
	w.AssertNoError(err)
	return ret
}

func (w *Workflow) EnsureLatestBranches() error {
	branchTxIDs := multistate.FetchLatestBranchTransactionIDs(w.StateStore())
	for _, branchID := range branchTxIDs {
		if _, err := w.EnsureBranch(branchID); err != nil {
			return err
		}
	}
	return nil
}
