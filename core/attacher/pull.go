package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

const TraceTagPull = "pull"

// TODO pull every transaction which is not in the state?

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

	//a.Assertf(a.pastCone.IsKnown(deptVID), "a.IsKnown(deptVID): %s", deptVID.IDShortString)

	repeatPullAfter, maxPullAttempts, numPeers := a.TxPullParameters()

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
			return a.pull(virtualTx, deptVID, repeatPullAfter, numPeers)
		}
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped: %s. Pull rules defined. Pull NOT NEEDED", deptVID.IDShortString)
		return true
	}

	// at this point inTheState status must be fully defined
	flags := a.pastCone.Flags(deptVID)
	a.Assertf(flags.FlagsUp(vertex.FlagPastConeVertexCheckedInTheState), "a.pastCone.Flags(deptVID).FlagsUp(vertex.FlagPastConeVertexCheckedInTheState)")

	if flags.FlagsUp(vertex.FlagPastConeVertexInTheState) {
		virtualTx.SetPullNotNeeded()
		return true
	}
	virtualTx.SetPullNeeded()
	return a.pull(virtualTx, deptVID, repeatPullAfter, numPeers)
}

func (a *attacher) pull(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx, repeatPullAfter time.Duration, nPeers int) bool {
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

	// add transaction to the wanted/expected list

	a.AddWantedTransaction(&deptVID.ID)
	nPulls := a.PullFromNPeers(nPeers, &deptVID.ID)
	virtualTx.SetPullHappened(nPulls, repeatPullAfter)
	return true
}
