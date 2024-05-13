package ledger

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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

const (
	DefaultTickDuration        = 100 * time.Millisecond
	DefaultTicksPerSlot        = 100
	DefaultSlotDuration        = DefaultTickDuration * DefaultTicksPerSlot
	DefaultSlotsPerLedgerEpoch = time.Hour * 24 * 365 / DefaultSlotDuration

	DustPerProxi         = 1_000_000
	BaseTokenName        = "Proxi"
	BaseTokenNameTicker  = "PRXI"
	DustTokenName        = "dust"
	PRXI                 = DustPerProxi
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * PRXI

	DefaultMaxBranchInflationBonus              = 5 * PRXI
	DefaultInitialChainInflationFractionPerTick = 500_000_000
	DefaultHalvingEpochs                        = 5
	DefaultChainInflationOpportunitySlots       = 12
	DefaultVBCost                               = 1
	DefaultTransactionPace                      = 10
	DefaultTransactionPaceSequencer             = 1
	DefaultMinimumAmountOnSequencer             = 1_000 * PRXI
	DefaultMaxNumberOfEndorsements              = 8
)

func newLibrary() *Library {
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

func (lib *Library) extendWithBaseConstants(id *IdentityData) {
	lib.ID = id
	// constants
	extendBaseConstants := []*easyfl.ExtendFunction{
		{"constInitialSupply", fmt.Sprintf("u64/%d", id.InitialSupply)},
		{"constGenesisControllerPublicKey", fmt.Sprintf("0x%s", hex.EncodeToString(id.GenesisControllerPublicKey))},
		{"constGenesisTimeUnix", fmt.Sprintf("u64/%d", id.GenesisTimeUnix)},
		{"constTickDuration", fmt.Sprintf("u64/%d", int64(id.TickDuration))},
		{"constMaxTickValuePerSlot", fmt.Sprintf("u64/%d", id.MaxTickValueInSlot)},
		{"constBranchBonusBase", fmt.Sprintf("u64/%d", id.BranchBonusBase)},
		{"constHalvingEpochs", fmt.Sprintf("u64/%d", id.NumHalvingEpochs)},
		{"constChainInflationFractionBase", fmt.Sprintf("u64/%d", id.ChainInflationPerTickFractionBase)},
		{"constChainInflationOpportunitySlots", fmt.Sprintf("u64/%d", id.ChainInflationOpportunitySlots)},
		{"constMinimumAmountOnSequencer", fmt.Sprintf("u64/%d", id.MinimumAmountOnSequencer)},
		{"constMaxNumberOfEndorsements", fmt.Sprintf("u64/%d", id.MaxNumberOfEndorsements)},

		{"constSlotsPerLedgerEpoch", fmt.Sprintf("u64/%d", id.SlotsPerHalvingEpoch)},
		{"constTransactionPace", fmt.Sprintf("u64/%d", id.TransactionPace)},
		{"constTransactionPaceSequencer", fmt.Sprintf("u64/%d", id.TransactionPaceSequencer)},
		{"constVBCost16", fmt.Sprintf("u16/%d", id.VBCost)}, // change to 64
		{"ticksPerSlot", fmt.Sprintf("%d", id.TicksPerSlot())},
		{"ticksPerSlot64", fmt.Sprintf("u64/%d", id.TicksPerSlot())},
		{"timeSlotSizeBytes", fmt.Sprintf("%d", SlotByteLength)},
		{"timestampByteSize", fmt.Sprintf("%d", TimeByteLength)},
	}
	lib.Extend(extendBaseConstants...)

	extendBaseHelpers := []*easyfl.ExtendFunction{
		{"mustSize", "if(equalUint(len($0), $1), $0, !!!wrong_data_size)"},
		{"mustValidTimeTick", "if(and(mustSize($0,1),lessThan($0,ticksPerSlot)),$0,!!!wrong_timeslot)"},
		{"mustValidTimeSlot", "mustSize($0, timeSlotSizeBytes)"},
		{"timeSlotPrefix", "slice($0, 0, 3)"}, // first 4 bytes of any array. It is not time slot yet
		{"timeSlotFromTimeSlotPrefix", "bitwiseAND($0, 0x3fffffff)"},
		{"timeTickFromTimestamp", "byte($0, 4)"},
		{"timestamp", "concat(mustValidTimeSlot($0),mustValidTimeTick($1))"},
		// takes first 5 bytes and sets first 2 bit to zero
		{"timestampPrefix", "bitwiseAND(slice($0, 0, 4), 0x3fffffffff)"},
	}
	lib.Extend(extendBaseHelpers...)
}

func (lib *Library) extend(id *IdentityData) *Library {
	lib.extendWithBaseConstants(id)
	lib.extendWithGeneralFunctions()
	lib.extendWithConstraints()
	return lib
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
		GenesisTimeUnix:                   genesisTimeUnix,
		GenesisControllerPublicKey:        privateKey.Public().(ed25519.PublicKey),
		InitialSupply:                     DefaultInitialSupply,
		TickDuration:                      DefaultTickDuration,
		MaxTickValueInSlot:                DefaultTicksPerSlot - 1,
		SlotsPerHalvingEpoch:              uint32(DefaultSlotsPerLedgerEpoch),
		BranchBonusBase:                   DefaultMaxBranchInflationBonus,
		VBCost:                            DefaultVBCost,
		TransactionPace:                   DefaultTransactionPace,
		TransactionPaceSequencer:          DefaultTransactionPaceSequencer,
		NumHalvingEpochs:                  DefaultHalvingEpochs,
		ChainInflationPerTickFractionBase: DefaultInitialChainInflationFractionPerTick,
		ChainInflationOpportunitySlots:    DefaultChainInflationOpportunitySlots,
		MinimumAmountOnSequencer:          DefaultMinimumAmountOnSequencer,
		MaxNumberOfEndorsements:           DefaultMaxNumberOfEndorsements,
		Description:                       "Proxima prototype ledger. Ver 0.0.0",
	}
}

func (id *IdentityData) SetTickDuration(d time.Duration) {
	id.TickDuration = d
	id.SlotsPerHalvingEpoch = uint32((24 * 365 * time.Hour) / id.SlotDuration())
}

// Library constants

func (lib LibraryConst) TicksPerSlot() byte {
	bin, err := lib.EvalFromSource(nil, "ticksPerSlot")
	util.AssertNoError(err)
	return bin[0]
}

func (lib LibraryConst) ChainInflationPerTickFractionBase() uint64 {
	bin, err := lib.EvalFromSource(nil, "constChainInflationFractionBase")
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(bin)
}

func (lib LibraryConst) HalvingEpochs() byte {
	bin, err := lib.EvalFromSource(nil, "constHalvingEpochs")
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret < 256, "ret<256")
	return byte(ret)
}

func (lib LibraryConst) HalvingEpoch(ts Time) byte {
	src := fmt.Sprintf("halvingEpoch(epochFromGenesis(u64/%d))", ts.Slot())
	bin, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret < 256, "ret<256")
	return byte(ret)
}

func (lib LibraryConst) SlotsPerEpoch() uint32 {
	bin, err := lib.EvalFromSource(nil, "constSlotsPerLedgerEpoch")
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret < math.MaxUint32, "ret < math.MaxUint32")
	return uint32(ret)
}

func (lib LibraryConst) MinimumAmountOnSequencer() uint64 {
	bin, err := lib.EvalFromSource(nil, "constMinimumAmountOnSequencer")
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret < math.MaxUint32, "ret < math.MaxUint32")
	return ret

}
