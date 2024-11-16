package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

const TraceTagPull = "pull"

func (a *attacher) pullIfNeeded(deptVID *vertex.WrappedTx, tag string) bool {
	if deptVID.IDHasFragment("009d20") {
		a.Log().Infof("@@>> pullIfNeeded %s", deptVID.IDShortString())
	}

	a.Tracef(TraceTagPull, "pullIfNeeded IN (%s): %s", tag, deptVID.IDShortString)

	ok := true
	virtual := false
	deptVID.UnwrapVirtualTx(func(virtualTx *vertex.VirtualTransaction) {
		ok = a.pullIfNeededUnwrapped(virtualTx, deptVID)
		virtual = true
	})

	a.Tracef(TraceTagPull, "pullIfNeeded OUT (%s) (virtual = %v): %s", tag, virtual, deptVID.IDShortString)
	return ok
}

func (a *attacher) pullIfNeededUnwrapped(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx) bool {
	if deptVID.IDHasFragment("009d20") {
		a.Log().Infof("@@>> %s pullIfNeededUnwrapped %s", a.name, deptVID.IDShortString())
	}

	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped IN: %s", deptVID.IDShortString)

	repeatPullAfter, maxPullAttempts, numPeers := a.TxPullParameters()
	if virtualTx.PullRulesDefined() {
		if virtualTx.PullPatienceExpired(maxPullAttempts) {
			// solidification deadline
			a.Log().Errorf("SOLIDIFICATION FAILURE %s at depth %d, hex: %s attacher: %s ",
				deptVID.IDShortString(), deptVID.GetAttachmentDepthNoLock(), deptVID.ID.StringHex(), a.Name())
			a.setError(fmt.Errorf("%w(%d x %v): can't solidify dependency %s",
				ErrSolidificationDeadline, maxPullAttempts, repeatPullAfter, deptVID.IDShortString()))
			return false
		}
		if virtualTx.PullNeeded() {
			a.pull(virtualTx, deptVID, repeatPullAfter, numPeers)
		}
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 1: %s", deptVID.IDShortString)
		if deptVID.IDHasFragment("009d20") {
			a.Log().Infof("@@>> pullIfNeededUnwrapped >> 1 %s", deptVID.IDShortString())
		}
		return true
	}

	if a.pastCone.IsInTheState(deptVID) {
		virtualTx.SetPullNotNeeded()
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 2: %s", deptVID.IDShortString)
		if deptVID.IDHasFragment("009d20") {
			a.Log().Infof("@@>> %s pullIfNeededUnwrapped >> 2 %s  %s", a.name, deptVID.IDShortString(), a.pastCone.Flags(deptVID).String())
		}
		return true
	}
	// no in the state or not known 'inTheState status'

	txBytesWithMetadata := a.TxBytesStore().GetTxBytesWithMetadata(&deptVID.ID)
	if len(txBytesWithMetadata) > 0 {
		virtualTx.SetPullNotNeeded()
		go func() {
			//a.IncCounter("store")
			//defer a.DecCounter("store")

			if _, err := a.TxBytesFromStoreIn(txBytesWithMetadata); err != nil {
				a.Log().Errorf("TxBytesFromStoreIn %s returned '%v'", deptVID.IDShortString(), err)
			}
		}()
		a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 3: %s", deptVID.IDShortString)
		if deptVID.IDHasFragment("009d20") {
			a.Log().Infof("@@>> pullIfNeededUnwrapped >> 3 %s", deptVID.IDShortString())
		}
		return true
	}
	virtualTx.SetPullNeeded()
	a.pull(virtualTx, deptVID, repeatPullAfter, numPeers)
	if deptVID.IDHasFragment("009d20") {
		a.Log().Infof("@@>> pullIfNeededUnwrapped >> 4 %s", deptVID.IDShortString())
	}
	a.Tracef(TraceTagPull, "pullIfNeededUnwrapped OUT 4: %s", deptVID.IDShortString)
	return true
}

func (a *attacher) pull(virtualTx *vertex.VirtualTransaction, deptVID *vertex.WrappedTx, repeatPullAfter time.Duration, nPeers int) {
	if deptVID.IDHasFragment("009d20") {
		a.Log().Infof("@@>> pull %s", deptVID.IDShortString())
	}

	a.Tracef(TraceTagPull, "pull: %s", deptVID.IDShortString)
	a.pokeMe(deptVID)
	// add transaction to the wanted/expected list
	a.AddWantedTransaction(&deptVID.ID)
	a.PullFromNPeers(nPeers, &deptVID.ID)
	virtualTx.SetPullHappened(repeatPullAfter)
}
