package txbuildercore

import (
	"github.com/lunfardo314/proxima/ledger/base"
)

// Common-element wallet helpers. Each helper produces a *txbuildercore.Output
// for one of the lock kinds the wallet routinely composes. The
// canonical lock bytecode is the same for every output of a given
// kind (the holder/target data lives in the index-value tuple at slot
// 1, not in the lock slot); the helpers cache it per-Library so a
// long-running wallet pays the compile cost once.
//
// Server-side `ledger.New<Foo>` constructors keep their existing
// signatures and either delegate here or use the global library;
// either way the emitted bytes are identical.

const (
	// SigLockName is the symbol of the canonical sig-lock function.
	// Matches ledger/lock_signature.go.
	SigLockName = "sigLock"

	// TagAlongLockName is the symbol of the canonical tag-along lock
	// function. Matches ledger/lock_tag_along.go.
	TagAlongLockName = "tagAlong"

	// ChainLockName is the symbol of the canonical chain-lock function.
	// Matches ledger/lock_chain.go.
	ChainLockName = "chainLock"
)

// lockBytecode returns the canonical bytecode for lock name, compiling
// once and caching per-Library. The cache is initialised on first use.
func (l *Library[T]) lockBytecode(name string) ([]byte, error) {
	l.lockCacheMu.Lock()
	defer l.lockCacheMu.Unlock()
	if l.lockCache == nil {
		l.lockCache = map[string][]byte{}
	}
	if b, ok := l.lockCache[name]; ok {
		return b, nil
	}
	b, err := l.CompileExpression(name)
	if err != nil {
		return nil, err
	}
	l.lockCache[name] = b
	return b, nil
}

// NewSigLockOutput composes the canonical sigLock output:
//
//	slot 0 (amounts):       trimmed-uint64 encoding of `amount`
//	slot 1 (index-values):  tuple holding `holderID` at position 0
//	slot 2 (lock):           canonical sigLock bytecode (per-Library cache)
//
// holderID is the 32-byte hash of (sigType || publicKey) — see
// base.HolderIDFromPublicKey.
func NewSigLockOutput(lib *Library[any], amount uint64, holderID base.HolderID) (*Output, error) {
	sigLockBin, err := lib.lockBytecode(SigLockName)
	if err != nil {
		return nil, err
	}
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(amount), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{holderID[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(sigLockBin, ConstraintIndexLock)
	return b.Output(), nil
}

// NewChainLockOutput composes the canonical chainLock output:
//
//	slot 0 (amounts):       trimmed-uint64 encoding of `amount`
//	slot 1 (index-values):  tuple holding `chainID` at position 0
//	slot 2 (lock):          canonical chainLock bytecode (per-Library cache)
//
// chainID is the 32-byte chain identifier of the chain that controls
// this output (the chain's controller is the spender).
func NewChainLockOutput(lib *Library[any], amount uint64, chainID base.ChainID) (*Output, error) {
	chainLockBin, err := lib.lockBytecode(ChainLockName)
	if err != nil {
		return nil, err
	}
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(amount), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{chainID[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(chainLockBin, ConstraintIndexLock)
	return b.Output(), nil
}

// NewTagAlongOutput composes the canonical tag-along output:
//
//	slot 0 (amounts):       trimmed-uint64 encoding of `fee`
//	slot 1 (index-values):  tuple [senderID, targetSequencerID]
//	                        (sender at position 0 per §4.1 master-first)
//	slot 2 (lock):          canonical tagAlong bytecode (per-Library cache)
func NewTagAlongOutput(lib *Library[any], fee uint64, targetSequencerID base.ChainID, senderID base.HolderID) (*Output, error) {
	tagAlongBin, err := lib.lockBytecode(TagAlongLockName)
	if err != nil {
		return nil, err
	}
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(fee), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{senderID[:], targetSequencerID[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(tagAlongBin, ConstraintIndexLock)
	return b.Output(), nil
}
