package tmp

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/util"
)

const (
	DefaultTickDuration = 100 * time.Millisecond
	DefaultTicksPerSlot = 100

	DustPerProxi         = 1_000_000
	BaseTokenName        = "Proxi"
	BaseTokenNameTicker  = "PRXI"
	DustTokenName        = "dust"
	PRXI                 = DustPerProxi
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * PRXI

	DefaultMaxBranchInflationBonus              = 5 * PRXI
	DefaultInitialChainInflationFractionPerTick = 500_000_000
	DefaultChainInflationOpportunitySlots       = 12
	DefaultVBCost                               = 1
	DefaultTransactionPace                      = 10
	DefaultTransactionPaceSequencer             = 1
	DefaultMinimumAmountOnSequencer             = 1_000 * PRXI
	DefaultMaxNumberOfEndorsements              = 8
)

const (
	annualInflationChainFixedPerc  = 10
	annualInflationBranchFixedPerc = 5
	annualInflationChainFixed      = (DefaultInitialSupply * annualInflationChainFixedPerc) / 100
	annualInflationBranchFixed     = (DefaultInitialSupply * annualInflationBranchFixedPerc) / 100
)

func TestConst(t *testing.T) {
	t.Logf("initial supply: %s", util.GoTh(DefaultInitialSupply))
	t.Logf("annualInflationChain: %s, %d%%", util.GoTh(annualInflationChainFixed), annualInflationChainFixedPerc)
	t.Logf("annualInflationBranch: %s, %d%%", util.GoTh(annualInflationBranchFixed), annualInflationBranchFixedPerc)

	const slotsPerYear = 60 * 60 * 24 * 365 / 10
	t.Logf("slots per year: %d", slotsPerYear)

	t.Logf("inflationChain per slot: %d", annualInflationChainFixed/slotsPerYear)
	t.Logf("inflationBranch per slot: %d", annualInflationBranchFixed/slotsPerYear)

}

func TestFinalConst(t *testing.T) {
	const (
		inflationChainPerTick         = 400_000
		chainInflationFractionPerTick = DefaultInitialSupply / inflationChainPerTick
		inflationBranchPerSlot        = 12_000_000
		maxChainInflationPerTick      = DefaultInitialSupply / chainInflationFractionPerTick
		maxChainInflationPerSlot      = maxChainInflationPerTick * 100

		annualInflationChainReverse      = maxChainInflationPerSlot * 6 * 60 * 24 * 365
		annualInflationChainReversePerc  = float64((annualInflationChainReverse * 100) / DefaultInitialSupply)
		annualInflationBranchReverse     = inflationBranchPerSlot * 6 * 60 * 24 * 365
		annualInflationBranchReversePerc = float64(annualInflationBranchReverse*100) / DefaultInitialSupply
	)
	t.Logf("Final constants:")
	t.Logf("init supply: %s", util.GoTh(DefaultInitialSupply))
	t.Logf("chain inflation fraction per tick: %s", util.GoTh(chainInflationFractionPerTick))
	t.Logf("chain inflation fraction per slot: %s", util.GoTh(chainInflationFractionPerTick/100))
	t.Logf("max chain inflation per slot: %s", util.GoTh(maxChainInflationPerSlot))
	t.Logf("branch inflation per slot: %s", util.GoTh(inflationBranchPerSlot))
	t.Logf("annual chain inflation: %s", util.GoTh(annualInflationChainReverse))
	t.Logf("annual chain inflation : %0.2f", annualInflationChainReversePerc)
	t.Logf("annual branch inflation: %s", util.GoTh(annualInflationBranchReverse))
	t.Logf("annual branch inflation : %0.2f", annualInflationBranchReversePerc)

}
