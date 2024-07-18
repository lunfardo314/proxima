package util

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestForEachUniquePair(t *testing.T) {
	slString := []string{"0", "1", "2", "3"}
	t.Logf("--- strings")
	ForEachUniquePair(slString, func(a1, a2 string) bool {
		fmt.Printf("(%s, %s)\n", a1, a2)
		return true
	})

	slInt := []int{0, 1, 2, 3}
	t.Logf("--- ints")
	ForEachUniquePair(slInt, func(a1, a2 int) bool {
		fmt.Printf("(%d, %d)\n", a1, a2)
		return true
	})

	slInt = []int(nil)
	t.Logf("--- ints nil")
	ForEachUniquePair(slInt, func(a1, a2 int) bool {
		fmt.Printf("(%d, %d)\n", a1, a2)
		return true
	})

	slInt = []int{5}
	t.Logf("--- ints 1")
	ForEachUniquePair(slInt, func(a1, a2 int) bool {
		fmt.Printf("(%d, %d)\n", a1, a2)
		return true
	})

	slInt = []int{5, 6}
	t.Logf("--- ints 2")
	ForEachUniquePair(slInt, func(a1, a2 int) bool {
		fmt.Printf("(%d, %d)\n", a1, a2)
		return true
	})
}

func TestCallWithTimeout(t *testing.T) {
	CallWithTimeout(context.Background(), 2*time.Second,
		func() {
			time.Sleep(1 * time.Millisecond)
		},
		func() {
			t.FailNow()
		},
	)
	timeoutExceeded := new(atomic.Bool)
	require.False(t, timeoutExceeded.Load())

	CallWithTimeout(context.Background(), 100*time.Millisecond,
		func() {
			time.Sleep(200 * time.Millisecond)
		}, func() {
			timeoutExceeded.Store(true)
		},
	)
	time.Sleep(200 * time.Millisecond)
	require.True(t, timeoutExceeded.Load())
}
