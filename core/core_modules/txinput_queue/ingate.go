package txinput_queue

import (
	"sync"
	"time"

	"golang.org/x/exp/maps"
)

// the purpose of inGate is to let the transaction in no more than once and also prevent
// gossiping of pulled transactions
//
// It is an exact map of transaction IDs, not a bloom filter, for two reasons. Each entry
// carries state — whether the transaction was pulled, which decides both that it passes a
// second time and that it is not gossiped — and a set membership approximation cannot hold
// it. And a bloom filter's false positives would silently drop transactions the node never
// saw, with no way to notice; unsolicited gossip is not retried, so the transaction would
// be lost until someone's past cone happens to pull it. The exact map is affordable because
// the TTL and the purge threshold bound it to a few tens of thousands of entries.

type (
	inGateEntry struct {
		purgeDeadline time.Time
		wasPulled     bool
	}

	inGate[T comparable] struct {
		mutex                sync.Mutex
		m                    map[T]inGateEntry
		ttl                  time.Duration
		cleanWhenExceedsSize int
	}
)

func newInGate[T comparable](ttl time.Duration, cleanWhenExceedsSize int) *inGate[T] {
	return &inGate[T]{
		m:                    make(map[T]inGateEntry),
		ttl:                  ttl,
		cleanWhenExceedsSize: cleanWhenExceedsSize,
	}
}

func (g *inGate[T]) updateEntry(key T, pulled bool) {
	g.m[key] = inGateEntry{
		wasPulled:     pulled,
		purgeDeadline: time.Now().Add(g.ttl),
	}
}

func (g *inGate[T]) checkPass(key T) (pass, wanted bool) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	entry, found := g.m[key]
	g.updateEntry(key, false)

	return !found || entry.wasPulled, entry.wasPulled
}

func (g *inGate[T]) addPulled(key T) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.updateEntry(key, true)
}

func (g *inGate[T]) purgeInGate() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if len(g.m) <= g.cleanWhenExceedsSize {
		return
	}

	nowis := time.Now()
	for key, entry := range g.m {
		if !entry.purgeDeadline.After(nowis) {
			delete(g.m, key)
		}
	}
}

func (g *inGate[T]) recreateMap() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.m = maps.Clone(g.m)
}
