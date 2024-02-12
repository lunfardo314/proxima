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
	PRXI                 = DustPerProxi
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * PRXI

	DefaultInitialBranchInflationBonus          = 20 * PRXI
	DefaultAnnualBranchInflationPromille        = 40
	DefaultInitialChainInflationFractionPerTick = 500_000_000
	DefaultHalvingEpochs                        = 5
	DefaultChainInflationOpportunitySlots       = 12
	DefaultVBCost                               = 1
	DefaultTransactionPace                      = 10
	DefaultTransactionPaceSequencer             = 5
	DefaultMinimumAmountOnSequencer             = 1_000 * PRXI
)

func newLibrary() *Library {
	ret := &Library{
		Library:            easyfl.NewBase(),
		constraintByPrefix: make(map[string]*constraintRecord),
		constraintNames:    make(map[string]struct{}),
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
	lib.Extendf("constInitialSupply", "u64/%d", id.InitialSupply)
	lib.Extendf("constGenesisControllerPublicKey", "0x%s", hex.EncodeToString(id.GenesisControllerPublicKey))
	lib.Extendf("constBaselineTime", "u64/%d", id.BaselineTime.UnixNano())
	lib.Extendf("constTickDuration", "u64/%d", int64(id.TickDuration))
	lib.Extendf("constMaxTickValuePerSlot", "u64/%d", id.MaxTickValueInSlot)
	lib.Extendf("constGenesisSlot", "u64/%d", id.GenesisSlot)
	lib.Extendf("constInitialBranchBonus", "u64/%d", id.InitialBranchBonus)
	lib.Extendf("constBranchBonusYearlyGrowthPromille", "u64/%d", id.BranchBonusInflationPerEpochPromille)
	lib.Extendf("constHalvingEpochs", "u64/%d", id.ChainInflationHalvingEpochs)
	lib.Extendf("constChainInflationFractionBase", "u64/%d", id.ChainInflationPerTickFractionBase)
	lib.Extendf("constChainInflationOpportunitySlots", "u64/%d", id.ChainInflationOpportunitySlots)
	lib.Extendf("constMinimumAmountOnSequencer", "u64/%d", id.MinimumAmountOnSequencer)

	lib.Extendf("constSlotsPerLedgerEpoch", "u64/%d", id.SlotsPerLedgerEpoch)
	lib.Extendf("constTransactionPace", "u64/%d", id.TransactionPace)
	lib.Extendf("constTransactionPaceSequencer", "u64/%d", id.TransactionPaceSequencer)
	lib.Extendf("constVBCost16", "u16/%d", id.VBCost) // change to 64
	lib.Extendf("ticksPerSlot", "%d", id.TicksPerSlot())
	lib.Extendf("ticksPerSlot64", "u64/%d", id.TicksPerSlot())
	lib.Extendf("timeSlotSizeBytes", "%d", SlotByteLength)
	lib.Extendf("timestampByteSize", "%d", TimeByteLength)

	lib.EmbedLong("ticksBefore", 2, evalTicksBefore64)

	// base helpers
	lib.Extend("sizeIs", "equal(len8($0), $1)")
	lib.Extend("mustSize", "if(sizeIs($0,$1), $0, !!!wrong_data_size)")

	lib.Extend("mustValidTimeTick", "if(and(mustSize($0,1),lessThan($0,ticksPerSlot)),$0,!!!wrong_timeslot)")
	lib.Extend("mustValidTimeSlot", "mustSize($0, timeSlotSizeBytes)")
	lib.Extend("timeSlotPrefix", "slice($0, 0, sub8(timeSlotSizeBytes,1))") // first 4 bytes of any array. It is not time slot yet
	lib.Extend("timeSlotFromTimeSlotPrefix", "bitwiseAND($0, 0x3fffffff)")
	lib.Extend("timeTickFromTimestamp", "byte($0, timeSlotSizeBytes)")
	lib.Extend("timestamp", "concat(mustValidTimeSlot($0),mustValidTimeTick($1))")
}

func (lib *Library) initNoTxConstraints(id *IdentityData) *Library {
	lib.extendWithBaseConstants(id)
	lib.extendWithMainFunctions()
	lib.MustExtendMany(inflationSource)
	return lib
}

func newLibraryInit(id *IdentityData) *Library {
	return newLibrary().initNoTxConstraints(id)
}

// TODO branch bonus inflation

const inflationSource = `
// $0 -  slot of the chain input as u64
func epochFromGenesis :
	div64(
       sub64($0, constGenesisSlot),
       constSlotsPerLedgerEpoch
    )

// $0 -  epochFromGenesis
func halvingEpoch :
	if(
		lessThan($0, constHalvingEpochs),
        $0,
        constHalvingEpochs
	)

// $0 slot of the chain input as u64
// result - inflation fraction corresponding to that year (taking into account halving) 
func inflationFractionBySlot :
    mul64(
        constChainInflationFractionBase, 
        lshift64(u64/1, halvingEpoch(epochFromGenesis($0)))
    )

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
func insideInflationOpportunityWindow :
   lessOrEqualThan(
	   div64(
		  ticksBefore($0, $1),
		  ticksPerSlot64
	   ),
       constChainInflationOpportunitySlots
   )

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input (no branch bonus)
// result: (dt * amount)/inflationFraction
func chainInflationAmount : 
if(
    insideInflationOpportunityWindow($0, $1),
	div64(
	   mul64(
		  ticksBefore($0, $1), 
		  $2
	   ), 
	   inflationFractionBySlot( concat(u32/0, timeSlotFromTimeSlotPrefix(timeSlotPrefix($0))) )
   ),
   u64/0
)

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
func inflationAmount : 
if(
	isZero(timeTickFromTimestamp($1)),
    sum64(chainInflationAmount($0, $1, $2), constInitialBranchBonus),
    chainInflationAmount($0, $1, $2)
)
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
		Description:                          "Proxima prototype ledger. Ver 0.0.0",
		InitialSupply:                        DefaultInitialSupply,
		GenesisControllerPublicKey:           privateKey.Public().(ed25519.PublicKey),
		BaselineTime:                         time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		TickDuration:                         DefaultTickDuration,
		MaxTickValueInSlot:                   DefaultTicksPerSlot - 1,
		GenesisSlot:                          0,
		SlotsPerLedgerEpoch:                  uint32(DefaultSlotsPerLedgerEpoch),
		InitialBranchBonus:                   DefaultInitialBranchInflationBonus,
		BranchBonusInflationPerEpochPromille: DefaultAnnualBranchInflationPromille,
		VBCost:                               DefaultVBCost,
		TransactionPace:                      DefaultTransactionPace,
		TransactionPaceSequencer:             DefaultTransactionPaceSequencer,
		ChainInflationHalvingEpochs:          DefaultHalvingEpochs,
		ChainInflationPerTickFractionBase:    DefaultInitialChainInflationFractionPerTick,
		ChainInflationOpportunitySlots:       DefaultChainInflationOpportunitySlots,
		MinimumAmountOnSequencer:             DefaultMinimumAmountOnSequencer,
	}

	// creating origin 1 slot before now. More convenient for the tests
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
	id.SlotsPerLedgerEpoch = uint32((24 * 365 * time.Hour) / id.SlotDuration())
}

// Library constants

func (lib LibraryConst) GenesisSlot() Slot {
	bin, err := lib.EvalFromSource(nil, "constGenesisSlot")
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret <= math.MaxUint32, "ret <= math.MaxUint32")
	return Slot(ret)
}

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
