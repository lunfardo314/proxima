package queue

import (
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/util/countdown"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestBasic(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		back := make([]string, 0)
		q := New[string](func(e string) {
			back = append(back, e)
		})
		q.Push("one")
		q.Close(false)

		require.EqualValues(t, 1, len(back))
		require.EqualValues(t, "one", back[0])

	})
	t.Run("2", func(t *testing.T) {
		back := make([]string, 0)
		q := New[string](func(e string) {
			back = append(back, e)
		})
		q.Push("one")
		q.Push("two")
		time.Sleep(10 * time.Millisecond)
		q.Close(false)

		require.EqualValues(t, 2, len(back))
		require.EqualValues(t, "one", back[0])
		require.EqualValues(t, "two", back[1])
	})
	t.Run("3", func(t *testing.T) {
		const n = 100_000
		var counter atomic.Int32
		q := New[int](func(e int) {
			counter.Inc()
		})
		require.EqualValues(t, 0, q.Len())
		for i := 0; i < n; i++ {
			q.Push(i)
		}
		q.Close(true)
		time.Sleep(300 * time.Millisecond)
		require.EqualValues(t, n, int(counter.Load()))
		require.EqualValues(t, 0, q.Len())
	})
}

func TestChainOfQueues(t *testing.T) {
	const (
		howManyQueues   = 100
		howManyMessages = 100_000
	)
	que := make([]*Queue[int], howManyQueues)
	ct := countdown.New(howManyMessages, 3*time.Second)

	que[0] = New[int](func(i int) {
		ct.Tick()
	})

	for i := range que {
		if i > 0 {
			que[i] = New[int](func(i int) {
				que[i-1].Push(i)
			})
		}
	}
	go func() {
		for i := 0; i < howManyMessages; i++ {
			que[howManyQueues-1].Push(1)
		}
	}()
	err := countdown.Wait(ct)
	require.NoError(t, err)
	for _, q := range que {
		q.Close(false)
	}
}

func TestMultiThread0(t *testing.T) {
	const (
		nRoutines = 10
		nMessages = 1000
	)
	ct := countdown.New(nRoutines*nMessages, 10*time.Second)
	q := New[int](func(i int) {
		ct.Tick()
	})
	var wg sync.WaitGroup

	for i := 0; i < nRoutines; i++ {
		wg.Add(1)
		go func() {
			for j := 0; j < nMessages; j++ {
				q.Push(1)
			}
			wg.Done()
		}()
	}
	err := ct.Wait()
	require.NoError(t, err)
}

func TestClose(t *testing.T) {
	const nMessages = 100_000

	var counter atomic.Int32
	q := New[int](func(i int) {
		counter.Inc()
	})

	for i := 0; i < nMessages; i++ {
		if i == nMessages/2 {
			q.Close(true)
		}
		q.Push(i)
	}
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, nMessages/2, int(counter.Load()))
}

func TestPriority(t *testing.T) {
	const nMessages = 100

	var counter atomic.Int32
	all := make(map[int]int)
	q := New[int](func(i int) {
		counter.Inc()
		all[i] = all[i] + 1
		if i%3 != 0 {
			time.Sleep(1 * time.Millisecond)
		}
	})

	for i := 0; i < nMessages; i++ {
		q.Push(i, i%3 == 0)
	}
	time.Sleep(1000 * time.Millisecond)
	require.EqualValues(t, nMessages, int(counter.Load()))

	require.EqualValues(t, nMessages, len(all))
	for _, v := range all {
		require.EqualValues(t, 1, v)
	}
}
