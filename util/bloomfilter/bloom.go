package bloomfilter

import (
	"context"
	"sync"
	"time"
)

type Filter[T comparable] struct {
	mutex sync.Mutex
	m     map[T]time.Time // key: last hit
	ttl   time.Duration
	buf   []T
}

const defaultPurgePeriod = 5 * time.Second

func New[T comparable](ctx context.Context, ttl time.Duration, purgeEach ...time.Duration) *Filter[T] {
	ret := &Filter[T]{
		m:   make(map[T]time.Time),
		ttl: ttl,
		buf: make([]T, 0),
	}
	period := defaultPurgePeriod
	if len(purgeEach) > 0 {
		period = purgeEach[0]
	}
	go ret.purgeLoop(ctx, period)
	return ret
}

func (b *Filter[T]) Add(key T) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.m[key] = time.Now()
}

func (b *Filter[T]) Len() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return len(b.m)
}

func (b *Filter[T]) CheckAndUpdate(key T) (hit bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, hit = b.m[key]
	b.m[key] = time.Now()
	return hit
}

func (b *Filter[T]) CheckAndDelete(key T) (hit bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if _, hit = b.m[key]; hit {
		delete(b.m, key)
	}
	return
}

func (b *Filter[T]) purgeLoop(ctx context.Context, period time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(period):
			b.purge()
		}
	}
}

func (b *Filter[T]) purge() {
	b.buf = b.buf[:0]
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for k, lastHit := range b.m {
		if time.Since(lastHit) > b.ttl {
			b.buf = append(b.buf, k)
		}
	}
	for _, k := range b.buf {
		delete(b.m, k)
	}
}
