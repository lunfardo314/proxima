package glb

import (
	"bytes"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// LockKind classifies an output's lock from the wallet's perspective.
// Computed purely from raw output bytes + the wallet library; no
// dependency on the ledger.L() singleton.
type LockKind int

const (
	// LockKindOther covers any lock the classifier doesn't recognise
	// in the current wallet-side switch (chainLock, delegateLock,
	// tagAlong, stem, …). Callers wanting a finer classification can
	// extend this enum.
	LockKindOther LockKind = iota
	// LockKindSig — public sigLock bytecode. Holder lives in
	// index-values[0] of the output tuple.
	LockKindSig
	// LockKindSWDMaster — sendWithDeadline output where the wallet is
	// the master (reclaim path).
	LockKindSWDMaster
	// LockKindSWDTargetSig — sendWithDeadline output where the wallet
	// is the sigLock target (accept path). Set only when the lock's
	// targetType is SendWithDeadlineTargetSigLock (0x00).
	LockKindSWDTargetSig
)

// lock-symbol constants — duplicated wallet-side so this file has no
// ledger import. Values match ledger.SigLockName and
// ledger.SendWithDeadlineLockName.
const (
	lockSymSig = "sigLock"
	lockSymSWD = "sendWithDeadline"

	swdTargetTypeSigLock byte = 0x00
)

// ClassifyLock returns the lock kind of an output for the given
// wallet, computed from the output's index-values + lock bytes.
//
//	indexValuesBytes — raw bytes at output element index 1
//	                   (txbuildercore.ConstraintIndexIndexValues).
//	lockBytes        — raw bytes at output element index 2.
//	walletHolderID   — 32-byte holder ID (sigLock from wallet privkey).
//
// LockKindOther is returned both for unrecognised locks and for
// classification errors (malformed bytes, mismatched arg shapes).
// Callers that need a hard error path should classify themselves.
func ClassifyLock(lib *txbuildercore.Library, indexValuesBytes, lockBytes []byte, walletHolderID base.HolderID) LockKind {
	sym, _, args, err := lib.ParseBytecodeOneLevel(lockBytes)
	if err != nil {
		return LockKindOther
	}
	switch sym {
	case lockSymSig:
		return LockKindSig

	case lockSymSWD:
		// SWD lock-arg shape: (targetType:1, acceptanceSlots:u32, cleanupSlots:u32).
		// Master + Target IDs live in index-values[0] / [1].
		vals, err := txbuildercore.DecodeIndexValuesTuple(indexValuesBytes)
		if err != nil || len(vals) < 2 || len(vals[0]) != 32 || len(vals[1]) != 32 {
			return LockKindOther
		}
		if bytes.Equal(vals[0], walletHolderID[:]) {
			return LockKindSWDMaster
		}
		// Otherwise the wallet must be on the target side (GetSpendableOutputs
		// already pre-filtered). Confirm the target side is sigLock-flavoured
		// (chainLock targets need a separate flow).
		if len(args) >= 1 {
			tt := easyfl.StripDataPrefix(args[0])
			if len(tt) == 1 && tt[0] == swdTargetTypeSigLock {
				return LockKindSWDTargetSig
			}
		}
		return LockKindOther
	}
	return LockKindOther
}
