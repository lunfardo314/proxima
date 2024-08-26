package queue_old

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/util/countdown"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBasic(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		q := New[string]()
		q.Push("one")
		q.Push("two")
		q.Close()

		back := make([]string, 0)
		q.Consume(func(elem string) {
			back = append(back, elem)
		})
		require.EqualValues(t, 2, len(back))
		require.EqualValues(t, "one", back[0])
		require.EqualValues(t, "two", back[1])
	})
	t.Run("1-buf", func(t *testing.T) {
		q := New[string](5)
		q.Push("one")
		q.Push("two")
		q.Close()

		back := make([]string, 0)
		q.Consume(func(elem string) {
			back = append(back, elem)
		})
		require.EqualValues(t, 2, len(back))
		require.EqualValues(t, "one", back[0])
		require.EqualValues(t, "two", back[1])
	})
	t.Run("3", func(t *testing.T) {
		q := New[int]()
		require.EqualValues(t, 0, q.Len())
		for i := 0; i < 10000; i++ {
			q.Push(i)
			//require.EqualValues(t, i+1, q.Len())  // not correct
		}
		for i := 0; i < 10000; i++ {
			ib, ok := q.pullOne()
			require.True(t, ok)
			require.EqualValues(t, i, ib)
		}
		require.EqualValues(t, 0, q.Len())
	})
	t.Run("3-buf", func(t *testing.T) {
		q := New[int](90)
		require.EqualValues(t, 0, q.Len())
		for i := 0; i < 10000; i++ {
			q.Push(i)
			//require.EqualValues(t, i+1, q.Len())
		}
		for i := 0; i < 10000; i++ {
			ib, ok := q.pullOne()
			require.True(t, ok)
			require.EqualValues(t, i, ib)
		}
		require.EqualValues(t, 0, q.Len())
	})
	t.Run("3-buf-consume", func(t *testing.T) {
		q := New[int](5)
		require.EqualValues(t, 0, q.Len())
		for i := 0; i < 10000; i++ {
			q.Push(i)
			//require.EqualValues(t, i+1, q.Len())
		}
		q.Close()
		count := 0
		q.Consume(func(elem int) {
			require.EqualValues(t, count, elem)
			count++
		})
		require.EqualValues(t, 0, q.Len())
	})
	t.Run("3-buf-delay", func(t *testing.T) {
		q := New[int](10)
		require.EqualValues(t, 0, q.Len())
		for i := 0; i < 1000; i++ {
			q.Push(i)
			//require.EqualValues(t, i+1, q.Len())
		}
		q.Close()
		count := 0
		tmp := 0
		q.Consume(func(elem int) {
			require.EqualValues(t, count, elem)
			count++
			for i := 0; i < 1_000_000; i++ {
				tmp += 314
			}
		})
		require.EqualValues(t, 0, q.Len())
	})
}

func TestStartStop(t *testing.T) {
	const (
		howManyQueues   = 100
		howManyMessages = 10_000
	)
	que := make([]*Queue[int], howManyQueues)

	cdTotal := countdown.New(howManyMessages*howManyQueues, 3*time.Second)
	cdClosed := countdown.New(howManyQueues, 3*time.Second)

	for i := range que {
		que[i] = NewQueue[int](fmt.Sprintf("%d", i), zap.InfoLevel, nil)
		tmp := i
		que[i].AddOnConsume(func(e int) {
			require.EqualValues(t, e, tmp)
		}).AddOnClosed(func() {
			cdClosed.Tick()
		})
		for j := 0; j < howManyMessages; j++ {
			que[i].Push(i)
			cdTotal.Tick()
		}
		go que[i].Run()
		que[i].Stop()
	}
	err := countdown.Wait(cdTotal, cdClosed)

	require.NoError(t, err)
	t.Logf("num goroutine: %d", runtime.NumGoroutine())
	//for i := 0; i < 100; i++ {
	//	t.Debugf("num goroutine: %d", runtime.NumGoroutine())
	//	time.Sleep(1 * time.Second)
	//}

}

func TestMultiThread0(t *testing.T) {
	q := New[int]()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		q.Consume(func(int) {})
		wg.Done()
	}()
	q.Close()
	wg.Wait()
}

func TestMultiThread1(t *testing.T) {
	q := New[int]()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for i := 0; i < 1000; i++ {
			q.Push(i)
		}
		q.Close()
	}()
	count := 0
	go func() {
		for i := 0; i < 10_000; i++ {
			ib, ok := q.pullOne()
			if !ok {
				t.Logf("stop at count %d", count)
				break
			}
			require.True(t, ok)
			require.EqualValues(t, i, ib)
			count++
		}
		wg.Done()
	}()
	wg.Wait()
	require.EqualValues(t, 1000, count)
	require.EqualValues(t, 0, q.Len())
}

