package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/util"
)

const TraceTagPull = "pullFromPeers"

func (a *attacher) pullIfNeeded(deptVID *vertex.WrappedTx, tag string) bool {
	a.Tracef(TraceTagPull, "pullIfNeeded IN (%s): %s", tag, deptVID.IDShortString)
	ok := true
	virtual := false
	// pullFromPeers is only may be needed for the virtual tx
	// all information about pullFromPeers is contained in the vertex. It is equally available to all attachers that need the vertex
	deptVID.UnwrapVirtualTx(func(virtualTx *vertex.VirtualTransaction) {
		ok = a.pullIfNeededUnwrapped(virtualTx, deptVID)
		virtual = true
	})

	a.Tracef(TraceTagPull, "pullIfNeeded OUT (%s) (virtual = %v): %s", tag, virtual, deptVID.IDShortString)
	return ok
}

func (a *attacher) pullIfNeededUnwrapped(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped IN: %s", deptVID.IDShortString)

	repeatPullAfter, maxPullAttempts := a.TxPullParameters()

	// depth cap applies only to gossip-driven recursion (txs after the forward-sync frontier).
	// txs in forward-sync territory (at or before the frontier) are exempt —
	// their depth is bounded naturally by the slot structure.
	depth := deptVID.GetAttachmentDepthNoLock()
	depTs := deptVID.Timestamp()
	isDepthCapped := func() bool {
		return depth > vertex.MaxAttachmentDepthForPull && depTs.After(a.LatestForwardSyncedTimestamp())
	}

	if virtualTx.PullRulesDefined() {
		if virtualTx.PullPatienceExpired(maxPullAttempts, isDepthCapped) {
			// solidification deadline
			a.Log().Errorf("SOLIDIFICATION FAILURE %s at depth %d, hex: %s attacher: %s ",
				deptVID.IDShortString(), depth, util.Ref(deptVID.ID()).StringHex(), a.Name())
			a.setError(fmt.Errorf("%w(%d x %v): can't solidify %s",
				ErrSolidificationDeadline, maxPullAttempts, repeatPullAfter, deptVID.IDShortString()))
			return false
		}
		if virtualTx.PullNeeded(isDepthCapped) {
			a.pullFromPeers(virtualTx, deptVID, repeatPullAfter)
		}
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 1: %s", deptVID.IDShortString)
		return true
	}

	if a.pastCone.IsInTheState(deptVID) {
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 2: %s", deptVID.IDShortString)
		return true
	}

	// not in the state or not known 'inTheState status'

	// always try the local txBytes store — local lookups are cheap and not a DoS vector,
	// unlike peer pulls which are depth-capped to prevent unbounded network amplification
	txBytesWithMetadata := a.TxBytesStore().GetTxBytesWithMetadata(util.Ref(deptVID.ID()))
	if len(txBytesWithMetadata) > 0 {
		// mark as pulled so re-injected tx passes rate control
		a.AddPulledTransaction(deptVID.ID())
		go func() {
			if _, err := a.TxBytesFromStoreIn(txBytesWithMetadata); err != nil {
				a.Log().Errorf("TxBytesFromStoreIn %s returned '%v'", deptVID.IDShortString(), err)
			}
		}()
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 3 (txstore): %s", deptVID.IDShortString)
		return true
	}
	if isDepthCapped() {
		a.Tracef("sync", "depth cap: skip peer pull for %s at depth %d (cap=%d), attacher=%s",
			deptVID.IDShortString, depth, vertex.MaxAttachmentDepthForPull, a.Name())
	}
	virtualTx.SetPullNeeded()
	if !isDepthCapped() {
		a.pullFromPeers(virtualTx, deptVID, repeatPullAfter)
	}
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 4: %s", deptVID.IDShortString)
	return true
}

func (a *attacher) pullFromPeers(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx, repeatPullAfter time.Duration) {
	// notify poker to poke add this attacher to notification list of the dependency
	a.pokeMe(deptVID)
	// add transaction to the wanted/expected list in the input queue
	a.AddPulledTransaction(deptVID.ID())
	// do not pullFromPeers is node is not connected to any peer longer than 2 pullFromPeers repeat periods
	if a.DurationSinceLastMessageFromPeer() <= 2*repeatPullAfter {
		a.PullFromPeers(deptVID.ID())
		virtualTx.SetPullHappened(repeatPullAfter)

		a.Tracef(TraceTagPull, "pullFromPeers: %s", deptVID.IDShortString)
	} else {
		a.Tracef(TraceTagPull, "pullFromPeers postponed (node disconnected): %s", deptVID.IDShortString)
	}
}
