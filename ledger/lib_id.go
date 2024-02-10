package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
)

type Library struct {
	*easyfl.Library
	ID                 *IdentityData
	constraintByPrefix map[string]*constraintRecord
	constraintNames    map[string]struct{}
}

const (
	DefaultTickDuration       = 100 * time.Millisecond
	DefaultTicksPerSlot       = 100
	DefaultSlotDuration       = DefaultTickDuration * DefaultTicksPerSlot
	DefaultSlotsPerLedgerYear = time.Hour * 24 * 365 / DefaultSlotDuration

	DustPerProxi         = 1_000_000
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * DustPerProxi

	DefaultInitialBranchInflationBonus          = 20_000_000
	DefaultAnnualBranchInflationPromille        = 40
	DefaultInitialChainInflationFractionPerTick = 400_000_000
	DefaultYearsHalving                         = 4
	DefaultVBCost                               = 1
	DefaultTransactionPace                      = 10
	DefaultTransactionPaceSequencer             = 5
)

func newLibrary() *Library {
	ret := &Library{
		Library:            easyfl.NewBase(),
		constraintByPrefix: make(map[string]*constraintRecord),
		constraintNames:    make(map[string]struct{}),
	}
	return ret
}

var librarySingleton *Library

func L() *Library {
	common.Assert(librarySingleton != nil, "ledger constraint library not initialized")
	return librarySingleton
}

func Init(id *IdentityData) {
	librarySingleton = newLibrary()
	librarySingleton.ID = id
	fmt.Printf("------ Base EasyFL library:\n")
	librarySingleton.PrintLibraryStats()
	defer func() {
		fmt.Printf("------ Extended EasyFL library:\n")
		librarySingleton.PrintLibraryStats()
	}()

	librarySingleton.extendWithBaseConstants(id)
	librarySingleton.extend()
}

func (lib *Library) extendWithBaseConstants(id *IdentityData) {
	// constants
	lib.Extendf("constInitialSupply", "u64/%d", id.InitialSupply)
	lib.Extendf("constGenesisControllerPublicKey", "0x%s", hex.EncodeToString(id.GenesisControllerPublicKey))
	lib.Extendf("constBaselineTime", "u64/%d", id.BaselineTime.UnixNano())
	lib.Extendf("constTickDuration", "u64/%d", int64(id.TickDuration))
	lib.Extendf("constMaxTickValuePerSlot", "%d", id.MaxTickValueInSlot)
	lib.Extendf("constGenesisSlot", "u64/%d", id.GenesisSlot)
	lib.Extendf("constInitialBranchBonus", "u64/%d", id.InitialBranchBonus)
	lib.Extendf("constBranchBonusYearlyGrowthPromille", "u64/%d", id.BranchBonusYearlyGrowthPromille)
	lib.Extendf("constYearsHalving", "u64/%d", id.ChainInflationHalvingYears)
	lib.Extendf("constChainInflationFractionBase", "u64/%d", id.ChainInflationPerTickFractionBase)

	lib.Extendf("constSlotsPerLedgerYear", "u64/%d", id.SlotsPerLedgerYear)
	lib.Extendf("constTransactionPace", "%d", id.TransactionPace)
	lib.Extendf("constTransactionPaceSequencer", "%d", id.TransactionPaceSequencer)
	lib.Extendf("constVBCost16", "u16/%d", id.VBCost)
	lib.Extendf("ticksPerSlot", "%d", id.TicksPerSlot())
	lib.Extendf("timeSlotSizeBytes", "%d", SlotByteLength)
	lib.Extendf("timestampByteSize", "%d", TimeByteLength)

	lib.EmbedLong("ticksBefore", 2, evalTicksBefore)

	// base helpers
	lib.Extend("sizeIs", "equal(len8($0), $1)")
	lib.Extend("mustSize", "if(sizeIs($0,$1), $0, !!!wrong_data_size)")

	lib.Extend("mustValidTimeTick", "if(and(mustSize($0,1),lessThan($0,ticksPerSlot)),$0,!!!wrong_timeslot)")
	lib.Extend("mustValidTimeSlot", "mustSize($0, timeSlotSizeBytes)")
	lib.Extend("timeSlotPrefix", "slice($0, 0, sub8(timeSlotSizeBytes,1))") // first 4 bytes of any array. It is not time slot yet
	lib.Extend("timeSlotFromTimeSlotPrefix", "bitwiseAND($0, 0x3fffffff)")
	lib.Extend("timeTickFromTimestamp", "byte($0, timeSlotSizeBytes)")
	lib.Extend("timestamp", "concat(mustValidTimeSlot($0),mustValidTimeTick($1))")

	lib.MustExtendMany(inflationFractionBySlotSource)
}

