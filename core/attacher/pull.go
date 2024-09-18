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

	repeatPullAfter, maxPullAttempts, numRandomPeers := a.TxPullParameters()

	if virtualTx.PullRulesDefined() {
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules defined", deptVID.IDShortString)

		if virtualTx.PullPatienceExpired(maxPullAttempts) {
			// solidification deadline
			a.Log().Errorf("SOLIDIFICATION FAILURE %s at depth %d, hex: %s attacher: %s ",
				deptVID.IDShortString(), deptVID.GetAttachmentDepthNoLock(), deptVID.ID.StringHex(), a.Name())
			a.setError(fmt.Errorf("%w(%d x %v): can't solidify dependency %s",
				ErrSolidificationDeadline, maxPullAttempts, repeatPullAfter, deptVID.IDShortString()))
			return false
		}
		if virtualTx.PullNeeded() {
			return a.pull(virtualTx, deptVID, repeatPullAfter, numRandomPeers)
		}
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules defined. Pull NOT NEEDED", deptVID.IDShortString)
		return true
	}
	// pull rules have not been defined yet
	a.checkRootedStatus(deptVID)

	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules not defined", deptVID.IDShortString)
	if a.isKnownRooted(deptVID) {
		virtualTx.SetPullNotNeeded()
		return true
	}

	// wasn't checked root status yet or not rooted transaction
	// define pull rules by setting pull deadline and pull
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Set pull timeout and pull", deptVID.IDShortString)
	virtualTx.SetPullNeeded()
	return a.pull(virtualTx, deptVID, repeatPullAfter, numRandomPeers)
}

func (a *attacher) pull(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx, repeatPullAfter time.Duration, nRandomPeers int) bool {
	a.Tracef(TraceTagPull, "pull IN %s", deptVID.IDShortString)
	defer a.Tracef(TraceTagPull, "pull OUT %s", deptVID.IDShortString)

	// TODO prevent repetitive reading from DB every pull

	txBytesWithMetadata := a.TxBytesStore().GetTxBytesWithMetadata(&deptVID.ID)
	if len(txBytesWithMetadata) > 0 {
		a.Tracef(TraceTagPull, "pull found in store %s", deptVID.IDShortString)

		virtualTx.SetPullNotNeeded()

		go func() {
			a.IncCounter("store")
			defer a.DecCounter("store")

			if _, err := a.TxBytesFromStoreIn(txBytesWithMetadata); err != nil {
				a.Log().Errorf("TxBytesFromStoreIn %s returned '%v'", deptVID.IDShortString(), err)
			}
		}()
		return true
	}
	a.Tracef(TraceTagPull, "pull NOT found in store %s", deptVID.IDShortString)
	// failed to load txBytes from store -> pull it from peers
	a.pokeMe(deptVID)

	a.AddPulledTransaction(&deptVID.ID)
	nPulls := a.PullFromRandomPeers(nRandomPeers, &deptVID.ID)
	virtualTx.SetPullHappened(nPulls, repeatPullAfter)
	return true
}
