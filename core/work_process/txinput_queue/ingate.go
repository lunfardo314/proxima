package txinput_queue

import (
	"sync"
	"time"
)

// the purpose of inGate is to let the transaction in no more than once and also prevent
// gossiping iof pulled transactions

type (
	inGateEntry struct {
		purgeDeadline time.Time
		isWanted      bool
	}

	inGate[T comparable] struct {
		mutex          sync.Mutex
		m              map[T]inGateEntry
		ttlBlack       time.Duration
		cleanIfExceeds int
	}
)

func newInGate[T comparable](ttlBlack time.Duration, cleanIfExceeds int) *inGate[T] {
	return &inGate[T]{
		m:              make(map[T]inGateEntry),
		ttlBlack:       ttlBlack,
		cleanIfExceeds: cleanIfExceeds,
	}
}

func (g *inGate[T]) checkPass(key T) (pass, wanted bool) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	entry, found := g.m[key]
	g.m[key] = inGateEntry{purgeDeadline: time.Now().Add(g.ttlBlack)}

	return !found || entry.isWanted, entry.isWanted
}

func (g *inGate[T]) addWanted(key T) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.m[key] = inGateEntry{isWanted: true}
}

func (g *inGate[T]) purgeBlackList() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if len(g.m) <= g.cleanIfExceeds {
		return
	}

	toDelete := make([]T, 0)
	nowis := time.Now()

	for key, entry := range g.m {
		if !entry.isWanted && nowis.After(entry.purgeDeadline) {
			toDelete = append(toDelete, key)
		}
	}
	for _, key := range toDelete {
		delete(g.m, key)
	}
}