// TODO branch bonus inflation

const inflationFractionBySlotSource = `
// $0 - slot of the chain input as u64
func yearFromGenesis : div64(sub64($0, constGenesisSlot), constSlotsPerLedgerYear)

// $0 - year from genesis
func halvingYear :
	if(
		lessThan($0, constYearsHalving),
        $0,
        constYearsHalving
	)

// $0 slot of the chain input as u64
// result - inflation fraction corresponding to that year (taking into account halving) 
func inflationFractionBySlot : and(
	require(lessOrEqualThan(constGenesisSlot, $0), !!!wrong_slot_value),
    mul64(
        constChainInflationFractionBase, 
        lshift64(u64/1, halvingYear(yearFromGenesis($0)))
    )
)

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
// result: (dt * amount)/inflationFraction
func chainInflationAmount : 
div64(
   mul64(
	  ticksBefore($0, $1), 
	  $2
   ), 
   inflationFractionBySlot( concat(u32/0, timeSlotFromTimeSlotPrefix(timeSlotPrefix($0))) )
)

// TODO must be inflated each year
// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
// Result: maximum allowed inflation amount on the chain output
func inflationAmount : sum64(chainInflationAmount($0, $1, $2), constInitialBranchBonus)
`

func GetTestingIdentityData(seed ...int) (*IdentityData, ed25519.PrivateKey) {
	s := 10000
	if len(seed) > 0 {
		s = seed[0]
	}
	pk := testutil.GetTestingPrivateKey(1, s)
	return DefaultIdentityData(pk), pk
}

// for determinism in multiple tests
var startupLedgerTime *Time

func DefaultIdentityData(privateKey ed25519.PrivateKey, slot ...Slot) *IdentityData {
	ret := &IdentityData{
		Description:                       "Proxima prototype ledger. Ver 0.0.0",
		InitialSupply:                     DefaultInitialSupply,
		GenesisControllerPublicKey:        privateKey.Public().(ed25519.PublicKey),
		BaselineTime:                      time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		TickDuration:                      DefaultTickDuration,
		MaxTickValueInSlot:                DefaultTicksPerSlot - 1,
		GenesisSlot:                       0,
		SlotsPerLedgerYear:                uint32(DefaultSlotsPerLedgerYear),
		InitialBranchBonus:                DefaultInitialBranchInflationBonus,
		BranchBonusYearlyGrowthPromille:   DefaultAnnualBranchInflationPromille,
		VBCost:                            DefaultVBCost,
		TransactionPace:                   DefaultTransactionPace,
		TransactionPaceSequencer:          DefaultTransactionPaceSequencer,
		ChainInflationHalvingYears:        DefaultYearsHalving,
		ChainInflationPerTickFractionBase: DefaultInitialChainInflationFractionPerTick,
	}

	// creating origin 1 slot before now. More convenient for the workflow_old tests
	if len(slot) > 0 {
		ret.GenesisSlot = slot[0]
	} else {
		if startupLedgerTime == nil {
			t := ret.TimeFromRealTime(time.Now())
			startupLedgerTime = &t
		}
		ret.GenesisSlot = startupLedgerTime.Slot()
	}
	return ret
}

func (id *IdentityData) SetTickDuration(d time.Duration) {
	id.TickDuration = d
	id.GenesisSlot = Slot(time.Now().Sub(id.BaselineTime)/d) - 1
	id.SlotsPerLedgerYear = uint32((24 * 365 * time.Hour) / id.SlotDuration())
}

// InitWithTestingLedgerIDData for testing
func InitWithTestingLedgerIDData(seed ...int) ed25519.PrivateKey {
	id, pk := GetTestingIdentityData(seed...)
	Init(id)
	return pk
}
