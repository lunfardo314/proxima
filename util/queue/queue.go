package queue

import (
	"sync"
	"sync/atomic"

	"github.com/gammazero/deque"
	"github.com/lunfardo314/proxima/util"
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

func (q *Queue[T]) inputLoop() {
	defer close(q.outCh)

	for {
		// read incoming
		if q.d.Len() == 0 {
			// buffer is empty. Waits for incoming element
			select {
			case e, ok := <-q.inCh:
				if !ok {
					// immediately close because buffer is empty
					return
				}
				if e.priority {
					q.d.PushFront(e.elem)
				} else {
					q.d.PushBack(e.elem)
				}
			}
		} else {
			// tries to read incoming element but does not block because buffer has data to be consumed
			select {
			case e, ok := <-q.inCh:
				if ok {
					if e.priority {
						q.d.PushFront(e.elem)
					} else {
						q.d.PushBack(e.elem)
					}
				} else {
					if !q.processRemainingOnClose {
						// close only no need to process remaining element in the buffer
						return
					}
				}
			default:
			}
		}

		util.Assertf(q.d.Len() > 0, "q.d.Len()>0")
		// consume output. Sends front element (FIFO) into the out channel.
		// If successful, removes element from queue, otherwise it is blocked,
		// just skips.
		// It happens only if buffer is not empty
		select {
		case q.outCh <- q.d.Front():
			// if send to channel succeeds, element is removed from the buffer
			q.d.PopFront()
		default:
		}
		newLen := int32(q.d.Len())
		if old := q.len.Swap(newLen); old != newLen && q.onLenChange != nil {
			q.onLenChange(int(newLen))
		}
	}
}

func (q *Queue[T]) consumeLoop() {
	for e := range q.outCh {
		q.consume(e)
	}
}
