package tests

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
		SlotsPerLedgerEpoch    		: %s
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
		util.GoTh(ledger.SlotsPerLedgerYear()),
		util.GoTh(int64(ledger.SlotsPerDay())),
		util.GoTh(ledger.TicksPerYear()),
		util.GoTh(ledger.SlotsPerHour()),
		util.GoTh(ledger.TicksPerHour()),
	)
}

func BranchInflationAnnual() int64 {
	return ledger.SlotsPerLedgerYear() * int64(ledger.L().ID.InitialBranchBonus)
}

func MaxAnnualInflationChain() int64 {
	return MaxSlotInflationChain() * ledger.SlotsPerLedgerYear()
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

func MaxSlotInflationChain() int64 {
	return (int64(ledger.L().ID.InitialSupply) * int64(ledger.TicksPerSlot())) / ChainInitialInflationFractionPerSlot()
}

func TestInflationCalculations2(t *testing.T) {
	const template2 = `
		InitialSupply						: %s (%s PRXI)
		DefaultInitialBranchInflationBonus	: %s (%s PRXI)
		BranchInflationAnnual				: %s (%s PRXI)
		BranchInflationAnnual %%			: %.2f%%
		InitialChainInflationFractionPerTick: %s (%d bits) 
		MinInflatableAmountPerTick			: %s (%s PRXI)
		MinInflatableAmountPerSlot			: %s (%s PRXI)
		MaxSlotInflationChain				: %s (%s PRXI)
		MaxAnnualInflationChain				: %s (%s PRXI)
		MaxAnnualInflationChain %%			: %.2f%%
		MaxAnnualInflationTotal %%			: %.2f%%
`

	t.Logf(template2,
		util.GoTh(ledger.L().ID.InitialSupply), util.GoTh(ledger.L().ID.InitialSupply/ledger.PRXI),
		util.GoTh(ledger.DefaultInitialBranchInflationBonus), util.GoTh(ledger.DefaultInitialBranchInflationBonus/ledger.DustPerProxi),
		util.GoTh(BranchInflationAnnual()), util.GoTh(BranchInflationAnnual()/ledger.DustPerProxi),
		percent(int(BranchInflationAnnual()), ledger.DefaultInitialSupply),
		util.GoTh(ledger.DefaultInitialChainInflationFractionPerTick), requireBits(ledger.DefaultInitialChainInflationFractionPerTick),
		util.GoTh(MinInflatableAmountPerTick()), util.GoTh(MinInflatableAmountPerTick()/ledger.PRXI),
		util.GoTh(MinInflatableAmountPerSlot()), util.GoTh(MinInflatableAmountPerSlot()/ledger.DustPerProxi),
		util.GoTh(MaxSlotInflationChain()), util.GoTh(MaxSlotInflationChain()/ledger.DustPerProxi),
		util.GoTh(MaxAnnualInflationChain()), util.GoTh(MaxAnnualInflationChain()/ledger.DustPerProxi),
		percent(int(MaxAnnualInflationChain()), ledger.DefaultInitialSupply),
		percent(int(MaxAnnualInflationChain()+BranchInflationAnnual()), ledger.DefaultInitialSupply),
	)
}

const simulateYears = 20

func chainFractionBySlot(s ledger.Slot) int {
	return int(ledger.L().ID.InflationFractionBySlot(s))
}

func TestInflationCalculations3(t *testing.T) {
	genesisSlot := ledger.GenesisSlot()
	for i := 0; i < simulateYears; i++ {
		t.Logf("    year %d : chain fraction %s", i, util.GoTh(int(genesisSlot+ledger.Slot(i))))
	}
}

type yearData struct {
	chainInflation  int
	branchInflation int
	slots           []int
}

const capitalParticipatingShare = 100

func TestInflationProjections(t *testing.T) {
	years := make([]yearData, simulateYears)
	var chainSlotInflation int
	branchI := ledger.DefaultInitialBranchInflationBonus
	genesisSlot := ledger.GenesisSlot()
	for y, year := range years {
		year.slots = make([]int, ledger.SlotsPerLedgerYear())
		slot := genesisSlot + ledger.Slot(y)
		for i := range year.slots {
			if i == 0 {
				if y == 0 {
					year.slots[0] = ledger.DefaultInitialSupply
					chainSlotInflation = 0
				} else {
					year.slots[0] = years[y-1].slots[ledger.SlotsPerLedgerYear()-1]
					chainSlotInflation = (years[y-1].slots[ledger.SlotsPerLedgerYear()-1] / chainFractionBySlot(slot)) * capitalParticipatingShare / 100
				}
			} else {
				chainSlotInflation = (year.slots[i-1] / chainFractionBySlot(slot)) * capitalParticipatingShare / 100
				year.slots[i] = year.slots[i-1] + chainSlotInflation + branchI
			}
			year.chainInflation += chainSlotInflation
			year.branchInflation += branchI
		}
		years[y] = year

		// no branch bonus inflation
		//branchI = branchI + branchI*ledger.DefaultAnnualBranchInflationPromille/1000 // 4% indexing
	}

	for y, year := range years {
		slot := genesisSlot + ledger.Slot(y)
		initSupply := year.slots[0]
		finalSupply := year.slots[ledger.SlotsPerLedgerYear()-1]
		annualInflation := finalSupply - initSupply
		fmt.Printf("year %d (%s), supply: %s (%d bits) -> %s, inflation: %s, %.2f%%  chain inflation: %.2f%%, branch inflation %.2f%%\n",
			y, util.GoTh(chainFractionBySlot(slot)),
			util.GoTh(initSupply), requireBits(uint64(initSupply)), util.GoTh(finalSupply),
			util.GoTh(annualInflation), percent(annualInflation, initSupply),
			percent(year.chainInflation, initSupply), percent(year.branchInflation, initSupply),
		)
	}
}
