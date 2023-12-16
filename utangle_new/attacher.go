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
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *WrappedTx
		AddVertexNoLock(vid *WrappedTx)
		GetWrappedOutput(oid *core.OutputID) (WrappedOutput, bool)
		GetVertex(txid *core.TransactionID) *WrappedTx
		Pull(txid *core.TransactionID)
	}

	attacher struct {
		inChan       chan any
		vid          *WrappedTx
		env          AttachEnvironment
		numMissingTx uint16
		stemInputIdx byte
		seqInputIdx  byte
	}
)

const periodicCheckEach = 500 * time.Millisecond

func AttachAsync(vid *WrappedTx, env AttachEnvironment, ctx context.Context) {
	env.WithGlobalWriteLock(func() {
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
	exit, invalid := prepareSolidification(vid, env)
	if invalid {
		vid.SetTxStatus(TxStatusBad)
		vid.NotifyFutureCone()
	}
	if exit {
		return
	}
	a := newAttacher(vid, env)
	defer a.close()

	for !exit {
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
			exit = !a.processNotification(msg)

		case <-time.After(periodicCheckEach):
			exit = !a.periodicCheck()
		}
	}
}

func prepareSolidification(vid *WrappedTx, env AttachEnvironment) (exit, invalid bool) {
	var wOut WrappedOutput
	var stemInputNeedsPull, seqInputNeedsPull bool

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
				if wOut, invalid = env.GetWrappedOutput(oid); invalid {
					return false
				}
				if wOut.VID != nil {
					v.Inputs[i] = wOut.VID
				}
				return true
			})
			v.Tx.ForEachEndorsement(func(idx byte, txid *core.TransactionID) bool {
				v.Endorsements[idx] = env.GetVertex(txid)
				return true
			})

			seqInputIdx := v.SequencerInputIndex()
			if v.Tx.IsBranchTransaction() {
				stemInputIdx := v.StemInputIndex()
				stemInputNeedsPull = v.Inputs[stemInputIdx] == nil
				if stemInputNeedsPull {
					// not available
					stemOid := v.Tx.MustInputAt(stemInputIdx)
					stemTxid := stemOid.TransactionID()
					env.Pull(&stemTxid)
					return
				}
			}

			seqInputNeedsPull = v.Inputs[seqInputIdx] == nil
			if !stemInputNeedsPull && seqInputNeedsPull {
				seqOid := v.Tx.MustInputAt(seqInputIdx)
				seqInTxid := seqOid.TransactionID()
				env.Pull(&seqInTxid)
			}
		},
		VirtualTx: func(_ *VirtualTransaction) {
			exit = true
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

func newAttacher(vid *WrappedTx, env AttachEnvironment) *attacher {
	util.Assertf(vid.IsSequencerMilestone(), "sequencer milestone expected")

	var numMissingTx uint16
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		numMissingTx = uint16(len(v.MissingInputTxIDSet()))
	}})

	inChan := make(chan any, 1)
	vid.OnNotify(func(msg any) {
		inChan <- msg
	})

	return &attacher{
		vid:          vid,
		env:          env,
		inChan:       inChan,
		numMissingTx: numMissingTx,
	}
}

func (a *attacher) close() {
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) processNotification(msg any) bool {
	if util.IsNil(msg) {
		return false
	}
	switch m := msg.(type) {
	case *WrappedTx:
	}
}

func (a *attacher) periodicCheck() bool {

}
