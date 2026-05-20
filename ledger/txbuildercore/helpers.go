package txbuildercore

import (
	"sync"

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

// lockBytecodeCache memoises the canonical lock bytecodes per-Library.
// The bytecode is a per-kind constant (sigLock, tagAlong, chainLock,
// …) — every output of that kind shares it. Compiling once and
// caching avoids paying the parse / compile cost per output.
type lockBytecodeCache struct {
	mu    sync.Mutex
	cache map[string][]byte
}

func newLockBytecodeCache() *lockBytecodeCache {
	return &lockBytecodeCache{cache: map[string][]byte{}}
}

func (c *lockBytecodeCache) get(lib *Library, name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.cache[name]; ok {
		return b, nil
	}
	b, err := lib.CompileExpression(name)
	if err != nil {
		return nil, err
	}
	c.cache[name] = b
	return b, nil
}

// Cache is the per-Library bytecode cache. Wallets normally have one
// Library instance, but multiple are supported.
var lockCachesMu sync.Mutex
var lockCaches = map[*Library]*lockBytecodeCache{}

func cacheFor(lib *Library) *lockBytecodeCache {
	lockCachesMu.Lock()
	defer lockCachesMu.Unlock()
	c, ok := lockCaches[lib]
	if !ok {
		c = newLockBytecodeCache()
		lockCaches[lib] = c
	}
	return c
}

// NewSigLockOutput composes the canonical sigLock output:
//
//	slot 0 (amounts):       trimmed-uint64 encoding of `amount`
//	slot 1 (index-values):  tuple holding `holderID` at position 0
//	slot 2 (lock):           canonical sigLock bytecode (per-Library cache)
//
// holderID is the 32-byte hash of (sigType || publicKey) — see
// base.HolderIDFromPublicKey.
func NewSigLockOutput(lib *Library, amount uint64, holderID base.HolderID) (*Output, error) {
	sigLockBin, err := cacheFor(lib).get(lib, SigLockName)
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
func NewChainLockOutput(lib *Library, amount uint64, chainID base.ChainID) (*Output, error) {
	chainLockBin, err := cacheFor(lib).get(lib, ChainLockName)
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
func NewTagAlongOutput(lib *Library, fee uint64, targetSequencerID base.ChainID, senderID base.HolderID) (*Output, error) {
	tagAlongBin, err := cacheFor(lib).get(lib, TagAlongLockName)
	if err != nil {
		return nil, err
	}
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(fee), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{senderID[:], targetSequencerID[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(tagAlongBin, ConstraintIndexLock)
	return b.Output(), nil
}
