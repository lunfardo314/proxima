package vertex

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
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

func (o *WrappedOutput) Timestamp() ledger.LogicalTime {
	return o.VID.Timestamp()
}

func (o *WrappedOutput) Slot() ledger.Slot {
	return o.VID.Slot()
}

func (o *WrappedOutput) IsConsumed(tips ...*WrappedTx) bool {
	if len(tips) == 0 {
		return false
	}
	visited := set.New[*WrappedTx]()

	consumed := false
	for _, tip := range tips {
		if consumed = o._isConsumedInThePastConeOf(tip, visited); consumed {
			break
		}
	}
	return consumed
}

func (o *WrappedOutput) _isConsumedInThePastConeOf(vid *WrappedTx, visited set.Set[*WrappedTx]) (consumed bool) {
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)

	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.ForEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
				if o.VID == vidInput {
					consumed = o.Index == v.Tx.MustOutputIndexOfTheInput(i)
				} else {
					consumed = o._isConsumedInThePastConeOf(vidInput, visited)
				}
				return !consumed
			})
			if !consumed {
				v.ForEachEndorsement(func(_ byte, vidEndorsed *WrappedTx) bool {
					consumed = o._isConsumedInThePastConeOf(vidEndorsed, visited)
					return !consumed
				})
			}
		},
	})
	return
}

func (o *WrappedOutput) ValidPace(targetTs ledger.LogicalTime) bool {
	return ledger.ValidTimePace(o.Timestamp(), targetTs)
}

func (o *WrappedOutput) AmountAndLock() (uint64, ledger.Lock, error) {
	oReal, err := o.VID.OutputAt(o.Index)
	if err != nil {
		return 0, nil, err
	}
	return oReal.Amount(), oReal.Lock(), nil
}
