package wait

import (
	"sync"
	"time"

	"go.uber.org/atomic"
)

type WaitingRoom[T any] struct {
	mutex     sync.Mutex
	sn        atomic.Uint64
	waiting   map[uint64]chan T
	closeOnce sync.Once
	closed    atomic.Bool
}

func New[T any]() *WaitingRoom[T] {
	ret := &WaitingRoom[T]{
		waiting: make(map[uint64]chan T),
	}
	return ret
}

func (w *WaitingRoom[T]) RunAtOrBefore(deadline time.Time, fun func(arg T)) uint64 {
	if w.closed.Load() {
		return 0
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()

	sn := w.sn.Inc()
	ch := make(chan T)
	w.waiting[sn] = ch

	go func() {
		nowis := time.Now()
		var timeout time.Duration
		if deadline.After(nowis) {
			timeout = deadline.Sub(nowis)
		}
		select {
		case arg := <-ch:
			// was released or cancelled, sn already removed from the list
			fun(arg)

		case <-time.After(timeout):
			// timeout. sn not removed from the list
			w.mutex.Lock()
			delete(w.waiting, sn)
			w.mutex.Unlock()

			var nilT T
			fun(nilT)
		}
	}()
	return sn
}

func (w *WaitingRoom[T]) Release(sn uint64, arg T) {
	if w.closed.Load() {
		return
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()

	ch, found := w.waiting[sn]
	if !found {
		return
	}
	delete(w.waiting, sn)
	ch <- arg
	close(ch)
}

func (w *WaitingRoom[T]) Stop() {
	w.closeOnce.Do(func() {
		w.mutex.Lock()
		defer w.mutex.Unlock()

		for _, ch := range w.waiting {
			close(ch)
		}
		w.waiting = make(map[uint64]chan T)
		w.closed.Store(true)
	})
}

func (w *WaitingRoom[T]) IsStopped() bool {
	return w.closed.Load()
}
