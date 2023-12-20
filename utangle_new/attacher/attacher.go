package attacher

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle_new/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
)

type (
	AttachEnvironment interface {
		Log() *zap.SugaredLogger
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		GetWrappedOutput(oid *core.OutputID) (vertex.WrappedOutput, bool)
		GetVertex(txid *core.TransactionID) *vertex.WrappedTx
		StateStore() global.StateStore
		GetBaselineStateReader(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx, branchData *multistate.BranchData)
		Pull(txid core.TransactionID)
	}

	attacher struct {
		closeMutex          sync.RWMutex
		closed              bool
		inChan              chan *vertex.WrappedTx
		ctx                 context.Context
		vid                 *vertex.WrappedTx
		baselineBranch      *vertex.WrappedTx
		baselineStateReader multistate.SugaredStateReader
		env                 AttachEnvironment
		consumed            map[*vertex.WrappedTx]set.Set[byte]
		rooted              set.Set[*vertex.WrappedTx] // those in the past cone known in the baseline
	}
)

const (
	periodicCheckEach = 500 * time.Millisecond
)

func newAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:      ctx,
		vid:      vid,
		env:      env,
		inChan:   make(chan *vertex.WrappedTx, 1),
		consumed: make(map[*vertex.WrappedTx]set.Set[byte]),
		rooted:   set.New[*vertex.WrappedTx](),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) {
	a := newAttacher(vid, env, ctx)
	defer a.close()

	// first solidify baseline state
	status := a.lazyRepeatUntil(func(v *vertex.Vertex) (exit bool, status vertex.TxStatus) {
		if a.baselineBranch == nil {
			status = a._solidifyBaseline(v)
		}
		return a.baselineBranch != nil, status
	})
	a.vid.SetTxStatus(status)
	if a.isFinalStatus() {
		return
	}
	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")
	// baseline is solid, i.e. we know the baseline state the transactions must be solidified upon
	a.baselineStateReader = multistate.MakeSugared(a.env.GetBaselineStateReader(a.baselineBranch))

	// then continue with the rest
	a.lazyRepeatUntil(func(v *vertex.Vertex) (exit bool, status vertex.TxStatus) {
		return false, a.attachVertex(v, a.vid)
	})
}

func (a *attacher) close() {
	a.closeMutex.Lock()
	defer a.closeMutex.Unlock()

	a.closed = true
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) notify(msg *vertex.WrappedTx) {
	a.closeMutex.RLock()
	defer a.closeMutex.RUnlock()

	if !a.closed {
		a.inChan <- msg
	}
}

func (a *attacher) lazyRepeatUntil(processVertex func(v *vertex.Vertex) (exit bool, status vertex.TxStatus)) (status vertex.TxStatus) {
	var exit bool

	for {
		exit = true
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				exit, status = processVertex(v)
			},
		})
		if exit || a.isFinalStatus() {
			return
		}

		select {
		case <-a.ctx.Done():
			return

		case downstreamVID := <-a.inChan:
			if downstreamVID == nil {
				return
			}
			a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
				exit, status = a.processNotification(v, downstreamVID)
			}})
			if exit || a.isFinalStatus() {
				return
			}

		case <-time.After(periodicCheckEach):
		}
	}
}

func (a *attacher) isFinalStatus() bool {
	return !a.vid.IsVertex() || a.vid.GetTxStatus() != vertex.TxStatusUndefined
}

func (a *attacher) markSuccessfullyConsumed(vid *vertex.WrappedTx, index byte) {
	s := a.consumed[vid]
	if s == nil {
		s = set.New[byte]()
	}
	s.Insert(index)
	a.consumed[vid] = s
}

func (a *attacher) isSuccessfullyConsumed(vid *vertex.WrappedTx, index byte) bool {
	s, found := a.consumed[vid]
	if !found {
		return false
	}
	return s.Contains(index)
}

func (a *attacher) attachOutput(vid *vertex.WrappedTx, index byte) (status vertex.TxStatus) {
	rootedTx := false
	if !a.rooted.Contains(vid) {
		if a.baselineStateReader.KnowsCommittedTransaction(vid.ID()) {
			a.rooted.Insert(vid)
			rootedTx = true
		}
	} else {
		rootedTx = true
	}
	if rootedTx {
		oid := vid.OutputID(index)
		if out := a.baselineStateReader.GetOutput(&oid); out != nil {
			ensured := vid.EnsureOutput(index, out)
			util.Assertf(ensured, "attachInput: inconsistency")
			status = vertex.TxStatusGood
		} else {
			status = vertex.TxStatusBad
		}
		return
	}
	// input tx is not rooted and not bad
	if vid.IsSequencerMilestone() {
		// do not need to recursively pull sequencer transactions because they are pulled by their attachers
		return
	}

	txid := vid.ID()
	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			status = a.attachVertex(v, vid) // recursive
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			a.env.Pull(*txid)
		},
	})
	return
}

