package genesis

import (
	"math"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/util"
)

func requireBits(n uint64) (ret int) {
	for ; n != 0; ret++ {
		n >>= 1
	}
	return
}

func TestInflationCalculations(t *testing.T) {
	t.Logf("MaxUint64: %s, require bits: %d", util.GoTh(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default supply: %s, require bits: %d", util.GoTh(DefaultSupply), requireBits(DefaultSupply))
	const (
		SatoshiInBTC = 100_000_000
		MaxBTCApprox = 21_000_000
	)
	t.Logf("Max Bitcoin supply. BTC: %s", util.GoTh(MaxBTCApprox))
	t.Logf("Max Bitcoin supply. Satoshi: %s, , require bits: %d", util.GoTh(MaxBTCApprox*SatoshiInBTC), requireBits(MaxBTCApprox*SatoshiInBTC))

	// Assumptions
	const (
		DustPerProxi       = 1_000_000
		InitialSupplyProxi = 1_000_000_000
		InitialSupply      = InitialSupplyProxi * DustPerProxi
		SlotDuration       = 10 * time.Second
		YearDuration       = 24 * 265 * time.Hour
		SlotsPerYear       = int(YearDuration) / int(SlotDuration)
		template1          = `
		DustPerProxi (dust/PRXI) 	: %s 
		InitialSupplyProxi (PRXI)	: %s 
		InitialSupply (dust)		: %s dust
		SlotDuration        		: %v
		SlotsPerYear        		: %s
`
	)
	t.Logf(template1, util.GoTh(DustPerProxi), util.GoTh(InitialSupplyProxi),
		util.GoTh(InitialSupply), SlotDuration, util.GoTh(SlotsPerYear))

	const (
		SequencerInflationFraction  = 400_000_000
		BranchInflationFraction     = 4_000_000
		MaxSlotInflationBase        = InitialSupply/SequencerInflationFraction + InitialSupply/BranchInflationFraction
		ExtrapolatedAnnualInflation = MaxSlotInflationBase * SlotsPerYear
		template2                   = `
		SequencerInflationFraction			: %s 
		BranchInflationFraction				: %s
		MaxSlotInflationBase				: %s
		ExtrapolatedAnnualInflation			: %s (dust)
		ExtrapolatedAnnualInflation (%%)		: %d%%
`
	)
	ExtrapolatedAnnualInflationPerc := int(math.Floor((float64(ExtrapolatedAnnualInflation) * 100) / float64(InitialSupply)))
	t.Logf(template2, util.GoTh(SequencerInflationFraction), util.GoTh(BranchInflationFraction),
		util.GoTh(MaxSlotInflationBase), util.GoTh(ExtrapolatedAnnualInflation), ExtrapolatedAnnualInflationPerc)
}
