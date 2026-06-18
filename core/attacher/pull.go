package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (a *attacher) pullIfNeeded(deptVID *vertex.WrappedTx) bool {
	ok := true
	// pullFromPeers is only may be needed for the virtual tx
	// all information about pullFromPeers is contained in the vertex. It is equally available to all attachers that need the vertex
	deptVID.UnwrapVirtualTx(func(virtualTx *vertex.VirtualTransaction) {
		ok = a.pullIfNeededUnwrapped(virtualTx, deptVID)
	})
	return ok
}

func (a *attacher) pullIfNeededUnwrapped(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
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
		} else if isDepthCapped() {
			// pull-rules already defined but capped: not pulling, waiting for forward sync
			a.hitDepthCapThisPass = true
		}
		return true
	}

	if a.pastCone.IsInTheState(deptVID) {
		return true
	}

	// not in the state or not known 'inTheState status'

	// Depth cap: in gossip/recursive territory (beyond MaxAttachmentDepthForPull AND
	// after the forward-sync frontier) stop descending — even when the dependency is
	// available locally in the cache/txstore. Otherwise a single far-ahead milestone
	// makes the recursive walk materialize the entire branch chain back to genesis via
	// the txstore, bypassing the cap (the 2026-06-14 lagging-node wedge: depth 900,
	// giant past cone, memDAG that never heals). Deep catch-up belongs to forward-sync,
	// which commits branches in order and advances the frontier until this dependency
	// is at/before it (isDepthCapped == false); a later visit then solidifies it from
	// the local txstore. SetPullNeeded marks it so the PullRulesDefined branch governs
	// subsequent visits; while capped, PullNeeded/PullPatienceExpired stay false, so it
	// waits without spinning and without a premature solidification-deadline failure.
	if isDepthCapped() {
		virtualTx.SetPullNeeded()
		// reached the depth cap on a not-yet-pulled dependency: wait for forward sync
		a.hitDepthCapThisPass = true
		return true
	}

	// try the transaction cache first (pre-parsed, no re-parsing needed),
	// then fall back to the txstore. Local lookups are cheap and not a DoS vector,
	// unlike peer pulls which are depth-capped.
	depID := deptVID.ID()
	if tx := a.TakeCachedTx(util.Ref(depID)); tx != nil {
		a.CachedTxInSolicited(tx)
		return true
	}
	if txBytes := a.GetTxBytes(util.Ref(depID)); len(txBytes) > 0 {
		a.TxBytesFromStoreInSolicited(txBytes)
		return true
	}
	virtualTx.SetPullNeeded()
	a.pullFromPeers(virtualTx, deptVID, repeatPullAfter)
	return true
}

func (a *attacher) pullFromPeers(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx, repeatPullAfter time.Duration) {
	// notify poker to poke add this attacher to notification list of the dependency
	if a.pokeMe != nil {
		a.pokeMe(deptVID)
	}
	// add transaction to the wanted/expected list in the input queue
	a.AddPulledTransaction(deptVID.ID())
	// do not pullFromPeers is node is not connected to any peer longer than 2 pullFromPeers repeat periods
	if a.DurationSinceLastMessageFromPeer() <= 2*repeatPullAfter {
		a.PullFromPeers(deptVID.ID())
		virtualTx.SetPullHappened(repeatPullAfter)
	}
}
