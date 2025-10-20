package txinput_queue

import (
	"sync"
	"time"

	"golang.org/x/exp/maps"
)

// the purpose of inGate is to let the transaction in no more than once and also prevent
// gossiping of pulled transactions

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
		if nowis.After(entry.purgeDeadline) {
			delete(g.m, key)
		}
	}
}

func (g *inGate[T]) recreateMap() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.m = maps.Clone(g.m)
}
