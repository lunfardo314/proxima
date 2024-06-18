package simulations

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func TestInflationFixedRate(t *testing.T) {
	const (
		beginSupply              = ledger.DefaultInitialSupply
		annualInflationFixedPerc = 15
		annualInflationFixed     = (beginSupply * annualInflationFixedPerc) / 100
	)
	t.Logf("annualInflationFixed: %d%%, %s", annualInflationFixedPerc, util.GoTh(annualInflationFixed))

	prevSupply := 0
	supply := beginSupply
	for year := 0; supply < math.MaxUint64>>1; year++ {
		prevSupply = supply
		supply += annualInflationFixed
		t.Logf("%3d : %s    %02f", year, util.GoTh(supply), float64((supply-prevSupply)*100)/float64(prevSupply))
	}
}
