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
	// Look up the lock name from the bytecode prefix only — avoids
	// reconstructing a typed Lock just to read its name (which would
	// fail for arbitrary EasyFL-only locks). Unknown locks are not
	// exempt; they pay the standard storage deposit. See claude/TODO.md.
	lib := L(base.MaxSlot)
	lockBin := o.MustAt(int(ConstraintIndexLock))
	prefix, err := lib.ParsePrefixBytecode(lockBin)
	util.AssertNoError(err)
	if name, ok := NameByPrefixWithLib(prefix, lib); ok && _locksExemptOfStorageDeposit.Contains(name) {
		return 0
	}
	res, err := lib.EvalFromSource(nil, fmt.Sprintf("storageDeposit(u64/%d)", len(o.Bytes())))
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}
