package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

const TraceTagPull = "pull"

func (a *attacher) pullIfNeeded(deptVID *vertex.WrappedTx, tag string) bool {
	a.Tracef(TraceTagPull, "pullIfNeeded IN (%s): %s", tag, deptVID.IDShortString)
	ok := true
	virtual := false
	// pull is only may be needed for the virtual tx
	// all information about pull is contained in the vertex. It is equally available to all attacher
	// which needs the vertex
	deptVID.UnwrapVirtualTx(func(virtualTx *vertex.VirtualTransaction) {
		ok = a.pullIfNeededUnwrapped(virtualTx, deptVID)
		virtual = true
	})

	a.Tracef(TraceTagPull, "pullIfNeeded OUT (%s) (virtual = %v): %s", tag, virtual, deptVID.IDShortString)
	return ok
}

func (a *attacher) pullIfNeededUnwrapped(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped IN: %s", deptVID.IDShortString)

	repeatPullAfter, maxPullAttempts, numPeers := a.TxPullParameters()
	if virtualTx.PullRulesDefined() {
		if virtualTx.PullPatienceExpired(maxPullAttempts) {
			// solidification deadline
			a.Log().Errorf("SOLIDIFICATION FAILURE %s at depth %d, hex: %s attacher: %s ",
				deptVID.IDShortString(), deptVID.GetAttachmentDepthNoLock(), deptVID.ID.StringHex(), a.Name())
			a.setError(fmt.Errorf("%w(%d x %v): can't solidify %s",
				ErrSolidificationDeadline, maxPullAttempts, repeatPullAfter, deptVID.IDShortString()))
			return false
		}
		if virtualTx.PullNeeded() {
			a.pull(virtualTx, deptVID, repeatPullAfter, numPeers)
		}
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 1: %s", deptVID.IDShortString)
		return true
	}

	if a.pastCone.IsInTheState(deptVID) {
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 2: %s", deptVID.IDShortString)
		return true
	}

	// not in the state or not known 'inTheState status'

	// try to find in the local txBytes store
	txBytesWithMetadata := a.TxBytesStore().GetTxBytesWithMetadata(&deptVID.ID)
	if len(txBytesWithMetadata) > 0 {
		go func() {
			//a.IncCounter("store")
			//defer a.DecCounter("store")

			if _, err := a.TxBytesFromStoreIn(txBytesWithMetadata); err != nil {
				a.Log().Errorf("TxBytesFromStoreIn %s returned '%v'", deptVID.IDShortString(), err)
			}
		}()
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 3: %s", deptVID.IDShortString)
		return true
	}
	virtualTx.SetPullNeeded()
	a.pull(virtualTx, deptVID, repeatPullAfter, numPeers)
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 4: %s", deptVID.IDShortString)
	return true
}

func (a *attacher) pull(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx, repeatPullAfter time.Duration, nPeers int) {
	// notify poker to poke add this attacher to notification list of the dependency
	a.pokeMe(deptVID)
	// add transaction to the wanted/expected list in the input queue
	a.AddWantedTransaction(&deptVID.ID)
	// do not pull is node is not connected to any peer longer than 2 pull repeat periods
	if a.DurationSinceLastMessageFromPeer() <= 2*repeatPullAfter {
		a.PullFromNPeers(nPeers, &deptVID.ID)
		virtualTx.SetPullHappened(repeatPullAfter)

		a.Tracef(TraceTagPull, "pull: %s", deptVID.IDShortString)
	} else {
		a.Tracef(TraceTagPull, "pull postponed (node disconnected): %s", deptVID.IDShortString)
	}
}
