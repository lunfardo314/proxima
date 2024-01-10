package workflow

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util/set"
)

func (w *Workflow) ListenToAccount(account core.Accountable, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewValidatedTx, func(vid *vertex.WrappedTx) {

	})
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) ListenToSequencers(fun func(vid *vertex.WrappedTx)) {
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) ScanAccount(addr core.AccountID) set.Set[vertex.WrappedOutput] {
	//TODO implement me
	panic("implement me")
}
