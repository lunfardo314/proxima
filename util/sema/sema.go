package sema

import (
	"fmt"
	"sync"
	"time"
)

type Sema struct {
	timeout time.Duration
	mutex   sync.RWMutex
	ch      chan struct{}
}

func New(timeout ...time.Duration) *Sema {
	ret := &Sema{}
	if len(timeout) > 0 {
		ret.timeout = timeout[0]
	}
	if ret.timeout != 0 {
		ret.ch = make(chan struct{}, 1)
	}
	return ret
}

func (s *Sema) Lock() {
	if s.timeout == 0 {
		s.mutex.Lock()
	} else {
		s._lock()
	}
}

func (s *Sema) _lock() {
	select {
	case s.ch <- struct{}{}:
	case <-time.After(s.timeout):
		panic(fmt.Sprintf("sema.Lock: can't get lock in %v", s.timeout))
	}
}

func (s *Sema) Unlock() {
	if s.timeout == 0 {
		s.mutex.Unlock()
	} else {
		s._unlock()
	}
}

func (s *Sema) _unlock() {
	select {
	case <-s.ch:
	default:
		panic("sema.Unlock: not locked")
	}
}

func (s *Sema) RLock() {
	if s.timeout == 0 {
		s.mutex.RLock()
	} else {
		s._lock()
	}
}

func (s *Sema) RUnlock() {
	if s.timeout == 0 {
		s.mutex.RUnlock()
	} else {
		s._unlock()
	}
}
