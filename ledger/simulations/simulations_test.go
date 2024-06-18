package simulations

import (
	"fmt"
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func requireBits(n uint64) (ret int) {
	for ; n != 0; ret++ {
		n >>= 1
	}
	return
}

func percent(n, d int) float32 {
	return (float32(n) * 100) / float32(d)
}

const (
	PricePRXI = 1.0
)

func TestInflationCalculations1(t *testing.T) {
	t.Logf("MaxUint64: %s, require bits: %d", util.GoTh(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default initial supply: %s (%s PRXI), require bits: %d",
		util.GoTh(ledger.L().ID.InitialSupply), util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI), requireBits(ledger.L().ID.InitialSupply))
	const (
		SatoshiInBTC = 100_000_000
		MaxBTCApprox = 21_000_000
	)
	t.Logf("Max Bitcoin supply. BTC: %s", util.GoTh(MaxBTCApprox))
	t.Logf("Max Bitcoin supply. Satoshi: %s, , require bits: %d", util.GoTh(MaxBTCApprox*SatoshiInBTC), requireBits(MaxBTCApprox*SatoshiInBTC))

	const template1 = `
		DustPerProxi (dust/PRXI) 	: %s (%d bits)
		InitialSupply (PRXI)		: %s (%d bits)
		InitialSupply (dust)		: %s dust (%d bits)
		Price						: %.2f USD
		Mcap						: %s USD
		SlotDuration        		: %v
		TicksPerSlot				: %d
		TickDuration				: %v
		SlotsPerHalvingEpoch    		: %s
		SlotsPerDay 	       		: %s
		TicksPerYear				: %s
		SlotsPerHour        		: %s
		TicksPerHour				: %s
`
	t.Logf(template1,
		util.GoTh(ledger.DustPerProxi),
		requireBits(ledger.DustPerProxi),
		util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI),
		requireBits(ledger.L().ID.InitialSupply/ledger.PRXI),
		util.GoTh(ledger.L().ID.InitialSupply),
		requireBits(ledger.L().ID.InitialSupply),
		PricePRXI,
		util.GoTh(int(PricePRXI*ledger.L().ID.InitialSupply)),
		ledger.SlotDuration(),
		ledger.TicksPerSlot(),
		ledger.TickDuration(),
		util.GoTh(ledger.SlotsPerHalvingEpoch()),
		util.GoTh(int64(ledger.SlotsPerDay())),
		util.GoTh(ledger.TicksPerYear()),
		util.GoTh(ledger.SlotsPerHour()),
		util.GoTh(ledger.TicksPerHour()),
	)
}

func BranchInflationAnnual() int64 {
	return ledger.SlotsPerHalvingEpoch() * int64(ledger.L().ID.BranchBonusBase)
}

func MaxInflationChainEpoch() int64 {
	return InitialSlotInflationChain() * ledger.SlotsPerHalvingEpoch()
}

func ChainInitialInflationFractionPerSlot() int64 {
	return int64(ledger.L().ID.ChainInflationPerTickFractionBase) / int64(ledger.TicksPerSlot())
}

func MinInflatableAmountPerTick() int64 {
	return ChainInitialInflationFractionPerSlot()
}

func MinInflatableAmountPerSlot() int64 {
	return ChainInitialInflationFractionPerSlot() / int64(ledger.TicksPerSlot())
}

func InitialSlotInflationChain() int64 {
	return int64(ledger.L().ID.InitialSupply) / ChainInitialInflationFractionPerSlot()
}

func TestInflationCalculations2(t *testing.T) {
	const template2 = `
		InitialSupply						: %s (%s PRXI)
		DefaultMaxBranchInflationBonus	: %s (%s PRXI)
		BranchInflationAnnual				: %s (%s PRXI)
		BranchInflationAnnual %%				: %.2f%%
		InitialChainInflationFractionPerTick: %s
		MinInflatableAmountPerTick			: %s (%s PRXI)
		MinInflatableAmountPerSlot			: %s (%s PRXI)
		InitialSlotInflationChain				: %s (%s PRXI)
		MaxInflationChainEpoch				: %s (%s PRXI)
		MaxInflationChainEpoch %%			: %.2f%%
		MaxAnnualInflationTotal %%			: %.2f%%
`

	t.Logf(template2,
		util.GoTh(ledger.L().ID.InitialSupply), util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI),
		util.GoTh(ledger.L().ID.BranchBonusBase), util.GoTh(ledger.L().ID.BranchBonusBase/ledger.DustPerProxi),
		util.GoTh(BranchInflationAnnual()), util.GoTh(BranchInflationAnnual()/ledger.DustPerProxi),
		percent(int(BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)),
		util.GoTh(ledger.L().ID.ChainInflationPerTickFractionBase),
		util.GoTh(MinInflatableAmountPerTick()), util.GoTh(MinInflatableAmountPerTick()/ledger.PRXI),
		util.GoTh(MinInflatableAmountPerSlot()), util.GoTh(MinInflatableAmountPerSlot()/ledger.DustPerProxi),
		util.GoTh(InitialSlotInflationChain()), util.GoTh(InitialSlotInflationChain()/ledger.DustPerProxi),
		util.GoTh(MaxInflationChainEpoch()), util.GoTh(MaxInflationChainEpoch()/ledger.DustPerProxi),
		percent(int(MaxInflationChainEpoch()), int(ledger.L().ID.InitialSupply)),
		percent(int(MaxInflationChainEpoch()+BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)),
	)
}

