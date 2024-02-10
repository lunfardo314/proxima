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
	DefaultYearsHalving                         = 5
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
	lib.Extend("constInitialSupply", fmt.Sprintf("u64/%d", id.InitialSupply))
	lib.Extend("constGenesisControllerPublicKey", fmt.Sprintf("0x%s", hex.EncodeToString(id.GenesisControllerPublicKey)))
	lib.Extend("constBaselineTime", fmt.Sprintf("u64/%d", id.BaselineTime.UnixNano()))
	lib.Extend("constVBCost16", fmt.Sprintf("u16/%d", id.VBCost))
	lib.Extend("ticksPerSlot", fmt.Sprintf("%d", id.TicksPerSlot()))

	// TODO enforce time pace
	//lib.Extend("timePace", fmt.Sprintf("%d", id.TransactionPace))
	//lib.Extend("timePace64", fmt.Sprintf("u64/%d", id.TransactionPace))
	//lib.Extend("timePaceSeq", fmt.Sprintf("%d", id.TransactionPaceSequencer))
	//lib.Extend("timePaceSeq64", fmt.Sprintf("u64/%d", id.TransactionPaceSequencer))

	lib.Extend("timeSlotSizeBytes", fmt.Sprintf("%d", SlotByteLength))
	lib.Extend("timestampByteSize", fmt.Sprintf("%d", TimeByteLength))
}

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
	fractionYoY := make([]uint64, DefaultYearsHalving)
	for i := range fractionYoY {
		fractionYoY[i] = DefaultInitialChainInflationFractionPerTick * (1 << i)
	}
	ret := &IdentityData{
		Description:                      "Proxima prototype ledger. Ver 0.0.0",
		InitialSupply:                    DefaultInitialSupply,
		GenesisControllerPublicKey:       privateKey.Public().(ed25519.PublicKey),
		BaselineTime:                     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		TickDuration:                     DefaultTickDuration,
		MaxTickValueInSlot:               DefaultTicksPerSlot - 1,
		GenesisSlot:                      0,
		SlotsPerLedgerYear:               uint32(DefaultSlotsPerLedgerYear),
		InitialBranchBonus:               DefaultInitialBranchInflationBonus,
		BranchBonusYearlyGrowthPromille:  DefaultAnnualBranchInflationPromille,
		VBCost:                           DefaultVBCost,
		TransactionPace:                  DefaultTransactionPace,
		TransactionPaceSequencer:         DefaultTransactionPaceSequencer,
		ChainInflationPerTickFractionYoY: fractionYoY,
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
