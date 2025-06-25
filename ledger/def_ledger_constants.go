package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type IdentityParameters struct {
	// arbitrary string up 255 bytes
	Description string
	// genesis time unix seconds
	GenesisTimeUnix uint32
	// initial supply of tokens
	InitialSupply uint64
	// ED25519 public key of the controller
	GenesisControllerPublicKey ed25519.PublicKey
	// time tick duration in nanoseconds
	TickDuration time.Duration
	// ----------- begin inflation-related
	SlotInflationBase    uint64 // constant C
	LinearInflationSlots uint64 // constant lambda
	// BranchInflationBonusBase inflation bonus
	BranchInflationBonusBase uint64
	// ----------- end inflation-related
	// VBCost
	VBCost uint64
	// number of ticks between non-sequencer transactions
	TransactionPace byte
	// number of ticks between sequencer transactions
	TransactionPaceSequencer byte
	// this limits number of sequencers in the network. Reasonable amount would be few hundreds of sequencers
	MinimumAmountOnSequencer uint64
	// limit maximum number of endorsements. For determinism
	MaxNumberOfEndorsements uint64
	// PreBranchConsolidationTicks enforces endorsement-only constraint for specified amount of ticks
	// before the slot boundary. It means, sequencer transaction can have only one input, its own predecessor
	// for any transaction with timestamp ticks > MaxTickValueInSlot - PreBranchConsolidationTicks
	// value 0 of PreBranchConsolidationTicks effectively means no constraint
	PreBranchConsolidationTicks  uint8
	PostBranchConsolidationTicks uint8
}

// default ledger constants

const (
	DefaultTickDuration = 80 * time.Millisecond

	DustPerProxi         = 1_000_000
	PRXI                 = DustPerProxi
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * PRXI

	// -------------- begin inflation-related
	// default inflation constants adjusted to the annual inflation cap of approx 12-13% first year

	DefaultSlotInflationBase        = 33_000_000
	DefaultLinearInflationSlots     = 3
	DefaultBranchInflationBonusBase = 5_000_000
	// used to enforce approx validity of defaults

	// -------------- end inflation-related

	DefaultVBCost                   = 1
	DefaultTransactionPace          = 12
	DefaultTransactionPaceSequencer = 2
	// DefaultMinimumAmountOnSequencer Reasonable limit could be 1/1000 of initial supply
	DefaultMinimumAmountOnSequencer     = 1_000 * PRXI // this is testnet default
	DefaultMaxNumberOfEndorsements      = 8
	DefaultPreBranchConsolidationTicks  = 25
	DefaultPostBranchConsolidationTicks = 12
	defaultDescription                  = "Proxima ledger definitions"
)

func init() {
	util.Assertf(DefaultInitialSupply/DefaultSlotInflationBase == 30_303_030, "wrong constants: DefaultInitialSupply/DefaultSlotInflationBase == 30_303_030")
}

func DefaultIdentityParameters(privateKey ed25519.PrivateKey, genesisTimeUnix uint32, description ...string) *IdentityParameters {
	//genesisTimeUnix := uint32(time.Now().Unix())

	dscr := defaultDescription
	if len(description) > 0 {
		dscr = description[0]
	}
	return &IdentityParameters{
		GenesisTimeUnix:              genesisTimeUnix,
		GenesisControllerPublicKey:   privateKey.Public().(ed25519.PublicKey),
		InitialSupply:                DefaultInitialSupply,
		TickDuration:                 DefaultTickDuration,
		VBCost:                       DefaultVBCost,
		TransactionPace:              DefaultTransactionPace,
		TransactionPaceSequencer:     DefaultTransactionPaceSequencer,
		BranchInflationBonusBase:     DefaultBranchInflationBonusBase,
		SlotInflationBase:            DefaultSlotInflationBase,
		LinearInflationSlots:         DefaultLinearInflationSlots,
		MinimumAmountOnSequencer:     DefaultMinimumAmountOnSequencer,
		MaxNumberOfEndorsements:      DefaultMaxNumberOfEndorsements,
		PreBranchConsolidationTicks:  DefaultPreBranchConsolidationTicks,
		PostBranchConsolidationTicks: DefaultPostBranchConsolidationTicks,
		Description:                  dscr,
	}
}

func ConstantsYAMLFromIdentity(id *IdentityParameters) []byte {
	return []byte(fmt.Sprintf(__definitionsLedgerConstantsYAML,
		id.InitialSupply,
		hex.EncodeToString(id.GenesisControllerPublicKey),
		id.GenesisTimeUnix,
		uint64(id.TickDuration),
		base.MaxTickValue,
		id.SlotInflationBase,
		id.LinearInflationSlots,
		id.BranchInflationBonusBase,
		id.MinimumAmountOnSequencer,
		id.MaxNumberOfEndorsements,
		id.PreBranchConsolidationTicks,
		id.PostBranchConsolidationTicks,
		id.TransactionPace,
		id.TransactionPaceSequencer,
		id.VBCost,
		hex.EncodeToString([]byte(id.Description)),
	))
}

