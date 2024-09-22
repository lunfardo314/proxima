package util

import (
	"time"

	"golang.org/x/exp/constraints"
	"golang.org/x/exp/slices"
)

type Number interface {
	constraints.Float | constraints.Integer | time.Duration
}

func Median[T Number](data []T) T {
	dataCopy := make([]T, len(data))
	copy(dataCopy, data)

	slices.Sort(dataCopy)

	l := len(dataCopy)
	switch {
	case l == 0:
		return 0
	case l%2 == 0:
		return (dataCopy[l/2-1] + dataCopy[l/2]) / 2
	default:
		return dataCopy[l/2]
	}
}
