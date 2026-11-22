package ingate

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInputGate(t *testing.T) {
	t.Run("basic checkPass", func(t *testing.T) {
		// Tests that a new key passes through, and subsequent checks don't pass
		g := New[int](10*time.Second, 10000, 0)
		pass, wanted := g.CheckPass(1)
		require.True(t, pass)
		require.False(t, wanted)

		pass, _ = g.CheckPass(1)
		require.False(t, pass)

		g.AddPulled(5)
		pass, wanted = g.CheckPass(5)
		require.True(t, pass)
		require.True(t, wanted)

		// a pulled key keeps passing until a copy is accepted
		pass, wanted = g.CheckPass(5)
		require.True(t, pass)
		require.True(t, wanted)
		g.Accept(5)
		pass, _ = g.CheckPass(5)
		require.False(t, pass)

		g.AddPulled(5)
		pass, wanted = g.CheckPass(5)
		require.True(t, pass)
		require.True(t, wanted)
	})

	t.Run("refused key stays closed", func(t *testing.T) {
		// A refused key no longer passes, even when pulled again: every copy of it
		// would be refused the same way, so it is parsed and logged once per TTL.
		g := New[int](10*time.Second, 10000, 0)
		g.AddPulled(7)
		pass, wanted := g.CheckPass(7)
		require.True(t, pass)
		require.True(t, wanted)

		g.Refuse(7)
		pass, _ = g.CheckPass(7)
		require.False(t, pass)
		g.AddPulled(7)
		pass, wanted = g.CheckPass(7)
		require.False(t, pass)
		require.False(t, wanted)
		require.False(t, g.WasPulled(7))
	})

	t.Run("multiple different keys", func(t *testing.T) {
		// Tests that different keys are tracked independently
		g := New[int](10*time.Second, 10000, 0)

		// All different keys should pass on first check
		for i := 0; i < 100; i++ {
			pass, wanted := g.CheckPass(i)
			require.True(t, pass, "key %d should pass on first check", i)
			require.False(t, wanted, "key %d was not pulled", i)
		}

		// All keys should now be blocked
		for i := 0; i < 100; i++ {
			pass, _ := g.CheckPass(i)
			require.False(t, pass, "key %d should be blocked on second check", i)
		}

		require.Equal(t, 100, len(g.m))
	})

	t.Run("addPulled before checkPass", func(t *testing.T) {
		// Tests that marking a key as pulled before checking allows it to pass with wanted=true
		g := New[int](10*time.Second, 10000, 0)

		g.AddPulled(42)
		pass, wanted := g.CheckPass(42)
		require.True(t, pass, "pulled key should pass")
		require.True(t, wanted, "pulled key should be wanted")

		// A refused copy (unsee) leaves the key pulled, so the genuine reply still passes
		g.Unsee(42)
		pass, wanted = g.CheckPass(42)
		require.True(t, pass, "pulled key should pass again after a refused copy")
		require.True(t, wanted, "pulled key should stay wanted after a refused copy")

		// After accept, entry is updated with wasPulled=false, so next check should fail
		g.Accept(42)
		pass, wanted = g.CheckPass(42)
		require.False(t, pass, "key should be blocked after a copy is accepted")
		require.False(t, wanted, "key should not be wanted after accept cleared it")
	})

	t.Run("addPulled can be called multiple times", func(t *testing.T) {
		// Tests that addPulled can reset a blocked key
		g := New[int](10*time.Second, 10000, 0)

		// First check passes
		pass, _ := g.CheckPass(1)
		require.True(t, pass)

		// Second check fails
		pass, _ = g.CheckPass(1)
		require.False(t, pass)

		// addPulled resets it
		g.AddPulled(1)
		pass, wanted := g.CheckPass(1)
		require.True(t, pass, "addPulled should allow key to pass again")
		require.True(t, wanted)

		// Can be done repeatedly
		g.AddPulled(1)
		g.AddPulled(1) // Multiple calls are fine
		pass, wanted = g.CheckPass(1)
		require.True(t, pass)
		require.True(t, wanted)
	})

	t.Run("string keys", func(t *testing.T) {
		// Tests that generic type works with strings
		g := New[string](10*time.Second, 10000, 0)

		pass, wanted := g.CheckPass("hello")
		require.True(t, pass)
		require.False(t, wanted)

		pass, _ = g.CheckPass("hello")
		require.False(t, pass)

		g.AddPulled("world")
		pass, wanted = g.CheckPass("world")
		require.True(t, pass)
		require.True(t, wanted)
	})
}

