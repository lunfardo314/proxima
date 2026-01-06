package txinput_queue

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInputGate(t *testing.T) {
	t.Run("basic checkPass", func(t *testing.T) {
		// Tests that a new key passes through, and subsequent checks don't pass
		g := newInGate[int](10*time.Second, 10000)
		pass, wanted := g.checkPass(1)
		require.True(t, pass)
		require.False(t, wanted)

		pass, _ = g.checkPass(1)
		require.False(t, pass)

		g.addPulled(5)
		pass, wanted = g.checkPass(5)
		require.True(t, pass)
		require.True(t, wanted)

		pass, _ = g.checkPass(5)
		require.False(t, pass)

		g.addPulled(5)
		pass, wanted = g.checkPass(5)
		require.True(t, pass)
		require.True(t, wanted)
	})

	t.Run("multiple different keys", func(t *testing.T) {
		// Tests that different keys are tracked independently
		g := newInGate[int](10*time.Second, 10000)

		// All different keys should pass on first check
		for i := 0; i < 100; i++ {
			pass, wanted := g.checkPass(i)
			require.True(t, pass, "key %d should pass on first check", i)
			require.False(t, wanted, "key %d was not pulled", i)
		}

		// All keys should now be blocked
		for i := 0; i < 100; i++ {
			pass, _ := g.checkPass(i)
			require.False(t, pass, "key %d should be blocked on second check", i)
		}

		require.Equal(t, 100, len(g.m))
	})

	t.Run("addPulled before checkPass", func(t *testing.T) {
		// Tests that marking a key as pulled before checking allows it to pass with wanted=true
		g := newInGate[int](10*time.Second, 10000)

		g.addPulled(42)
		pass, wanted := g.checkPass(42)
		require.True(t, pass, "pulled key should pass")
		require.True(t, wanted, "pulled key should be wanted")

		// After checkPass, entry is updated with wasPulled=false, so next check should fail
		pass, wanted = g.checkPass(42)
		require.False(t, pass, "key should be blocked after first checkPass")
		require.False(t, wanted, "key should not be wanted after checkPass cleared it")
	})

	t.Run("addPulled can be called multiple times", func(t *testing.T) {
		// Tests that addPulled can reset a blocked key
		g := newInGate[int](10*time.Second, 10000)

		// First check passes
		pass, _ := g.checkPass(1)
		require.True(t, pass)

		// Second check fails
		pass, _ = g.checkPass(1)
		require.False(t, pass)

		// addPulled resets it
		g.addPulled(1)
		pass, wanted := g.checkPass(1)
		require.True(t, pass, "addPulled should allow key to pass again")
		require.True(t, wanted)

		// Can be done repeatedly
		g.addPulled(1)
		g.addPulled(1) // Multiple calls are fine
		pass, wanted = g.checkPass(1)
		require.True(t, pass)
		require.True(t, wanted)
	})

	t.Run("string keys", func(t *testing.T) {
		// Tests that generic type works with strings
		g := newInGate[string](10*time.Second, 10000)

		pass, wanted := g.checkPass("hello")
		require.True(t, pass)
		require.False(t, wanted)

		pass, _ = g.checkPass("hello")
		require.False(t, pass)

		g.addPulled("world")
		pass, wanted = g.checkPass("world")
		require.True(t, pass)
		require.True(t, wanted)
	})
}

func TestInGatePurge(t *testing.T) {
	t.Run("purge does nothing below threshold", func(t *testing.T) {
		// Tests that purgeInGate doesn't remove entries when under cleanWhenExceedsSize
		g := newInGate[int](1*time.Millisecond, 100)

		// Add 50 entries (below threshold of 100)
		for i := 0; i < 50; i++ {
			g.checkPass(i)
		}
		require.Equal(t, 50, len(g.m))

		// Wait for TTL to expire
		time.Sleep(5 * time.Millisecond)

		// Purge should not remove anything because we're below threshold
		g.purgeInGate()
		require.Equal(t, 50, len(g.m))
	})

	t.Run("purge removes expired entries above threshold", func(t *testing.T) {
		// Tests that purgeInGate removes expired entries when above cleanWhenExceedsSize
		g := newInGate[int](1*time.Millisecond, 10)

		// Add 20 entries (above threshold of 10)
		for i := 0; i < 20; i++ {
			g.checkPass(i)
		}
		require.Equal(t, 20, len(g.m))

		// Wait for TTL to expire
		time.Sleep(5 * time.Millisecond)

		// Purge should remove all expired entries
		g.purgeInGate()
		require.Equal(t, 0, len(g.m))
	})

	t.Run("purge keeps non-expired entries", func(t *testing.T) {
		// Tests that purgeInGate only removes expired entries, keeping fresh ones
		g := newInGate[int](100*time.Millisecond, 5)

		// Add 10 entries (above threshold of 5)
		for i := 0; i < 10; i++ {
			g.checkPass(i)
		}
		require.Equal(t, 10, len(g.m))

		// Purge immediately - nothing should be removed (TTL not expired)
		g.purgeInGate()
		require.Equal(t, 10, len(g.m))
	})

	t.Run("purge mixed expired and fresh", func(t *testing.T) {
		// Tests that purge removes only expired entries when mixed with fresh ones
		g := newInGate[int](5*time.Millisecond, 5)

		// Add 10 old entries
		for i := 0; i < 10; i++ {
			g.checkPass(i)
		}

		// Wait for them to expire
		time.Sleep(10 * time.Millisecond)

		// Add 5 fresh entries
		for i := 100; i < 105; i++ {
			g.checkPass(i)
		}
		require.Equal(t, 15, len(g.m))

		// Purge should remove the 10 old entries, keep the 5 fresh ones
		g.purgeInGate()
		require.Equal(t, 5, len(g.m))

		// Verify fresh entries are still there
		for i := 100; i < 105; i++ {
			pass, _ := g.checkPass(i)
			require.False(t, pass, "fresh entry %d should still be tracked", i)
		}
	})
}

