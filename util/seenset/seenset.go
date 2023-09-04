package seenset

import (
	"sync"
	"time"
)

// TODO background cleanup

type SeenSet[K comparable] struct {
	mutex sync.Mutex
	m     map[K]time.Time
}

func New[K comparable]() *SeenSet[K] {
	return &SeenSet[K]{
		m: make(map[K]time.Time),
	}
}

func (s *SeenSet[K]) Seen(k K, notouch ...bool) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	_, ret := s.m[k]
	if len(notouch) == 0 || !notouch[0] {
		s.m[k] = time.Now()
	}
	return ret
}

func (s *SeenSet[K]) SeenWhen(k K, notouch ...bool) (time.Time, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	rett, retb := s.m[k]
	if len(notouch) == 0 || !notouch[0] {
		s.m[k] = time.Now()
	}
	return rett, retb
}
