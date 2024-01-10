package workflow

import "github.com/lunfardo314/proxima/utangle/vertex"

func (w *Workflow) PostEventNewGood(vid *vertex.WrappedTx) {
	w.log.Infof("TRACE PostEventNewGood: %s", vid.IDShortString())
	w.events.PostEvent(EventNewGoodTx, vid)
}

func (w *Workflow) PostEventNewValidated(vid *vertex.WrappedTx) {
	w.log.Infof("TRACE PostEventNewValidated: %s", vid.IDShortString())
	w.events.PostEvent(EventNewValidatedTx, vid)
}
