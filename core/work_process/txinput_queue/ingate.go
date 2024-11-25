package txinput_queue

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/util/set"
)

type inGate[T comparable] struct {
	mutex sync.Mutex
	// list of transactions which are waited by attachers
	whiteList set.Set[T]
	// list of transaction which should be ignored (they are already known)
	blackList map[T]time.Time // deadline to be purged
	// TTL of the blacklist
	ttlBlack       time.Duration
	cleanIfExceeds int
}

func newInGate[T comparable](ttlBlack time.Duration, cleanIfExceeds int) *inGate[T] {
	return &inGate[T]{
		whiteList:      set.New[T](),
		blackList:      make(map[T]time.Time),
		ttlBlack:       ttlBlack,
		cleanIfExceeds: cleanIfExceeds,
	}
}

func (g *inGate[T]) checkPass(key T) (pass, wanted bool) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	nowis := time.Now()
	if _, inWhite := g.whiteList[key]; inWhite {
		// transaction is waited. Remove from WL and move to BL
		g.whiteList.Remove(key)
		g.blackList[key] = nowis.Add(g.ttlBlack)
		return true, true
	}
	// not in WL
	_, inBlack := g.blackList[key]
	// update when arrived in BL
	g.blackList[key] = nowis.Add(g.ttlBlack)
	// pass if wasn't in the BL
	return !inBlack, false
}

func (g *inGate[T]) addWanted(key T) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// force to WL and remove from BL
	g.whiteList.Insert(key)
	delete(g.blackList, key)
}

func (g *inGate[T]) purgeBlackList() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if len(g.blackList) <= g.cleanIfExceeds {
		return
	}

	toDelete := make([]T, 0)
	nowis := time.Now()

	for key, deadline := range g.blackList {
		if nowis.After(deadline) {
			toDelete = append(toDelete, key)
		}
	}
	for _, key := range toDelete {
		delete(g.blackList, key)
	}
}
