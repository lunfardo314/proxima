package txinput_queue

import (
	"sync"
	"time"
)

type inGate[T comparable] struct {
	mutex     sync.Mutex
	whiteList map[T]time.Time
	blackList map[T]time.Time
	ttlWhite  time.Duration
	ttlBlack  time.Duration
}

func newInGate[T comparable](ttlWhite, ttlBlack time.Duration) *inGate[T] {
	return &inGate[T]{
		whiteList: make(map[T]time.Time),
		blackList: make(map[T]time.Time),
		ttlWhite:  ttlWhite,
		ttlBlack:  ttlBlack,
	}
}

func (g *inGate[T]) checkPass(key T) (pass, wanted bool) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if _, inWhite := g.whiteList[key]; inWhite {
		delete(g.whiteList, key)
		g.blackList[key] = time.Now().Add(g.ttlBlack)
		return true, true
	}
	// not in whiteList
	if _, inBlack := g.blackList[key]; inBlack {
		g.blackList[key] = time.Now().Add(g.ttlBlack)
		return false, false
	}
	// not in black
	g.blackList[key] = time.Now().Add(g.ttlBlack)
	return true, false
}

func (g *inGate[T]) addWanted(key T) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// if already seen, no reason put to wanted
	if _, inBlack := g.blackList[key]; inBlack {
		return
	}

	g.whiteList[key] = time.Now().Add(g.ttlWhite)
}

func (g *inGate[T]) purge() int {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	ret := 0
	toDelete := make([]T, 0)
	nowis := time.Now()

	for key, deadline := range g.whiteList {
		if deadline.Before(nowis) {
			toDelete = append(toDelete, key)
		}
	}
	for _, key := range toDelete {
		delete(g.whiteList, key)
	}
	ret += len(toDelete)

	toDelete = toDelete[:0]
	for key, deadline := range g.blackList {
		if deadline.Before(nowis) {
			toDelete = append(toDelete, key)
		}
	}
	for _, key := range toDelete {
		delete(g.blackList, key)
	}
	ret += len(toDelete)
	return ret
}
