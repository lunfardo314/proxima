package util

import (
	"fmt"
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
	success := CallWithTimeout(func() {
		time.Sleep(1 * time.Millisecond)
	}, 2*time.Second)
	require.True(t, success)

	success = CallWithTimeout(func() {
		time.Sleep(100 * time.Millisecond)
	}, time.Millisecond)
	require.False(t, success)
}
