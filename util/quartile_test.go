package util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuartile(t *testing.T) {
	require.EqualValues(t, [3]int{0, 0, 0}, Quartiles[int](nil))
	require.EqualValues(t, [3]int{1, 1, 1}, Quartiles([]int{1}))
	require.EqualValues(t, [3]int{1, 1, 2}, Quartiles([]int{1, 2}))
	require.EqualValues(t, [3]int{1, 2, 3}, Quartiles([]int{1, 2, 3}))
	require.EqualValues(t, [3]int{1, 2, 3}, Quartiles([]int{1, 2, 3, 4}))
	require.EqualValues(t, [3]int{2, 3, 4}, Quartiles([]int{1, 2, 3, 4, 5}))
	require.EqualValues(t, [3]int{3, 5, 7}, Quartiles([]int{9, 8, 7, 6, 5, 4, 3, 2, 1}))

	require.EqualValues(t, 2, Median([]int{1, 2, 3, 4}))
	require.EqualValues(t, 3, Median([]int{1, 2, 3, 4, 5}))
}
