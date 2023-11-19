package seenset

import (
	"sync"
	"time"
)

// TODO background cleanup

type SeenSet[K comparable] struct {
	mutex sync.Mutex
	m     map[K]time.Time
	ttl   time.Duration
}

const defaultTTL = 10 * time.Minute

func New[K comparable](ttl ...time.Duration) *SeenSet[K] {
	ret := &SeenSet[K]{
		m:   make(map[K]time.Time),
		ttl: defaultTTL,
	}
	if len(ttl) > 0 {
		ret.ttl = ttl[0]
	}
	go ret.purgeLoop()

	return ret
}

func (s *SeenSet[K]) Seen(k K) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	_, ret := s.m[k]
	s.m[k] = time.Now().Add(s.ttl)
	return ret
}

func (s *SeenSet[K]) purgeLoop() {
	for {
		time.Sleep(5 * time.Second)
		s.purge()
	}
}

func (s *SeenSet[K]) purge() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	nowis := time.Now()
	toDelete := make([]K, 0)
	for k, ttl := range s.m {
		if ttl.Before(nowis) {
			toDelete = append(toDelete, k)
		}
	}
	for _, k := range toDelete {
		delete(s.m, k)
	}
}