func (id *IdentityParameters) GenesisTime() time.Time {
	return time.Unix(int64(id.GenesisTimeUnix), 0)
}

func (id *IdentityParameters) GenesisTimeUnixNano() int64 {
	return time.Unix(int64(id.GenesisTimeUnix), 0).UnixNano()
}

func (id *IdentityParameters) GenesisControlledAddress() AddressED25519 {
	return AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

// TimeToTicksSinceGenesis converts time value into ticks since genesis
func (id *IdentityParameters) TimeToTicksSinceGenesis(nowis time.Time) int64 {
	timeSinceGenesis := nowis.Sub(id.GenesisTime())
	return int64(timeSinceGenesis / id.TickDuration)
}

func (id *IdentityParameters) LedgerTimeFromClockTime(nowis time.Time) base.LedgerTime {
	ret, err := base.LedgerTimeFromTicksSinceGenesis(id.TimeToTicksSinceGenesis(nowis))
	util.AssertNoError(err)
	return ret
}

func (id *IdentityParameters) SlotDuration() time.Duration {
	return id.TickDuration * time.Duration(base.TicksPerSlot)
}

func (id *IdentityParameters) SlotsPerDay() int {
	return int(24 * time.Hour / id.SlotDuration())
}

func (id *IdentityParameters) SlotsPerYear() int {
	return 365 * id.SlotsPerDay()
}

func (id *IdentityParameters) TicksPerYear() int {
	return id.SlotsPerYear() * base.TicksPerSlot
}

func (id *IdentityParameters) OriginChainID() base.ChainID {
	oid := base.GenesisOutputID()
	return base.MakeOriginChainID(oid)
}

func (id *IdentityParameters) IsPreBranchConsolidationTimestamp(ts base.LedgerTime) bool {
	return uint8(ts.Tick) > base.MaxTickValue-id.PreBranchConsolidationTicks
}

func (id *IdentityParameters) IsPostBranchConsolidationTimestamp(ts base.LedgerTime) bool {
	return uint8(ts.Tick) >= id.PostBranchConsolidationTicks
}

func (id *IdentityParameters) EnsurePostBranchConsolidationConstraintTimestamp(ts base.LedgerTime) base.LedgerTime {
	if id.IsPostBranchConsolidationTimestamp(ts) {
		return ts
	}
	return base.NewLedgerTime(ts.Slot, base.Tick(id.PostBranchConsolidationTicks))
}

func (id *IdentityParameters) SetTickDuration(d time.Duration) {
	id.TickDuration = d
}

func (id *IdentityParameters) String() string {
	return id.Lines().String()
}

func (id *IdentityParameters) Lines(prefix ...string) *lines.Lines {
	originChainID := id.OriginChainID()
	return lines.New(prefix...).
		Add("Description: '%s'", id.Description).
		Add("Initial supply: %s", util.Th(id.InitialSupply)).
		Add("Genesis controller public key: %s", hex.EncodeToString(id.GenesisControllerPublicKey)).
		Add("Genesis controller address (calculated): %s", id.GenesisControlledAddress().String()).
		Add("Genesis Unix time: %d (%s)", id.GenesisTimeUnix, id.GenesisTime().Format(time.RFC3339)).
		Add("Tick duration: %v", id.TickDuration).
		Add("Slot inflation base (constant C): %s", util.Th(id.SlotInflationBase)).
		Add("Linear inflation slots (constant lambda): %s", util.Th(id.LinearInflationSlots)).
		Add("Constant initial supply/slot inflation base: %s", util.Th(id.InitialSupply/id.SlotInflationBase)).
		Add("Branch inflation bonus base: %s", util.Th(id.BranchInflationBonusBase)).
		Add("Pre-branch consolidation ticks: %v", id.PreBranchConsolidationTicks).
		Add("Post-branch consolidation ticks: %v", id.PostBranchConsolidationTicks).
		Add("Minimum amount on sequencer: %s", util.Th(id.MinimumAmountOnSequencer)).
		Add("Transaction pace: %d", id.TransactionPace).
		Add("Sequencer pace: %d", id.TransactionPaceSequencer).
		Add("VB cost: %d", id.VBCost).
		Add("Max number of endorsements: %d", id.MaxNumberOfEndorsements).
		Add("Origin chain id (calculated): %s", originChainID.String())
}

func (id *IdentityParameters) TimeConstantsToString() string {
	nowis := time.Now()
	timestampNowis := id.LedgerTimeFromClockTime(nowis)

	// TODO sometimes fails
	//util.Assertf(util.Abs(nowis.UnixNano()-timestampNowis.UnixNano()) < int64(TickDuration()),
	//	"nowis.UnixNano()(%d)-timestampNowis.UnixNano()(%d) = %d < int64(TickDuration())(%d)",
	//	nowis.UnixNano(), timestampNowis.UnixNano(), nowis.UnixNano()-timestampNowis.UnixNano(), int64(TickDuration()))

	maxYears := base.MaxSlot / (id.SlotsPerDay() * 365)
	return lines.New().
		Add("TickDuration = %v", id.TickDuration).
		Add("SlotDuration = %v", id.SlotDuration()).
		Add("SlotsPerDay = %d", id.SlotsPerDay()).
		Add("MaxYears = %d", maxYears).
		Add("seconds per year = %d", 60*60*24*365).
		Add("GenesisTime = %v", id.GenesisTime().Format(time.StampNano)).
		Add("nowis %v", nowis.Format(time.StampNano)).
		Add("nowis nano %d", nowis.UnixNano()).
		Add("GenesisTimeUnix = %d", id.GenesisTimeUnix).
		Add("GenesisTimeUnixNano = %d", id.GenesisTimeUnixNano()).
		Add("ticks since genesis: %d", id.TimeToTicksSinceGenesis(nowis)).
		Add("timestampNowis = %s ", timestampNowis.String()).
		Add("timestampNowis.ClockTime() = %v ", ClockTime(timestampNowis)).
		Add("timestampNowis.ClockTime().UnixNano() = %v ", ClockTime(timestampNowis).UnixNano()).
		Add("timestampNowis.UnixNano() = %v ", UnixNanoFromLedgerTime(timestampNowis)).
		Add("rounding: nowis.UnixNano() - timestampNowis.UnixNano() = %d", nowis.UnixNano()-UnixNanoFromLedgerTime(timestampNowis)).
		Add("tick duration nano = %d", int64(TickDuration())).
		String()
}

const __definitionsLedgerConstantsYAML = `
# definitions of main ledger constants
functions:
   -
      sym: constInitialSupply
      description: Initial number of tokens in the ledger
      source: u64/%d
   -
      sym: constGenesisControllerPublicKey
      description: Public key ED25519 of the genesis controller in hexadecimal format
      source: 0x%s
   -
      sym: constGenesisTimeUnix
      description: Unix time in seconds when ledger was initiated. Timestamp 0|0 corresponds to the genesis time 
      source: u64/%d
   -
      sym: constTickDuration
      description: tick duration in nanoseconds. Default is 80ms
      source: u64/%d
   -
      sym: constMaxTickValuePerSlot
      description: maximum value of ticks in the slot. Usually 127
      source: u64/%d
   -
      sym: ticksPerSlot64
      description: number of ticks in the slot. Usually 128
      source: add(constMaxTickValuePerSlot, u64/1)
# inflation related
   -
      sym: constSlotInflationBase
      description: maximum inflation of the total supply in slot 0. Usually 33000000 
      source: u64/%d
   -
      sym: constLinearInflationSlots
      description: number of slot with linear inflation
      source: u64/%d
   -
      sym: constBranchInflationBonusBase
      description: maximum value of the branch inflation bonus. Usually 5000000
      source: u64/%d
   -
      sym: constMinimumAmountOnSequencer
      description: minimum amount of tokens on the sequencer output. For testnet it is 1000 * PRXI = 1000000000
      source: u64/%d
   -
      sym: constMaxNumberOfEndorsements
      description: up to 8 endorsements
      source: u64/%d
   -
      sym: constPreBranchConsolidationTicks
      description: number of last ticks in a slot when sequencer transaction cannot consume more than 2 UTXOs
      source: u64/%d
   -
      sym: constPostBranchConsolidationTicks
      description: number of first ticks in the timestamp of the sequencer transaction
      source: u64/%d
   -
      sym: constTransactionPace
      description: minimum number of ticks between non-sequencer transaction and its inputs  
      source: u64/%d
   -
      sym: constTransactionPaceSequencer
      description: minimum number of ticks between sequencer transaction and its inputs and endorsed transactions  
      source: u64/%d
   -
      sym: constVBCost16
      description: constant for the storage deposit constraint  
      source: u16/%d
   -
      sym: constDescription
      description: arbitrary binary data  
      source: 0x%s
   -
      sym: timeSlotSizeBytes
      description: constant for the storage deposit constraint  
      source: 4
   -
      sym: timestampByteSize
      description: constant for the storage deposit constraint  
      source: 5
`
