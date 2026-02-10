package ledger

import (
	_ "embed"
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

//go:embed def/misc_calc.easyfl
var _miscCalculationsSource string

var _locksExemptOfStorageDeposit = set.New(
	StemLockName,
	TagAlongLockName,
)

// DefaultStorageDeposit not always enough
func DefaultStorageDeposit() uint64 {
	return L(0).MinimumInflatableAmount0
}

func MinimumStorageDeposit(o *Output) uint64 {
	if _locksExemptOfStorageDeposit.Contains(o.Lock().Name()) {
		return 0
	}
	res, err := L(base.MaxSlot).EvalFromSource(nil, fmt.Sprintf("storageDeposit(u64/%d)", len(o.Bytes())))
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}
