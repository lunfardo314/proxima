package vertex

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
)

func (o *WrappedOutput) DecodeID() *core.OutputID {
	if o.VID == nil {
		ret := core.NewOutputID(&core.TransactionID{}, o.Index)
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

func (o *WrappedOutput) Amount() uint64 {
	out, err := o.VID.OutputAt(o.Index)
	util.AssertNoError(err)
	return out.Amount()
}

func (o *WrappedOutput) Timestamp() core.LogicalTime {
	return o.VID.Timestamp()
}

func (o *WrappedOutput) TimeSlot() core.TimeSlot {
	return o.VID.TimeSlot()
}
