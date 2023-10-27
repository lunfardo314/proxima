package util

import (
	"fmt"
	"testing"

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

func TestWeldSlices(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		s1 := ""
		s2 := ""

		b, ok := WeldSlices([]byte(s1), []byte(s2))
		require.True(t, ok)
		require.EqualValues(t, "", string(b))
	})
	t.Run("2", func(t *testing.T) {
		s1 := "a"
		s2 := ""

		b, ok := WeldSlices([]byte(s1), []byte(s2))
		require.True(t, ok)
		require.EqualValues(t, "a", string(b))
	})
	t.Run("3", func(t *testing.T) {
		s1 := ""
		s2 := "ab"

		b, ok := WeldSlices([]byte(s1), []byte(s2))
		require.True(t, ok)
		require.EqualValues(t, "ab", string(b))
	})
	t.Run("4", func(t *testing.T) {
		s1 := "a"
		s2 := "b"

		_, ok := WeldSlices([]byte(s1), []byte(s2))
		require.False(t, ok)
	})
	t.Run("5", func(t *testing.T) {
		s1 := "a"
		s2 := "ab"

		b, ok := WeldSlices([]byte(s1), []byte(s2))
		require.True(t, ok)
		require.EqualValues(t, "ab", string(b))
	})
	t.Run("6", func(t *testing.T) {
		s1 := "abc"
		s2 := "cde"

		b, ok := WeldSlices([]byte(s1), []byte(s2))
		require.True(t, ok)
		require.EqualValues(t, "abcde", string(b))
	})
	t.Run("6-1", func(t *testing.T) {
		s1 := "cde"
		s2 := "abc"

		_, ok := WeldSlices([]byte(s1), []byte(s2))
		require.False(t, ok)
	})
	t.Run("7", func(t *testing.T) {
		s1 := "abc"
		s2 := "def"

		_, ok := WeldSlices([]byte(s1), []byte(s2))
		require.False(t, ok)
	})
	t.Run("8", func(t *testing.T) {
		s1 := "def"
		s2 := "abc"

		_, ok := WeldSlices([]byte(s1), []byte(s2))
		require.False(t, ok)
	})
	t.Run("9", func(t *testing.T) {
		s1 := "abc123456789"
		s2 := "2345"

		b, ok := WeldSlices([]byte(s1), []byte(s2))
		require.True(t, ok)
		require.EqualValues(t, s1, string(b))
	})
	t.Run("9-1", func(t *testing.T) {
		s1 := "2345"
		s2 := "abc123456789"

		_, ok := WeldSlices([]byte(s1), []byte(s2))
		require.False(t, ok)
	})
}
