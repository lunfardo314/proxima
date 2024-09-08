package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

const TraceTagPull = "pull"

func (a *attacher) pullIfNeeded(deptVID *vertex.WrappedTx) bool {
	a.Tracef(TraceTagPull, "pullIfNeeded IN: %s", deptVID.IDShortString)
	defer a.Tracef(TraceTagPull, "pullIfNeeded OUT: %s", deptVID.IDShortString)

	ok := true
	deptVID.UnwrapVirtualTx(func(virtualTx *vertex.VirtualTransaction) {
		ok = a.pullIfNeededUnwrapped(virtualTx, deptVID)
	})
	return ok
}

func (a *attacher) pullIfNeededUnwrapped(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped IN: %s", deptVID.IDShortString)
	defer a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT: %s", deptVID.IDShortString)

	a.Assertf(a.isKnown(deptVID), "a.isKnown(deptVID): %s", deptVID.IDShortString)

	if virtualTx.PullRulesDefined() {
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules defined", deptVID.IDShortString)

		if virtualTx.PullDeadlineExpired() {
			// deadline expired
			a.setError(fmt.Errorf("%w(%v): can't solidify dependency %s", ErrSolidificationDeadline, PullTimeout, deptVID.IDShortString()))
			return false
		}
		if virtualTx.PullNeeded(PullRepeatPeriod) {
			return a.pull(virtualTx, deptVID)
		}
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules defined. Pull NOT NEEDED", deptVID.IDShortString)
		return true
	}

	// pull rules not defined
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules not defined", deptVID.IDShortString)
	if a.isKnownRooted(deptVID) {
		virtualTx.SetPullNotNeeded()
		return true
	}

	if a.baseline != nil && a.baselineStateReader().KnowsCommittedTransaction(&deptVID.ID) {
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Known in the state", deptVID.IDShortString)
		virtualTx.SetPullNotNeeded()
		a.mustMarkVertexRooted(deptVID)
		return true
	}

	// not rooted transaction with rules not defined
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Set pull timeout and pull", deptVID.IDShortString)
	virtualTx.SetPullDeadline(time.Now().Add(PullTimeout))
	return a.pull(virtualTx, deptVID)
}

func (a *attacher) pull(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	a.Tracef(TraceTagPull, "pull IN %s", deptVID.IDShortString)
	defer a.Tracef(TraceTagPull, "pull OUT %s", deptVID.IDShortString)

	txBytesWithMetadata := a.TxBytesStore().GetTxBytesWithMetadata(&deptVID.ID)
	if len(txBytesWithMetadata) > 0 {
		a.Tracef(TraceTagPull, "pull found in store %s", deptVID.IDShortString)

		virtualTx.SetPullNotNeeded()
		go func() {
			if _, err := a.TxBytesFromStoreIn(txBytesWithMetadata); err != nil {
				a.Log().Errorf("TxBytesFromStoreIn %s returned '%v'", deptVID.IDShortString(), err)
			}
		}()
		return true
	}
	a.Tracef(TraceTagPull, "pull NOT found in store %s", deptVID.IDShortString)
	// failed to load txBytes from store -> pull it from peers
	a.PullFromPeers(&deptVID.ID)
	virtualTx.SetLastPullNow()
	a.pokeMe(deptVID)
	return true
}