func TestInGatePurge(t *testing.T) {
	t.Run("purge does nothing below threshold", func(t *testing.T) {
		// Tests that purgeInGate doesn't remove entries when under cleanWhenExceedsSize
		g := New[int](1*time.Millisecond, 100, 0)

		// Add 50 entries (below threshold of 100)
		for i := 0; i < 50; i++ {
			g.CheckPass(i)
		}
		require.Equal(t, 50, len(g.m))

		// Wait for TTL to expire
		time.Sleep(5 * time.Millisecond)

		// Purge should not remove anything because we're below threshold
		g.Purge()
		require.Equal(t, 50, len(g.m))
	})

	t.Run("purge removes expired entries above threshold", func(t *testing.T) {
		// Tests that purgeInGate removes expired entries when above cleanWhenExceedsSize
		g := New[int](1*time.Millisecond, 10, 0)

		// Add 20 entries (above threshold of 10)
		for i := 0; i < 20; i++ {
			g.CheckPass(i)
		}
		require.Equal(t, 20, len(g.m))

		// Wait for TTL to expire
		time.Sleep(5 * time.Millisecond)

		// Purge should remove all expired entries
		g.Purge()
		require.Equal(t, 0, len(g.m))
	})

	t.Run("purge keeps non-expired entries", func(t *testing.T) {
		// Tests that purgeInGate only removes expired entries, keeping fresh ones
		g := New[int](100*time.Millisecond, 5, 0)

		// Add 10 entries (above threshold of 5)
		for i := 0; i < 10; i++ {
			g.CheckPass(i)
		}
		require.Equal(t, 10, len(g.m))

		// Purge immediately - nothing should be removed (TTL not expired)
		g.Purge()
		require.Equal(t, 10, len(g.m))
	})

	t.Run("purge mixed expired and fresh", func(t *testing.T) {
		// Tests that purge removes only expired entries when mixed with fresh ones
		g := New[int](5*time.Millisecond, 5, 0)

		// Add 10 old entries
		for i := 0; i < 10; i++ {
			g.CheckPass(i)
		}

		// Wait for them to expire
		time.Sleep(10 * time.Millisecond)

		// Add 5 fresh entries
		for i := 100; i < 105; i++ {
			g.CheckPass(i)
		}
		require.Equal(t, 15, len(g.m))

		// Purge should remove the 10 old entries, keep the 5 fresh ones
		g.Purge()
		require.Equal(t, 5, len(g.m))

		// Verify fresh entries are still there
		for i := 100; i < 105; i++ {
			pass, _ := g.CheckPass(i)
			require.False(t, pass, "fresh entry %d should still be tracked", i)
		}
	})
}

func TestInGateHardCap(t *testing.T) {
	// Entries that never expire cannot be bounded by the TTL alone, which is what a flood
	// of never-repeating ids produces. Over the hard cap the purge must evict the oldest
	// entries down to half the cap and keep the newest.
	g := New[int](time.Hour, 0, 100)
	for i := 0; i < 250; i++ {
		g.CheckPass(i)
		time.Sleep(10 * time.Microsecond) // distinct deadlines, oldest first
	}
	require.Equal(t, 250, len(g.m))
	g.Purge()
	require.Equal(t, 50, len(g.m))
	for i := 200; i < 250; i++ {
		pass, _ := g.CheckPass(i)
		require.False(t, pass, "newest entry %d must survive the eviction", i)
	}

	// unsee drops a record so the key is admitted again
	g.Unsee(249)
	pass, _ := g.CheckPass(249)
	require.True(t, pass)
}

func TestInGateRecreateMap(t *testing.T) {
	t.Run("recreateMap preserves entries", func(t *testing.T) {
		// Tests that recreateMap clones the map without losing data
		g := New[int](10*time.Second, 10000, 0)

		// Add some entries
		for i := 0; i < 10; i++ {
			g.CheckPass(i)
		}
		g.AddPulled(100)

		originalLen := len(g.m)

		// Recreate map
		g.RecreateMap()

		// Same length
		require.Equal(t, originalLen, len(g.m))

		// Entries should still block
		for i := 0; i < 10; i++ {
			pass, _ := g.CheckPass(i)
			require.False(t, pass, "entry %d should still be tracked after recreateMap", i)
		}

		// Pulled entry should still pass with wanted=true
		pass, wanted := g.CheckPass(100)
		require.True(t, pass)
		require.True(t, wanted)
	})

	t.Run("recreateMap on empty gate", func(t *testing.T) {
		// Tests that recreateMap works on empty map
		g := New[int](10*time.Second, 10000, 0)

		g.RecreateMap()
		require.Equal(t, 0, len(g.m))

		// Should still work normally after
		pass, _ := g.CheckPass(1)
		require.True(t, pass)
	})
}

