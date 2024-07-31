package vertex

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/lines"
)

func (o *WrappedOutput) DecodeID() *ledger.OutputID {
	if o.VID == nil {
		ret := ledger.NewOutputID(&ledger.TransactionID{}, o.Index)
		return &ret
	}
	ret := o.VID.OutputID(o.Index)
	return &ret
}

func (o *WrappedOutput) IDShortString() string {
	if o == nil {
		return "<nil>"
	}
	return o.DecodeID().StringShort()
}

func (o *WrappedOutput) Timestamp() ledger.Time {
	return o.VID.Timestamp()
}

func (o *WrappedOutput) Slot() ledger.Slot {
	return o.VID.Slot()
}

func (o *WrappedOutput) IsAvailable() (available bool) {
	o.VID.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			available = int(o.Index) < v.Tx.NumProducedOutputs()
		},
		VirtualTx: func(v *VirtualTransaction) {
			_, available = v.OutputAt(o.Index)
		},
	})
	return
}

func WrappedOutputsShortLines(wOuts []WrappedOutput) *lines.Lines {
	ret := lines.New()
	for _, wOut := range wOuts {
		ret.Add(wOut.IDShortString())
	}
	return ret
}
