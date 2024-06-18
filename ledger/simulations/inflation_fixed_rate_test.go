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

	t.Logf("annualInflationChainFixed: %d%%, %s PRXI", annualInflationChainFixedPerc, util.GoTh(annualInflationChainFixed/ledger.PRXI))
	t.Logf("annualInflationBranchFixed: %d%%, %s PRXI", annualInflationBranchFixedPerc, util.GoTh(annualInflationBranchFixed/ledger.PRXI))
	t.Logf("annualInflationBranchFixed / 100 seq: %s PRXI", util.GoTh((annualInflationBranchFixed/ledger.PRXI)/100))
	slotsPerYear := uint64(365 * ledger.SlotsPerDay())
	t.Logf("annual slots: %d", slotsPerYear)
	chainInflationPerSlot := float64(annualInflationChainFixed/slotsPerYear) / float64(ledger.PRXI)
	branchInflationPerSlot := float64(annualInflationBranchFixed/slotsPerYear) / float64(ledger.PRXI)
	t.Logf("chain inflation per slot: %0.4f PRXI = ", chainInflationPerSlot)
	t.Logf("branch inflation per slot: %0.4f PRXI = ", branchInflationPerSlot)

	t.Logf("math.MaxUint64<<2: %s", util.GoTh(math.MaxUint64>>2))
	t.Logf("initial supply: %s PRXI", util.GoTh(beginSupply/ledger.PRXI))

	prevSupply := 0
	supply := beginSupply
	for year := 0; float64(supply) < float64(math.MaxUint64>>2); year++ {
		prevSupply = supply
		supply += annualInflationChainFixed + annualInflationBranchFixed
		t.Logf("%3d : %s PRXI   %0.2f%%", year, util.GoTh(prevSupply/ledger.PRXI), float64((supply-prevSupply)*100)/float64(prevSupply))
	}
}
