package utangle_new

import (
	"context"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

type (
	AttachEnvironment interface {
		Log() *zap.SugaredLogger
		WithGlobalLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *WrappedTx
		AddVertexNoLock(vid *WrappedTx)
		Pull(txid *core.TransactionID)
	}

	attacher struct {
		inChan       chan any
		vid          *WrappedTx
		env          AttachEnvironment
		numMissingTx uint16
		stemSolid    bool
		seqPredSolid bool
	}
)

const periodicCheckEach = 500 * time.Millisecond

func AttachAsync(vid *WrappedTx, env AttachEnvironment, ctx context.Context) {
	env.WithGlobalLock(func() {
		if env.GetVertexNoLock(vid.ID()) != nil {
			return
		}
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				env.AddVertexNoLock(vid)
			},
			VirtualTx: func(v *VirtualTransaction) {
				env.AddVertexNoLock(vid)
			},
			TxID: func(txid *core.TransactionID) {
				env.AddVertexNoLock(vid)
				env.Pull(vid.ID())
			},
			Deleted: vid.PanicAccessDeleted,
		})
		if vid.IsSequencerMilestone() {
			go _attach(vid, env, ctx)
		}
	})
}

func _attach(vid *WrappedTx, env AttachEnvironment, ctx context.Context) {
	exit := false
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			initialSolidification(v)
			exit = v.IsSolid()
		},
		VirtualTx: func(_ *VirtualTransaction) {
			exit = true
		},
		Deleted: vid.PanicAccessDeleted,
	})
	if exit {
		return
	}
	a := newAttacher(vid, env)
	defer a.close()

	for {
		if !vid.isVertex() {
			// stop once it converted to virtualTx or deleted
			return
		}
		select {
		case <-ctx.Done():
			return
		case msg := <-a.inChan:
			if msg == nil {
				return
			}
			if !a.notify(msg) {
				return
			}
		case <-time.After(periodicCheckEach):
			if !a.periodicCheck() {
				return
			}
		}
	}
}

func initialSolidification(v *Vertex) {
	v.forEachInputDependency(func(i byte, vidInput *WrappedTx) bool {

	})
	v.forEachEndorsement(func(i byte, vidEndorsed *WrappedTx) bool {

	})
}

func newAttacher(vid *WrappedTx, env AttachEnvironment) *attacher {
	inChan := make(chan any, 1)
	vid.OnNotify(func(msg any) {
		inChan <- msg
	})

	return &attacher{
		vid:    vid,
		env:    env,
		inChan: inChan,
	}
}

func (a *attacher) close() {
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) notify(msg any) bool {
	if util.IsNil(msg) {
		return false
	}
	switch m := msg.(type) {
	case *WrappedTx:
	}
}

func (a *attacher) periodicCheck() bool {

}
