package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

func TestInflationFunctions(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	const amount uint64 = 100_000_000

	for s := uint32(0); s < uint32(1_000_000); s += 10_000 {
		for period := uint32(1); period < 3000; period += 10 {
			inf := lib.ChainInflationMultiStep(amount, s, period)
			infDir := lib.ChainInflationMultiStep(amount, s, period)
			if inf != infDir {
				t.Errorf("chain inflation inf=%d infDir=%d", inf, infDir)
			}
		}
	}
}
