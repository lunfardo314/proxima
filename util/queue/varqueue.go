package queue

import (
	"sync"
	"time"

	"github.com/gammazero/deque"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/atomic"
)

// FIXME panics on closing

// VarBuffered implements variable size synchronized FIFO queue
type VarBuffered[T any] struct {
	d                 *deque.Deque[T]
	dequeMutex        sync.RWMutex
	inSignal          chan struct{}
	in                chan T
	out               chan T
	closing           atomic.Bool
	pushCount         int
	pullCount         int
	pullFromChanCount int
}

const defaultBufferSize = 0

func New[T any](bufsize ...int) *VarBuffered[T] {
	bs := defaultBufferSize
	if len(bufsize) > 0 {
		bs = bufsize[0]
	}
	ret := &VarBuffered[T]{
		d:        new(deque.Deque[T]),
		inSignal: make(chan struct{}, 1),
		in:       make(chan T, bs),
		out:      make(chan T, bs),
	}
	go ret.queueLoop()
	return ret
}

func (q *VarBuffered[T]) queueLoop() {
	defer func() {
		close(q.in)
		close(q.inSignal)
		close(q.out)
	}()

	for {
		if e, ok := q.pull(); ok {
			q.out <- e
			continue
		}
		// both in channel and deque are empty
		if q.closing.Load() {
			// leave the go routine
			return
		}
		// queue is empty, wait for signal on incoming data
		// loop will repeat non-stop if there's data, otherwise every 200 msec
		select {
		case <-q.inSignal:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (q *VarBuffered[T]) pull() (T, bool) {
	q.dequeMutex.Lock()
	defer q.dequeMutex.Unlock()

	var nilT T

	select {
	case e, ok := <-q.in:
		if ok {
			return e, true
		}
		return nilT, false
	default:
	}
	if q.d.Len() == 0 {
		return nilT, false
	}
	return q.d.PopFront(), true
}

// Push pushes element
func (q *VarBuffered[T]) Push(elem T, priority ...bool) bool {
	prio := false
	if len(priority) > 0 {
		prio = priority[0]
	}
	return q.push(elem, prio)
}

func (q *VarBuffered[T]) push(elem T, priority bool) bool {
	if q.closing.Load() {
		// ignored
		return false
	}
	q.dequeMutex.Lock()
	defer q.dequeMutex.Unlock()

	defer func() {
		select {
		case q.inSignal <- struct{}{}:
		default:
		}
	}()

	q.pushCount++
	if q.d.Len() == 0 {
		// empty deque, push directly to the in channel, non-blocking
		select {
		case q.in <- elem:
			return true
		default:
		}
	}
	// deque is not empty
	var e T
	if priority {
		q.d.PushFront(elem)
		e = elem
	} else {
		q.d.PushBack(elem)
		e = q.d.Front()
	}
	select {
	case q.in <- e:
		q.d.PopFront()
	default:
	}
	return true
}

func (q *VarBuffered[T]) PushAny(elem any) bool {
	return q.Push(elem.(T))
}

// Close closes VarBuffered deferred until all elements are pulled
func (q *VarBuffered[T]) Close() {
	q.closing.Store(true)
}

func (q *VarBuffered[T]) pullOne() (T, bool) {
	ret, ok := <-q.out
	return ret, ok
}

// Consume reads all elements of the queue until it is closed
func (q *VarBuffered[T]) Consume(consumerFunctions ...func(elem T)) {
	util.Assertf(len(consumerFunctions) > 0, "must be at least one consumer function")
	var e T
	var ok bool
	for {
		if e, ok = q.pullOne(); !ok {
			break
		}
		for _, fun := range consumerFunctions {
			fun(e)
		}
	}
}

// Len returns number of elements in the queue. Approximate +- 1 !
func (q *VarBuffered[T]) Len() int {
	q.dequeMutex.Lock()
	defer q.dequeMutex.Unlock()

	return q.len()
}

func (q *VarBuffered[T]) len() int {
	return q.d.Len() + len(q.in) + len(q.out)
}

func (q *VarBuffered[T]) Info() (int, int) {
	q.dequeMutex.Lock()
	defer q.dequeMutex.Unlock()

	return q.pushCount, q.len()
}