func TestInGateConcurrency(t *testing.T) {
	t.Run("concurrent checkPass", func(t *testing.T) {
		// Tests that concurrent access to checkPass is safe
		g := New[int](10*time.Second, 100000, 0)

		var wg sync.WaitGroup
		passCount := make([]int, 10)

		// 10 goroutines each trying to pass the same 100 keys
		for goroutine := 0; goroutine < 10; goroutine++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				for key := 0; key < 100; key++ {
					pass, _ := g.CheckPass(key)
					if pass {
						passCount[idx]++
					}
				}
			}(goroutine)
		}

		wg.Wait()

		// Total passes should equal exactly 100 (each key passes exactly once)
		total := 0
		for _, count := range passCount {
			total += count
		}
		require.Equal(t, 100, total, "each key should pass exactly once across all goroutines")
	})

	t.Run("concurrent addPulled and checkPass", func(t *testing.T) {
		// Tests concurrent addPulled and checkPass don't cause data races
		g := New[int](10*time.Second, 100000, 0)

		var wg sync.WaitGroup

		// Goroutines adding pulled
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					g.AddPulled(j)
				}
			}()
		}

		// Goroutines checking pass
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					g.CheckPass(j)
				}
			}()
		}

		wg.Wait()
		// No race detector errors = success
	})

	t.Run("concurrent purge and checkPass", func(t *testing.T) {
		// Tests concurrent purgeInGate and checkPass don't cause data races
		g := New[int](1*time.Millisecond, 10, 0)

		var wg sync.WaitGroup

		// Goroutines adding entries
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					g.CheckPass(offset*100 + j)
				}
			}(i)
		}

		// Goroutines purging
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					time.Sleep(100 * time.Microsecond)
					g.Purge()
				}
			}()
		}

		wg.Wait()
		// No race detector errors = success
	})
}

func TestInGateEdgeCases(t *testing.T) {
	t.Run("zero TTL", func(t *testing.T) {
		// Tests behavior with zero TTL - entries expire immediately
		g := New[int](0, 5, 0)

		g.CheckPass(1)
		g.CheckPass(2)
		g.CheckPass(3)

		// Even with zero TTL, entries are still tracked until purge
		pass, _ := g.CheckPass(1)
		require.False(t, pass)

		// After purge (above threshold), entries should be removed
		g.CheckPass(4)
		g.CheckPass(5)
		g.CheckPass(6) // Now we have 6 entries, above threshold of 5

		g.Purge()

		// All entries should be purged (zero TTL means already expired)
		require.Equal(t, 0, len(g.m))
	})

	t.Run("threshold of zero", func(t *testing.T) {
		// Tests behavior with cleanWhenExceedsSize = 0 (always purge)
		g := New[int](1*time.Millisecond, 0, 0)

		g.CheckPass(1)
		time.Sleep(5 * time.Millisecond)

		// With threshold 0, purge should always run
		g.Purge()
		require.Equal(t, 0, len(g.m))
	})

	t.Run("checkPass updates deadline", func(t *testing.T) {
		// Tests that checkPass refreshes the purge deadline
		g := New[int](50*time.Millisecond, 0, 0)

		g.CheckPass(1)

		// Wait 30ms (within TTL)
		time.Sleep(30 * time.Millisecond)

		// Check again - this should refresh the deadline
		pass, _ := g.CheckPass(1)
		require.False(t, pass)

		// Wait another 30ms (60ms total, but only 30ms since last checkPass)
		time.Sleep(30 * time.Millisecond)

		// Purge should NOT remove the entry because deadline was refreshed
		g.Purge()
		require.Equal(t, 1, len(g.m))

		// Wait for full TTL from last update
		time.Sleep(50 * time.Millisecond)
		g.Purge()
		require.Equal(t, 0, len(g.m))
	})

	t.Run("addPulled updates deadline", func(t *testing.T) {
		// Tests that addPulled refreshes the purge deadline
		g := New[int](50*time.Millisecond, 0, 0)

		g.CheckPass(1)

		// Wait 30ms
		time.Sleep(30 * time.Millisecond)

		// addPulled refreshes deadline
		g.AddPulled(1)

		// Wait another 30ms
		time.Sleep(30 * time.Millisecond)

		// Entry should still exist (deadline was refreshed by addPulled)
		g.Purge()
		require.Equal(t, 1, len(g.m))
	})
}
