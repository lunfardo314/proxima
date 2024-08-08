package ledger

import (
	"crypto/ed25519"
	"encoding/binary"
	"math"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
)

type (
	Library struct {
		*easyfl.Library
		ID                 *IdentityData
		constraintByPrefix map[string]*constraintRecord
		constraintNames    map[string]struct{}
		inlineTests        []func()
	}

	LibraryConst struct {
		*Library
	}
)

// default ledger constants

const (
	DefaultTickDuration           = 100 * time.Millisecond
	DefaultTicksPerSlot           = 100
	DefaultSlotDuration           = DefaultTickDuration * DefaultTicksPerSlot
	DefaultInflationEpochDuration = 365 * 24 * time.Hour // standard inflation epoch is 365 days
	DefaultSlotsPerInflationEpoch = uint64(DefaultInflationEpochDuration / DefaultSlotDuration)

	DustPerProxi         = 1_000_000
	BaseTokenName        = "Proxi"
	BaseTokenNameTicker  = "PRXI"
	DustTokenName        = "dust"
	PRXI                 = DustPerProxi
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * PRXI

	// begin inflation-related

	DefaultMaxBranchInflationBonus        = 5_000_000
	DefaultChainInflationPerTickBase      = 317_098
	DefaultChainInflationOpportunitySlots = 12
	DefaultTicksPerInflationEpoch         = DefaultSlotsPerInflationEpoch * DefaultTicksPerSlot
	// end inflation-related

	DefaultVBCost                   = 1
	DefaultTransactionPace          = 10
	DefaultTransactionPaceSequencer = 1
	// DefaultMinimumAmountOnSequencer Reasonable limit could be 1/1000 of initial supply
	DefaultMinimumAmountOnSequencer    = 1_000 * PRXI
	DefaultMaxNumberOfEndorsements     = 8
	DefaultPreBranchConsolidationTicks = 20

	DaysPerYear                    = 365
	TargetAnnualChainInflationRate = 10
)

func init() {
	// enforce validity of defaults
	util.Assertf(DefaultInitialSupply*TargetAnnualChainInflationRate/100 == DefaultChainInflationPerTickBase*DefaultTicksPerInflationEpoch,
		"DefaultInitialSupply*TargetAnnualChainInflationRate/100 == DefaultChainInflationPerTickBase*DefaultTicksPerInflationEpoch")
}

func newBaseLibrary() *Library {
	ret := &Library{
		Library:            easyfl.NewBase(),
		constraintByPrefix: make(map[string]*constraintRecord),
		constraintNames:    make(map[string]struct{}),
		inlineTests:        make([]func(), 0),
	}
	return ret
}

func (lib *Library) Const() LibraryConst {
	return LibraryConst{lib}
}

func (lib *Library) TimeFromRealTime(t time.Time) Time {
	return lib.ID.TimeFromRealTime(t)
}

func GetTestingIdentityData(seed ...int) (*IdentityData, ed25519.PrivateKey) {
	s := 10000
	if len(seed) > 0 {
		s = seed[0]
	}
	pk := testutil.GetTestingPrivateKey(1, s)
	return DefaultIdentityData(pk), pk
}

func DefaultIdentityData(privateKey ed25519.PrivateKey) *IdentityData {
	genesisTimeUnix := uint32(time.Now().Unix())

	return &IdentityData{
		GenesisTimeUnix:                genesisTimeUnix,
		GenesisControllerPublicKey:     privateKey.Public().(ed25519.PublicKey),
		InitialSupply:                  DefaultInitialSupply,
		TickDuration:                   DefaultTickDuration,
		MaxTickValueInSlot:             DefaultTicksPerSlot - 1,
		VBCost:                         DefaultVBCost,
		TransactionPace:                DefaultTransactionPace,
		TransactionPaceSequencer:       DefaultTransactionPaceSequencer,
		BranchInflationBonusBase:       DefaultMaxBranchInflationBonus,
		ChainInflationPerTickBase:      DefaultChainInflationPerTickBase,
		ChainInflationOpportunitySlots: DefaultChainInflationOpportunitySlots,
		TicksPerInflationEpoch:         DefaultTicksPerInflationEpoch,
		MinimumAmountOnSequencer:       DefaultMinimumAmountOnSequencer,
		MaxNumberOfEndorsements:        DefaultMaxNumberOfEndorsements,
		PreBranchConsolidationTicks:    DefaultPreBranchConsolidationTicks,
		Description:                    "Proxima prototype ledger. Ver 0.1",
	}
}

func (id *IdentityData) SetTickDuration(d time.Duration) {
	id.TickDuration = d
}

// Library constants

func (lib LibraryConst) TicksPerSlot() byte {
	bin, err := lib.EvalFromSource(nil, "ticksPerSlot")
	util.AssertNoError(err)
	return bin[0]
}

func (lib LibraryConst) MinimumAmountOnSequencer() uint64 {
	bin, err := lib.EvalFromSource(nil, "constMinimumAmountOnSequencer")
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret < math.MaxUint32, "ret < math.MaxUint32")
	return ret
}

func (lib LibraryConst) TicksPerInflationEpoch() uint64 {
	bin, err := lib.EvalFromSource(nil, "ticksPerInflationEpoch")
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(bin)
}
