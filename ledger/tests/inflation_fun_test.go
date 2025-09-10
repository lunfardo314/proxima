package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
)

func TestInflationFunctions(t *testing.T) {
	const amount uint64 = 100_000_000

	for s := uint32(0); s < uint32(1_000_000); s += 10_000 {
		for period := uint32(1); period < 3000; period += 10 {
			inf := ledger.L().ChainInflationOriginal(amount, s, period)
			infDir := ledger.ChainInflation(amount, s, period)
			if inf != infDir {
				t.Errorf("chain inflation inf=%d infDir=%d", inf, infDir)
			}
		}
	}
}
