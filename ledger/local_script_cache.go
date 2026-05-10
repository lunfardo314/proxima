package ledger

import (
	"sync"

	"github.com/lunfardo314/easyfl"
)

// CompiledScriptCache is the library-level cache of decoded local scripts
// keyed by their content hash. Lifetime equals the *Library lifetime; one
// cache per library version. The cache invariant is
//
//	cache[H] == s  =>  blake2b(s.Bytes()) == H
//
// which holds because every Put happens after redeemScript witnessed the
// hash equality.
type CompiledScriptCache interface {
	Get(hash [32]byte) (*easyfl.LocalScript[*EvalContext], bool)
	Put(hash [32]byte, s *easyfl.LocalScript[*EvalContext])
}

// unboundedScriptCache is the default impl: thread-safe, no eviction. The
// memory bound is the working set of unique scripts seen since process
// start. Eviction-during-tx safety is trivial because nothing is ever
// evicted.
type unboundedScriptCache struct {
	m sync.Map // [32]byte -> *easyfl.LocalScript[*EvalContext]
}

func newUnboundedScriptCache() *unboundedScriptCache {
	return &unboundedScriptCache{}
}

func (c *unboundedScriptCache) Get(h [32]byte) (*easyfl.LocalScript[*EvalContext], bool) {
	v, ok := c.m.Load(h)
	if !ok {
		return nil, false
	}
	return v.(*easyfl.LocalScript[*EvalContext]), true
}

func (c *unboundedScriptCache) Put(h [32]byte, s *easyfl.LocalScript[*EvalContext]) {
	c.m.Store(h, s)
}

// CompiledScriptCache returns the library-level compiled-script cache,
// allocating the default unbounded impl on first call. Safe to call from
// any goroutine (sync.Once on first init).
func (lib *Library) CompiledScriptCache() CompiledScriptCache {
	lib.scriptCacheOnce.Do(func() {
		if lib.compiledScriptCache == nil {
			lib.compiledScriptCache = newUnboundedScriptCache()
		}
	})
	return lib.compiledScriptCache
}

// WithCompiledScriptCache swaps the compiled-script cache impl. Must be
// called before any redeemScript constraint runs against this library.
func (lib *Library) WithCompiledScriptCache(c CompiledScriptCache) *Library {
	lib.compiledScriptCache = c
	return lib
}
