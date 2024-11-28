package vertex

import (
	"sync"

	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

var (
	vidArrPool [256]sync.Pool
	vertexPool sync.Pool
)

func GetVertexArray256(size byte) []*WrappedTx {
	if size == 0 {
		return nil
	}
	r := vidArrPool[size].Get()
	if r == nil {
		return make([]*WrappedTx, size)
	}
	ret := r.([]*WrappedTx)
	util.Assertf(len(ret) == int(size), "len(ret)==size")
	return ret
}

func DisposeVertexArray256(arr []*WrappedTx) {
	util.Assertf(len(arr) < 256, "len(arr) < 256")
	for i := range arr {
		arr[i] = nil
	}
	if len(arr) > 0 {
		vidArrPool[byte(len(arr))].Put(arr)
	}
}

func New(tx *transaction.Transaction) (ret *Vertex) {
	r := vertexPool.Get()
	if r == nil {
		ret = &Vertex{}
	} else {
		ret = r.(*Vertex)
	}
	*ret = Vertex{
		Tx:           tx,
		Inputs:       GetVertexArray256(byte(tx.NumInputs())),
		Endorsements: GetVertexArray256(byte(tx.NumEndorsements())),
	}
	return
}

func (v *Vertex) Dispose() {
	DisposeVertexArray256(v.Inputs)
	DisposeVertexArray256(v.Endorsements)
	*v = Vertex{}
}
