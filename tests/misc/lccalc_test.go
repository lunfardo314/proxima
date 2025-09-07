package misc

import (
	"math/rand"
	"testing"

	"github.com/lunfardo314/proxima/util"
)

func genDeltas(n int) []uint64 {
	ret := make([]uint64, n)
	supply := uint64(1_000_000_000_000_000)
	ret[0] = supply
	for i := 1; i < n; i++ {
		supply += supply / (10 * 355 * 24 * 360)
		ret[i] = supply + uint64(rand.Intn(5_000_000)+1)
	}
	return ret
}

func lcBySum(deltas []uint64, i int) uint64 {
	ret := uint64(0)
	step := 0
	for j := i; j < len(deltas); j++ {
		ret += uint64(deltas[j] >> step)
		step++
		if step >= 64 {
			break
		}
	}
	return ret
}

func TestLC(t *testing.T) {
	deltas := genDeltas(100)
	lc1 := make([]uint64, len(deltas))

	for i := 0; i < len(deltas); i++ {
		lc1[i] = lcBySum(deltas, i)
	}

	for i := 0; i < len(deltas); i++ {
		lc2 := uint64(0)
		if i+1 < len(deltas) {
			lc2 = deltas[i] + (lc1[i+1] >> 1)
		}
		t.Logf("%2d: delta: %21s, lc1: %21s lc2: %21s     diff: %s",
			i, util.Th(deltas[i]), util.Th(lc1[i]), util.Th(lc2), util.Th(int64(lc1[i])-int64(lc2)))
	}
}
