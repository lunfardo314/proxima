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
// storage-deposit floor. Stem and tagAlong outputs are intentionally allowed
// to be small. sendWithDeadline is exempt too: its lifetime is bounded by the
// cleanup deadline (constSendWithDeadlineMaxReclaimSlots ≈ 8.5h), after which
// anyone can consume it, so dust cannot accumulate indefinitely.
var _locksExemptOfStorageDeposit = set.New(
	StemLockName,
	TagAlongLockName,
	SendWithDeadlineLockName,
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
	return lib.StorageDeposit(effectiveStorageSize(o))
}

// effectiveStorageSize is the cost-of-storage proxy fed into the
// storageDeposit schedule. The UTXO contributes its own serialized bytes;
// each entry in the index-values tuple at output element index 1
// additionally costs one trie row of (length byte + value + 33-byte UTXO
// ID) under TriePartitionControllers. The approximation charges:
//
//	utxoBytes + indexValuesTupleBytes + N * 33
//
// where indexValuesTupleBytes ~= sum(value lengths) + small tuple framing.
// Slight under-count of the per-entry 1-byte partition-prefix + 1-byte
// length framing is acceptable; we're billing for persistent state, not
// counting bytes exactly.
func effectiveStorageSize(o *Output) uint64 {
	size := uint64(len(o.Bytes()))
	ivBin, err := o.At(int(ConstraintIndexIndexValues))
	if err != nil || len(ivBin) == 0 {
		return size
	}
	values, err := IndexValuesFromBytes(ivBin)
	if err != nil {
		return size + uint64(len(ivBin))
	}
	return size + uint64(len(ivBin)) + uint64(len(values))*33
}

// MinimumStorageDeposit (free fn) uses the latest library — kept for
// existing callers that don't have a library version pinned.
func MinimumStorageDeposit(o *Output) uint64 {
	return L(base.MaxSlot).MinimumStorageDeposit(o)
}
