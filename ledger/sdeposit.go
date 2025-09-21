package ledger

import (
	"github.com/lunfardo314/proxima/util/set"
)

const vByteCost = 1

var _locksExemptOfStorageDeposit = set.New(
	StemLockName,
	TagAlongLockName,
)

func MinimumStorageDeposit(o *Output) uint64 {
	if _locksExemptOfStorageDeposit.Contains(o.Lock().Name()) {
		return 0
	}
	return vByteCost * uint64(len(o.Bytes()))
}
