package workflow

import "github.com/lunfardo314/proxima/core/vertex"

func (w *Workflow) PostEventNewGood(vid *vertex.WrappedTx) {
	w.Tracef("events", "PostEventNewGood: %s", vid.IDShortString())
	w.events.PostEvent(EventNewGoodTx, vid)
}

func (w *Workflow) PostEventNewTransaction(vid *vertex.WrappedTx) {
	w.Tracef("events", "PostEventNewTransaction: %s", vid.IDShortString())
	w.events.PostEvent(EventNewTx, vid)
}