func (a *attacher) attachInput(v *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) vertex.TxStatus {
	vidInputTx := v.Inputs[inputIdx]
	if vidInputTx == nil {
		inputOid := v.Tx.MustInputAt(inputIdx)
		// state independent attachment, future cone links, new conflict set propagation
		vidInputTx = attachInputID(&inputOid, consumerTx, a.env)
		v.Inputs[inputIdx] = vidInputTx
	}
	util.Assertf(vidInputTx != nil, "vidInputTx != nil")
	return vidInputTx.GetTxStatus()
}

func (a *attacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx) (status vertex.TxStatus) {
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	status = vertex.TxStatusUndefined
	allGood := true
	for i := range v.Inputs {
		status = a.attachInput(v, vid, byte(i))

		switch status {
		case vertex.TxStatusBad:
			return // invalidate
		case vertex.TxStatusUndefined:
			allGood = false
		}
		util.Assertf(v.Inputs[i] != nil, "v.Inputs[i] != nil")

		inputOidIndex := v.Tx.MustOutputIndexOfTheInput(byte(i))
		status = a.attachOutput(v.Inputs[i], inputOidIndex) // << recursion

		switch status {
		case vertex.TxStatusBad:
			return // Invalidate
		case vertex.TxStatusUndefined:
			allGood = false
		case vertex.TxStatusGood:
			a.markSuccessfullyConsumed(v.Inputs[i], inputOidIndex)
		}

		// TODO absorb forks
	}
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if vidEndorsed == nil {
			vidEndorsed = attachTxID(v.Tx.EndorsementAt(i), a.env, true)
			v.Endorsements[i] = vidEndorsed
		}
		switch vidEndorsed.GetTxStatus() {
		case vertex.TxStatusBad:
			status = vertex.TxStatusBad
			return false
		case vertex.TxStatusUndefined:
			allGood = false
		case vertex.TxStatusGood:
			if !v.EndorsementForkSetAbsorbed[i] {
				if conflict := v.Forks.Absorb(vidEndorsed.Forks()); conflict.VID != nil {
					status = vertex.TxStatusBad
					return false
				}
				v.EndorsementForkSetAbsorbed[i] = true
			}
		}
		return true
	})
	if allGood {
		status = vertex.TxStatusGood
	}
	return status
}

// _solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) _solidifyBaseline(v *vertex.Vertex) (status vertex.TxStatus) {
	if v.Tx.IsBranchTransaction() {
		status = _solidifyStem(v, a.env)
	} else {
		status = _solidifySequencerBaseline(v, a.env)
	}
	a.baselineBranch = v.BaselineBranch
	return
}

func _solidifyStem(v *vertex.Vertex, env AttachEnvironment) (status vertex.TxStatus) {
	status = vertex.TxStatusUndefined
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = attachTxID(stemInputOid.TransactionID(), env, false)
		util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")
	}
	switch v.Inputs[stemInputIdx].GetTxStatus() {
	case vertex.TxStatusGood:
		v.BaselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(v.BaselineBranch != nil, "a.baselineBranch != nil")
	case vertex.TxStatusBad:
		status = vertex.TxStatusBad
	}
	return
}

func _solidifySequencerBaseline(v *vertex.Vertex, env AttachEnvironment) (status vertex.TxStatus) {
	status = vertex.TxStatusUndefined
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = attachTxID(predOid.TransactionID(), env, true)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]
	} else {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = attachTxID(v.Tx.EndorsementAt(0), env, true)
		}
		inputTx = v.Endorsements[0]
	}
	if inputTx.GetTxStatus() == vertex.TxStatusBad {
		status = vertex.TxStatusBad
	} else {
		v.BaselineBranch = inputTx.BaselineBranch() // may be nil
	}
	return
}

func (a *attacher) processNotification(v *vertex.Vertex, vid *vertex.WrappedTx) (exit bool, status vertex.TxStatus) {
	panic("not implemented")
}
