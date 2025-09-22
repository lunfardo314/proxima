package ledger

import (
	"github.com/lunfardo314/proxima/util/set"
)

// Storage deposit and vByteCostBase related code

const (
	vByteCostDoubleThreshold = 100
)

var _locksExemptOfStorageDeposit = set.New(
	StemLockName,
	TagAlongLockName,
)

func vByteCostBase() uint64 {
	return Const.MinimumInflatableAmount0 / 100
}

// DefaultStorageDeposit not always enough
func DefaultStorageDeposit() uint64 {
	return vByteCostDoubleThreshold * vByteCostBase()
}

func MinimumStorageDeposit(o *Output) uint64 {
	if _locksExemptOfStorageDeposit.Contains(o.Lock().Name()) {
		return 0
	}
	sz := uint64(len(o.Bytes()))
	b := vByteCostBase()
	if sz <= vByteCostDoubleThreshold {
		return b * sz
	}
	return b*vByteCostDoubleThreshold + 2*(sz-vByteCostDoubleThreshold)*b
}
