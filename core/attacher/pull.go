package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// recordCapBranch adds the branch the attacher just stopped at (it would not pull it because of
// the depth cap) as a forward-sync target. The branch is deterministic for a given lineage, so
// AddSyncTarget is idempotent; we log only the first insert of a target.
func (a *attacher) recordCapBranch(branchID base.TransactionID) {
	if global.AddSyncTarget(branchID) {
		a.Log().Infof("[forward_sync] target added: %s (slot %d), attacher at depth cap %d",
			branchID.StringShort(), branchID.Slot(), a.AttachmentDepthCap())
	}
}

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

	// The depth cap is a PURE CONSTANT given the configuration (AttachmentDepthCap()).
	// The attacher is agnostic about forward sync, the LRB, and any frontier: the only
	// thing that bounds the backward pull is the depth, and the only base case that
	// terminates the recursion is "dependency already in committed state" (handled
	// below via pastCone.IsInTheState). Coupling this to a forward-sync frontier was
	// the 2026-06-20 freeze. See sync_semantics.md §2.
	// Cap only on BRANCH dependencies. Depth counts branches, so the branch the backward walk
	// stops at is exactly the one forward sync must commit (the target). Non-branch deps are
	// always pulled — capping on one would leave forward sync with no committable target.
	depth := deptVID.GetAttachmentDepthNoLock()
	depID := deptVID.ID()
	depIsBranch := depID.IsBranchTransaction()
	isDepthCapped := func() bool {
		return depIsBranch && depth > a.AttachmentDepthCap()
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
			a.recordCapBranch(depID)
		}
		return true
	}

	if a.pastCone.IsInTheState(deptVID) {
		return true
	}

	// not in the state or not known 'inTheState status'

	// Depth cap (branches only): beyond AttachmentDepthCap() branches back, stop descending —
	// even when the branch is available locally in the cache/txstore. Otherwise a single
	// far-ahead milestone makes the recursive walk materialize the whole branch chain back to
	// genesis via the txstore, an unbounded past cone / memDAG (the 2026-06-14 lagging-node
	// wedge: depth 900, memDAG that never heals). The cap is sized by configuration (large when
	// forward sync is off, so recursion alone bridges a realistic gap; small when it is on). At
	// the cap the attacher does not pull the branch but adds it as a forward-sync target and
	// polls until it is committed. SetPullNeeded marks it so the PullRulesDefined branch governs
	// subsequent visits; while capped, PullNeeded/PullPatienceExpired stay false, so it waits
	// without spinning and without a premature solidification deadline.
	if isDepthCapped() {
		virtualTx.SetPullNeeded()
		a.recordCapBranch(depID)
		return true
	}

	// try the transaction cache first (pre-parsed, no re-parsing needed),
	// then fall back to the txstore. Local lookups are cheap and not a DoS vector,
	// unlike peer pulls which are depth-capped.
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