const simulateYears = 20 // > 78 int64 overflows

func chainFractionBySlot(s ledger.Slot) int {
	return int(ledger.L().ID.InflationFractionBySlot(s))
}

func TestInflationCalculations3(t *testing.T) {
	for i := 0; i < simulateYears; i++ {
		slot := ledger.Slot(i * int(ledger.L().ID.SlotsPerHalvingEpoch))
		t.Logf("    year %d : chain fraction %s", i, util.GoTh(int(slot)))
	}
}

type yearData struct {
	chainInflation  int
	branchInflation int
	supplyInSlot    []int
}

func TestReverseFractionEstimation(t *testing.T) {
	const targetAnnualChainInflationPercentage = 60

	absChainInflationEpoch := int((ledger.L().ID.InitialSupply * targetAnnualChainInflationPercentage) / 100)
	absChainInflationPerSlot := absChainInflationEpoch / int(ledger.L().ID.SlotsPerHalvingEpoch)
	absChainInflationPerTick := absChainInflationPerSlot / int(ledger.TicksPerSlot())
	projectedFraction := int(ledger.L().ID.InitialSupply) / absChainInflationPerTick

	actualInflationPerTick := int(ledger.L().ID.InitialSupply) / int(ledger.L().ID.ChainInflationPerTickFractionBase)
	actualInflationPerEpoch := actualInflationPerTick * int(ledger.TicksPerSlot()) * int(ledger.SlotsPerHalvingEpoch())
	actualInflationPerEpochPerc := percent(actualInflationPerEpoch, int(ledger.L().ID.InitialSupply))

	template := `
		targetInflation  		: %d%%
		absChainInflationEpoch : %s
        absChainInflationPerSlot: %s
		absChainInflationPerTick: %s
  		projectedFraction       : %s
		currentFraction			: %s
		actualInflationPerTick	: %s
		actualInflationPerEpoch	: %s
		actualInflationPerEpoch %% : %.2f %%
`
	t.Logf(template,
		targetAnnualChainInflationPercentage,
		util.GoTh(absChainInflationEpoch),
		util.GoTh(absChainInflationPerSlot),
		util.GoTh(absChainInflationPerTick),
		util.GoTh(projectedFraction),
		util.GoTh(ledger.L().ID.ChainInflationPerTickFractionBase),
		util.GoTh(actualInflationPerTick),
		util.GoTh(actualInflationPerEpoch),
		actualInflationPerEpochPerc,
	)
}

func TestInflationProjections(t *testing.T) {
	t.Logf("initial branch bonus: %s", util.GoTh(ledger.L().ID.BranchBonusBase))
	t.Logf("initial branch inflation epoch: %s (%.2f%%)",
		util.GoTh(BranchInflationAnnual()), percent(int(BranchInflationAnnual()), int(ledger.L().ID.InitialSupply)))
	t.Logf("initial chain inflation epoch: %s (%.2f%%)",
		util.GoTh(MaxInflationChainEpoch()), percent(int(MaxInflationChainEpoch()), int(ledger.L().ID.InitialSupply)))

	years := make([]yearData, simulateYears)
	var chainSlotInflation int
	slotsPerEpoch := ledger.L().ID.SlotsPerHalvingEpoch
	for y, year := range years {
		year.supplyInSlot = make([]int, slotsPerEpoch)
		slot := ledger.Slot(y * int(slotsPerEpoch))
		for i := range year.supplyInSlot {
			if i == 0 {
				if y == 0 {
					year.supplyInSlot[0] = int(ledger.L().ID.InitialSupply)
					chainSlotInflation = 0
				} else {
					year.supplyInSlot[0] = years[y-1].supplyInSlot[slotsPerEpoch-1]
					chainSlotInflation = years[y-1].supplyInSlot[slotsPerEpoch-1] / chainFractionBySlot(slot) //  * capitalParticipatingShare / 100
				}
			} else {
				chainSlotInflation = (year.supplyInSlot[i-1] * ledger.L().ID.TicksPerSlot()) / chainFractionBySlot(slot) // * capitalParticipatingShare / 100
				year.supplyInSlot[i] = year.supplyInSlot[i-1] + chainSlotInflation + int(ledger.L().ID.BranchBonusBase)
			}
			year.chainInflation += chainSlotInflation
			year.branchInflation += int(ledger.L().ID.BranchBonusBase)
		}
		years[y] = year
	}

	t.Logf("ASSUMPTION: 100%% of ledger capital is inflated every year. More realistic assumtion would be 60%% - 70%%")
	for y, year := range years {
		slot := ledger.Slot(y * int(slotsPerEpoch))
		initSupply := year.supplyInSlot[0]
		finalSupply := year.supplyInSlot[slotsPerEpoch-1]
		inflationPerEpoch := finalSupply - initSupply
		fmt.Printf("year %d (%s), supply: %s (%d bits) -> %s, inflation: %s, %.2f%%  chain inflation: %.2f%%, branch inflation %.2f%%\n",
			y, util.GoTh(chainFractionBySlot(slot)),
			util.GoTh(initSupply), requireBits(uint64(initSupply)), util.GoTh(finalSupply),
			util.GoTh(inflationPerEpoch), percent(inflationPerEpoch, initSupply),
			percent(year.chainInflation, initSupply), percent(year.branchInflation, initSupply),
		)
	}
}
