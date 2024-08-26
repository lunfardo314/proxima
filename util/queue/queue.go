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
		inMutex                 sync.RWMutex
		closing                 bool
		processRemainingOnClose bool // mainly for testing
		len                     atomic.Int32
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
	go ret.inputLoop()
	go ret.consumeLoop()
	return ret
}

// Close queue must be closed in order to close channels and stop goroutines
func (q *Queue[T]) Close(processRemaining bool) {
	q.inMutex.Lock()
	defer q.inMutex.Unlock()

	if !q.closing {
		q.closing = true
		q.processRemainingOnClose = processRemaining
		close(q.inCh)
	}
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
		q.len.Store(int32(q.d.Len()))
	}
}

func (q *Queue[T]) consumeLoop() {
	for e := range q.outCh {
		q.consume(e)
	}
}
