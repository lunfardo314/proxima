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
	ret := Quartiles(data)
	return ret[1]
}

// Quartiles returns quartiles 1, 2, 3
func Quartiles[T Number](data []T) (ret [3]T) {
	dataCopy := make([]T, len(data))
	copy(dataCopy, data)

	slices.Sort(dataCopy)

	l := len(dataCopy)
	if l == 0 {
		return
	}

	d := l / 4
	if l%4 == 0 {
		ret[0] = (dataCopy[d-1] + dataCopy[d]) / 2
	} else {
		ret[0] = dataCopy[d]
	}

	d = l / 2
	if l%2 == 0 {
		ret[1] = (dataCopy[d-1] + dataCopy[d]) / 2
	} else {
		ret[1] = dataCopy[d]
	}

	d = (l * 3) / 4
	if (l*3)%4 == 0 {
		ret[2] = (dataCopy[d-1] + dataCopy[d]) / 2
	} else {
		ret[2] = dataCopy[d]
	}
	return
}
