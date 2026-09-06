package ledger

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/stretchr/testify/require"
)

// TestBoundedScriptCacheEviction is the regression for audit FEFL-4: the
// compiled-script cache must be bounded so a stream of distinct scripts cannot
// grow RSS without limit. It verifies LRU eviction: once capacity is exceeded
// the least-recently-used entry is dropped, while a recently-touched entry
// survives. Values are nil script pointers; only presence (the ok flag) matters.
func TestBoundedScriptCacheEviction(t *testing.T) {
	const capacity = 4
	c := newBoundedScriptCache(capacity)
	var nilScript *easyfl.LocalScript[*EvalContext]

	key := func(b byte) [32]byte {
		var h [32]byte
		h[0] = b
		return h
	}

	// fill to capacity
	for i := byte(0); i < capacity; i++ {
		c.Put(key(i), nilScript)
	}
	for i := byte(0); i < capacity; i++ {
		_, ok := c.Get(key(i))
		require.True(t, ok, "key %d should still be present", i)
	}

	// touch key 0 so it becomes most-recently-used, then insert a new key:
	// key 1 (now the LRU) must be evicted, key 0 must survive.
	_, _ = c.Get(key(0))
	c.Put(key(100), nilScript)

	_, ok := c.Get(key(1))
	require.False(t, ok, "LRU entry (key 1) must have been evicted")
	_, ok = c.Get(key(0))
	require.True(t, ok, "recently used entry (key 0) must survive")
	_, ok = c.Get(key(100))
	require.True(t, ok, "newest entry must be present")

	require.LessOrEqual(t, c.ll.Len(), capacity, "cache must never exceed capacity")
}
