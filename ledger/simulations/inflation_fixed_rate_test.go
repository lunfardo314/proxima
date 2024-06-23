package simulations

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func TestInflationFixedRate(t *testing.T) {
	const (
		beginSupply                    = ledger.DefaultInitialSupply
		annualInflationChainFixedPerc  = 10
		annualInflationBranchFixedPerc = 5
		annualInflationChainFixed      = (beginSupply * annualInflationChainFixedPerc) / 100
		annualInflationBranchFixed     = (beginSupply * annualInflationBranchFixedPerc) / 100
	)

	prevSupply := 0
	supply := beginSupply
	for year := 0; float64(supply) < float64(math.MaxUint64>>2); year++ {
		prevSupply = supply
		supply += annualInflationChainFixed + annualInflationBranchFixed
		t.Logf("%3d : %s PRXI   %0.2f%%", year, util.Th(prevSupply/ledger.PRXI), float64((supply-prevSupply)*100)/float64(prevSupply))
	}
}
