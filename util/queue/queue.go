package queue

import (
	"sync"
	"sync/atomic"

	"github.com/gammazero/deque"
)

type (
	// Queue implements variable and adaptive size FIFO queue. Unlike channels, never jams
	Queue[T any] struct {
		d                       *deque.Deque[T] // variable size deque
		inCh                    chan _inElem[T]
		outCh                   chan T
		consume                 func(e T)
		onLenChange             func(int) // optional callback when queue length changes
		inMutex                 sync.RWMutex
		closing                 bool
		processRemainingOnClose bool // mainly for testing
		len                     atomic.Int32
		// done tracks the inputLoop and consumeLoop goroutines. Close waits on it so
		// that any in-flight consume call finishes before Close returns — otherwise a
		// consumer that calls into downstream (e.g. a DB) may still be running when
		// the caller assumes the module is stopped and proceeds to tear those deps down.
		done sync.WaitGroup
	}

	_inElem[T any] struct {
		elem     T
		priority bool
	}
)

func New[T any](consume func(e T)) *Queue[T] {
	ret := &Queue[T]{
		d:       new(deque.Deque[T]),
		inCh:    make(chan _inElem[T]),
		outCh:   make(chan T),
		consume: consume,
	}
	ret.done.Add(2)
	go func() {
		defer ret.done.Done()
		ret.inputLoop()
	}()
	go func() {
		defer ret.done.Done()
		ret.consumeLoop()
	}()
	return ret
}

// Close initiates shutdown and blocks until both the inputLoop and consumeLoop
// goroutines have exited. The caller is guaranteed that, after Close returns,
// no further consume invocations are in progress.
func (q *Queue[T]) Close(processRemaining bool) {
	q.inMutex.Lock()
	if !q.closing {
		q.closing = true
		q.processRemainingOnClose = processRemaining
		close(q.inCh)
	}
	q.inMutex.Unlock()

	q.done.Wait()
}

// Push places element into the queue optionally with priority
func (q *Queue[T]) Push(e T, priority ...bool) {
	q.inMutex.RLock()
	defer q.inMutex.RUnlock()

	if q.closing {
		// ignore when closing
		return
	}
	prio := false
	if len(priority) > 0 {
		prio = priority[0]
	}
	q.inCh <- _inElem[T]{
		elem:     e,
		priority: prio,
	}
}

func (q *Queue[T]) Len() int {
	return int(q.len.Load())
}

// OnLenChange sets a callback invoked whenever the queue length changes.
// Must be called before Start/New. Not thread-safe.
func (q *Queue[T]) OnLenChange(fn func(int)) {
	q.onLenChange = fn
}

// inputLoop multiplexes between the producer (inCh) and the consumer (outCh).
//
// When the deque is empty, the goroutine simply blocks on inCh — the consumer
// has nothing to take.
//
// When the deque has elements, the goroutine blocks on a single select with
// both directions: a producer push wakes us to enqueue; a consumer ready to
// receive wakes us to dispatch the front element. Either side makes progress;
// neither side can starve the other.
//
// The previous implementation used non-blocking selects with `default:` on
// both branches, which spun whenever the deque held data and the consumer was
// busy — burning CPU for no progress. The blocking-select form preserves the
// "never jams" property (deque is unbounded, growing as long as the producer
// outpaces the consumer) without the spin.
func (q *Queue[T]) inputLoop() {
	defer close(q.outCh)

	push := func(e _inElem[T]) {
		if e.priority {
			q.d.PushFront(e.elem)
		} else {
			q.d.PushBack(e.elem)
		}
	}

	updateLen := func() {
		newLen := int32(q.d.Len())
		if old := q.len.Swap(newLen); old != newLen && q.onLenChange != nil {
			q.onLenChange(int(newLen))
		}
	}

	for {
		if q.d.Len() == 0 {
			// nothing to dispatch — only the producer can wake us
			e, ok := <-q.inCh
			if !ok {
				// channel closed and deque empty — done
				return
			}
			push(e)
		} else {
			// either side can make progress; block until one fires
			select {
			case e, ok := <-q.inCh:
				if !ok {
					// channel closed; either drain the rest or bail
					if !q.processRemainingOnClose {
						return
					}
					for q.d.Len() > 0 {
						q.outCh <- q.d.Front()
						q.d.PopFront()
					}
					updateLen()
					return
				}
				push(e)
			case q.outCh <- q.d.Front():
				q.d.PopFront()
			}
		}
		updateLen()
	}
}

func (q *Queue[T]) consumeLoop() {
	for e := range q.outCh {
		q.consume(e)
	}
}
