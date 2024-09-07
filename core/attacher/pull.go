package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

func (a *attacher) pullIfNeeded(deptVID *vertex.WrappedTx) bool {
	ok := true
	deptVID.UnwrapVirtualTx(func(virtualTx *vertex.VirtualTransaction) {
		ok = a.pullIfNeededUnwrapped(virtualTx, deptVID)
	})
	return ok
}

func (a *attacher) pullIfNeededUnwrapped(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	a.Assertf(a.isKnown(deptVID), "a.isKnown(deptVID): %s", deptVID.IDShortString)

	if virtualTx.PullRulesDefined() {
		if virtualTx.PullDeadlineExpired() {
			// deadline expired
			a.setError(fmt.Errorf("%w(%v): can't solidify dependency %s", ErrSolidificationDeadline, PullTimeout, deptVID.IDShortString()))
			return false
		}
		if virtualTx.PullNeeded(PullRepeatPeriod) {
			return a.pull(virtualTx, deptVID)
		}
		return true
	}

	// pull rules not defined
	if a.isKnownRooted(deptVID) {
		virtualTx.SetPullNotNeeded()
		return true
	}

	if a.baselineStateReader().KnowsCommittedTransaction(&deptVID.ID) {
		virtualTx.SetPullNotNeeded()
		a.mustMarkVertexRooted(deptVID)
		return true
	}

	// not rooted transaction with rules not defined
	virtualTx.SetPullDeadline(time.Now().Add(PullTimeout))
	virtualTx.SetLastPullNow()
	return a.pull(virtualTx, deptVID)
}

func (a *attacher) pull(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	txBytesWithMetadata := a.TxBytesStore().GetTxBytesWithMetadata(&deptVID.ID)
	if len(txBytesWithMetadata) > 0 {
		virtualTx.SetPullNotNeeded()
		// loaded from txStore
		txid, err := a.TxBytesFromStoreIn(txBytesWithMetadata)
		if err != nil {
			a.setError(fmt.Errorf("pull(from txstore): %w", err))
			return false
		}
		a.Assertf(*txid == deptVID.ID, "*txid == deptVID.ID")
		return true
	}
	a.PullFromPeers(&deptVID.ID)
	virtualTx.SetLastPullNow()
	return true
}