func TestMultiThread2(t *testing.T) {
	q := New[int]()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for i := 0; i < 1000; i++ {
			q.Push(i)
		}
		q.Close()
	}()
	count := 0
	go func() {
		q.Consume(func(e int) {
			require.EqualValues(t, e, count)
			count++
		})
		wg.Done()
	}()
	wg.Wait()
	require.EqualValues(t, 1000, count)
	require.EqualValues(t, 0, q.Len())
}

func BenchmarkMultiThread2(b *testing.B) {
	q := New[int](200)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for i := 0; i < b.N; i++ {
			q.Push(i)
		}
		q.Close()
	}()
	count := 0
	go func() {
		q.Consume(func(e int) {
			count++
		})
		wg.Done()
	}()
	wg.Wait()
	require.EqualValues(b, b.N, count)
	require.EqualValues(b, 0, q.Len())
}

func TestMultiThread3(t *testing.T) {
	q := New[int]()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for i := 0; i < 100; i++ {
			q.PushAny(i)
		}
		q.Close()
	}()
	count := 0
	go func() {
		q.Consume(func(e int) {
			//t.Debugf("%d", e)
			count++
			//time.Sleep(1 * time.Millisecond)
		})
		wg.Done()
	}()
	wg.Wait()
	require.EqualValues(t, 100, count)
	require.EqualValues(t, 0, q.Len())
}

func TestMany(t *testing.T) {
	const howManyConsumers = 100
	const howManyMessages = 10_000
	cons := make([]*Queue[time.Time], howManyConsumers)
	w := countdown.New(howManyMessages)
	for i := range cons {
		cons[i] = NewQueueWithBufferSize[time.Time](fmt.Sprintf("%d", i), 100, zap.InfoLevel, nil)
		if i+1 < howManyConsumers {
			next := i + 1
			cons[i].AddOnConsume(func(ts time.Time) {
				cons[next].Push(ts)
			})
			cons[i].AddOnClosed(func() {
				cons[next].Stop()
			})
		} else {
			cons[i].AddOnConsume(func(ts time.Time) {
				w.Tick()
			})
		}
	}
	for i := range cons {
		go cons[i].Run()
	}
	toggle := false
	startTime := time.Now()
	for i := 0; i < howManyMessages; i++ {
		toggle = !toggle
		cons[0].Push(time.Now(), toggle)
	}
	err := w.Wait()
	require.NoError(t, err)
	endTime := time.Now()

	cons[0].Stop()
	t.Logf("\nchain of %d consumers\n%d messages\navg = %f msec/msg",
		howManyConsumers, howManyMessages,
		(float32(endTime.Sub(startTime))/howManyMessages)/float32(time.Millisecond))
}

func TestPriority(t *testing.T) {
	const (
		howManyNormalMessages   = 100_000
		howManyPriorityMessages = 100_000
		trace                   = false
	)
	countNormal := countdown.New(howManyNormalMessages)
	countPriority := countdown.New(howManyPriorityMessages)
	countRepeated := countdown.New(howManyNormalMessages)
	consumer := NewQueue[string]("cons", zap.InfoLevel, nil)
	consumer.AddOnConsume(func(s string) {
		if trace {
			t.Logf("%s -> consume", s)
		}
		switch {
		case strings.HasPrefix(s, "prio"):
			countPriority.Tick()
		case strings.HasPrefix(s, "repeat"):
			countRepeated.Tick()
		default:
			consumer.Push(fmt.Sprintf("repeat with priority '%s')", s), true)
			countNormal.Tick()
		}
	})

	var wgStart sync.WaitGroup

	wgStart.Add(1)
	go func() {
		wgStart.Wait()
		for i := 0; i < howManyNormalMessages; i++ {
			msg := fmt.Sprintf("msg %d", i)
			if trace {
				t.Logf("%s -> push", msg)
			}
			consumer.Push(msg)
			if i%100 == 0 {
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()
	go func() {
		wgStart.Wait()
		for i := 0; i < howManyPriorityMessages; i++ {
			msg := fmt.Sprintf("prio msg %d", i)
			if trace {
				t.Logf("%s -> push", msg)
			}
			consumer.Push(msg, true)
			if i%100 == 0 {
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()
	go consumer.Run()
	wgStart.Done()

	err := countdown.Wait(countNormal, countPriority, countRepeated)
	require.NoError(t, err)
	consumer.Stop()
}

func BenchmarkRW(b *testing.B) {
	q := New[int]()
	for i := 0; i < b.N; i++ {
		q.Push(i)
	}
	for i := 0; i < b.N; i++ {
		_, _ = q.pullOne()
	}
}
