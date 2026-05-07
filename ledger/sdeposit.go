package ledger

import (
	"encoding/binary"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// _locksExemptOfStorageDeposit lists lock kinds that bypass the framework
// storage-deposit floor. Stem and tagAlong outputs are intentionally
// allowed to be small.
var _locksExemptOfStorageDeposit = set.New(
	StemLockName,
	TagAlongLockName,
)

// DefaultStorageDeposit not always enough
func DefaultStorageDeposit() uint64 {
	return L(0).MinimumInflatableAmount0
}

// StorageDeposit evaluates the EasyFL `storageDeposit($0)` schedule for an
// output of `outputSizeBytes` bytes. Lazily precompiles and caches the
// expression on the receiver library.
func (lib *Library) StorageDeposit(outputSizeBytes uint64) uint64 {
	expr := lib.StorageDepositPrecompiled.Load()
	if expr == nil {
		expr = lib.mustCompile("storageDeposit($0)", 1)
		lib.StorageDepositPrecompiled.Store(expr)
	}
	var sizeBin [8]byte
	binary.BigEndian.PutUint64(sizeBin[:], outputSizeBytes)
	var res []byte
	err := util.CatchPanicOrError(func() error {
		res = easyfl.EvalExpressionWithSlicePool(nil, nil, expr, sizeBin[:])
		return nil
	})
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// LockBytecodeIsStorageDepositExempt reports whether the lock bytecode at
// output element index 2 belongs to the exempt set (stem, tagAlong). For
// any other prefix — including arbitrary EasyFL locks — the output pays
// the standard deposit.
func (lib *Library) LockBytecodeIsStorageDepositExempt(lockBytecode []byte) bool {
	prefix, err := lib.ParsePrefixBytecode(lockBytecode)
	util.AssertNoError(err)
	name, ok := NameByPrefixWithLib(prefix, lib)
	return ok && _locksExemptOfStorageDeposit.Contains(name)
}

// MinimumStorageDeposit on Library uses this library's precompiled
// storageDeposit schedule and exemption set.
func (lib *Library) MinimumStorageDeposit(o *Output) uint64 {
	if lib.LockBytecodeIsStorageDepositExempt(o.MustAt(int(ConstraintIndexLock))) {
		return 0
	}
	return lib.StorageDeposit(uint64(len(o.Bytes())))
}

// MinimumStorageDeposit (free fn) uses the latest library — kept for
// existing callers that don't have a library version pinned.
func MinimumStorageDeposit(o *Output) uint64 {
	return L(base.MaxSlot).MinimumStorageDeposit(o)
}