func TestInGateRecreateMap(t *testing.T) {
	t.Run("recreateMap preserves entries", func(t *testing.T) {
		// Tests that recreateMap clones the map without losing data
		g := newInGate[int](10*time.Second, 10000)

		// Add some entries
		for i := 0; i < 10; i++ {
			g.checkPass(i)
		}
		g.addPulled(100)

		originalLen := len(g.m)

		// Recreate map
		g.recreateMap()

		// Same length
		require.Equal(t, originalLen, len(g.m))

		// Entries should still block
		for i := 0; i < 10; i++ {
			pass, _ := g.checkPass(i)
			require.False(t, pass, "entry %d should still be tracked after recreateMap", i)
		}

		// Pulled entry should still pass with wanted=true
		pass, wanted := g.checkPass(100)
		require.True(t, pass)
		require.True(t, wanted)
	})

	t.Run("recreateMap on empty gate", func(t *testing.T) {
		// Tests that recreateMap works on empty map
		g := newInGate[int](10*time.Second, 10000)

		g.recreateMap()
		require.Equal(t, 0, len(g.m))

		// Should still work normally after
		pass, _ := g.checkPass(1)
		require.True(t, pass)
	})
}

func TestInGateConcurrency(t *testing.T) {
	t.Run("concurrent checkPass", func(t *testing.T) {
		// Tests that concurrent access to checkPass is safe
		g := newInGate[int](10*time.Second, 100000)

		var wg sync.WaitGroup
		passCount := make([]int, 10)

		// 10 goroutines each trying to pass the same 100 keys
		for goroutine := 0; goroutine < 10; goroutine++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				for key := 0; key < 100; key++ {
					pass, _ := g.checkPass(key)
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
		g := newInGate[int](10*time.Second, 100000)

		var wg sync.WaitGroup

		// Goroutines adding pulled
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					g.addPulled(j)
				}
			}()
		}

		// Goroutines checking pass
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					g.checkPass(j)
				}
			}()
		}

		wg.Wait()
		// No race detector errors = success
	})

	t.Run("concurrent purge and checkPass", func(t *testing.T) {
		// Tests concurrent purgeInGate and checkPass don't cause data races
		g := newInGate[int](1*time.Millisecond, 10)

		var wg sync.WaitGroup

		// Goroutines adding entries
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					g.checkPass(offset*100 + j)
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
					g.purgeInGate()
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
		g := newInGate[int](0, 5)

		g.checkPass(1)
		g.checkPass(2)
		g.checkPass(3)

		// Even with zero TTL, entries are still tracked until purge
		pass, _ := g.checkPass(1)
		require.False(t, pass)

		// After purge (above threshold), entries should be removed
		g.checkPass(4)
		g.checkPass(5)
		g.checkPass(6) // Now we have 6 entries, above threshold of 5

		g.purgeInGate()

		// All entries should be purged (zero TTL means already expired)
		require.Equal(t, 0, len(g.m))
	})

	t.Run("threshold of zero", func(t *testing.T) {
		// Tests behavior with cleanWhenExceedsSize = 0 (always purge)
		g := newInGate[int](1*time.Millisecond, 0)

		g.checkPass(1)
		time.Sleep(5 * time.Millisecond)

		// With threshold 0, purge should always run
		g.purgeInGate()
		require.Equal(t, 0, len(g.m))
	})

	t.Run("checkPass updates deadline", func(t *testing.T) {
		// Tests that checkPass refreshes the purge deadline
		g := newInGate[int](50*time.Millisecond, 0)

		g.checkPass(1)

		// Wait 30ms (within TTL)
		time.Sleep(30 * time.Millisecond)

		// Check again - this should refresh the deadline
		pass, _ := g.checkPass(1)
		require.False(t, pass)

		// Wait another 30ms (60ms total, but only 30ms since last checkPass)
		time.Sleep(30 * time.Millisecond)

		// Purge should NOT remove the entry because deadline was refreshed
		g.purgeInGate()
		require.Equal(t, 1, len(g.m))

		// Wait for full TTL from last update
		time.Sleep(50 * time.Millisecond)
		g.purgeInGate()
		require.Equal(t, 0, len(g.m))
	})

	t.Run("addPulled updates deadline", func(t *testing.T) {
		// Tests that addPulled refreshes the purge deadline
		g := newInGate[int](50*time.Millisecond, 0)

		g.checkPass(1)

		// Wait 30ms
		time.Sleep(30 * time.Millisecond)

		// addPulled refreshes deadline
		g.addPulled(1)

		// Wait another 30ms
		time.Sleep(30 * time.Millisecond)

		// Entry should still exist (deadline was refreshed by addPulled)
		g.purgeInGate()
		require.Equal(t, 1, len(g.m))
	})
}
