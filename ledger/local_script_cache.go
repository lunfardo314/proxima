package ledger

import (
	"container/list"
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

// defaultScriptCacheCapacity bounds the number of decoded scripts retained.
// The working set of distinct covenant scripts in legitimate use is tiny (a
// handful across the whole network), so this is generous for real traffic and
// exists only to cap an open-network griefing vector: a stream of transactions
// each committing a distinct one-byte-tweaked script would otherwise grow the
// cache — and RSS — without bound. Eviction is LRU by count.
const defaultScriptCacheCapacity = 2048

// boundedScriptCache is the default impl: thread-safe LRU with a fixed
// capacity. Cross-transaction reuse (decode once, then serve subsequent txs
// that name the same script) is preserved for the hot working set; only cold
// entries are evicted.
//
// Eviction and the within-tx invariant: redeemScript Puts a hash, then a later
// callRedeemer in the SAME tx Gets it. Those two evals run back-to-back in one
// validation goroutine, so an entry is evicted mid-tx only if `capacity`
// distinct OTHER scripts are Put in that microsecond window — unreachable in
// practice, since validation itself is the bottleneck. If it ever did happen
// the consequence is a recovered panic that rejects that one tx (a liveness
// edge, not a fund-safety issue), which is strictly better than the unbounded
// growth this replaces.
type boundedScriptCache struct {
	mutex    sync.Mutex
	capacity int
	ll       *list.List // front = most recently used
	items    map[[32]byte]*list.Element
}

type scriptCacheEntry struct {
	key    [32]byte
	script *easyfl.LocalScript[*EvalContext]
}

func newBoundedScriptCache(capacity int) *boundedScriptCache {
	return &boundedScriptCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[[32]byte]*list.Element),
	}
}

func (c *boundedScriptCache) Get(h [32]byte) (*easyfl.LocalScript[*EvalContext], bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	el, ok := c.items[h]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*scriptCacheEntry).script, true
}

func (c *boundedScriptCache) Put(h [32]byte, s *easyfl.LocalScript[*EvalContext]) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if el, ok := c.items[h]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*scriptCacheEntry).script = s
		return
	}
	el := c.ll.PushFront(&scriptCacheEntry{key: h, script: s})
	c.items[h] = el
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*scriptCacheEntry).key)
		}
	}
}

// CompiledScriptCache returns the library-level compiled-script cache,
// allocating the default bounded impl on first call. Safe to call from
// any goroutine (sync.Once on first init).
func (lib *Library) CompiledScriptCache() CompiledScriptCache {
	lib.scriptCacheOnce.Do(func() {
		if lib.compiledScriptCache == nil {
			lib.compiledScriptCache = newBoundedScriptCache(defaultScriptCacheCapacity)
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
