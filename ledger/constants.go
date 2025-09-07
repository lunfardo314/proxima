package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// Constants contains constant values of the ledger
type Constants struct {
	// arbitrary string up 255 bytes
	Description string
	// genesis time unix seconds
	GenesisTimeUnix uint32
	// ED25519 public key of the controller
	GenesisControllerPublicKey ed25519.PublicKey
	// time tick duration in nanoseconds
	TickDuration time.Duration
	// initial supply of tokens
	InitialSupply uint64
	// ----------- begin inflation-related
	SlotInflationBase        uint64 // constant C
	MinimumInflatableAmount0 uint64 // calculated -> initial supply / slot inflation base
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
	// -------------- delegation related
	// number of slots where target cannot consume delegation output
	SafeRevocationSlots uint32
	// number of slot in the delegation epoch
	DelegationEpochSlots uint32
	// maximum number of frozen epochs
	MaxFrozenEpochs uint32
}

var Const *Constants

// constantsFromLibrary load all constants from library definition into a runtime structure
func initConstantsFromLibrary(lib *easyfl.Library[*EvalContext]) {
	Const = &Constants{}
	var err error
	var res []byte
	Const.InitialSupply, err = _uint64FromConst(lib, "constInitialSupply")
	util.AssertNoError(err)
	res, err = lib.EvalFromSource(nil, "constGenesisControllerPublicKey")
	util.AssertNoError(err)
	Const.GenesisControllerPublicKey = res
	gt, err := _uint64FromConst(lib, "constGenesisTimeUnix")
	util.AssertNoError(err)
	Const.GenesisTimeUnix = uint32(gt)
	td, err := _uint64FromConst(lib, "constTickDuration")
	util.AssertNoError(err)
	Const.TickDuration = time.Duration(td)
	Const.SlotInflationBase, err = _uint64FromConst(lib, "constSlotInflationBase")
	util.AssertNoError(err)
	Const.MinimumInflatableAmount0 = Const.InitialSupply / Const.SlotInflationBase
	Const.BranchInflationBonusBase, err = _uint64FromConst(lib, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	Const.MinimumAmountOnSequencer, err = _uint64FromConst(lib, "constMinimumAmountOnSequencer")
	util.AssertNoError(err)
	Const.MaxNumberOfEndorsements, err = _uint64FromConst(lib, "constMaxNumberOfEndorsements")
	util.AssertNoError(err)
	pb, err := _uint64FromConst(lib, "constPreBranchConsolidationTicks")
	util.AssertNoError(err)
	Const.PreBranchConsolidationTicks = byte(pb)
	pb, err = _uint64FromConst(lib, "constPostBranchConsolidationTicks")
	util.AssertNoError(err)
	Const.PostBranchConsolidationTicks = byte(pb)
	tp, err := _uint64FromConst(lib, "constTransactionPace")
	util.AssertNoError(err)
	Const.TransactionPace = byte(tp)
	tp, err = _uint64FromConst(lib, "constTransactionPaceSequencer")
	util.AssertNoError(err)
	Const.TransactionPaceSequencer = byte(tp)
	Const.VBCost, err = _uint64FromConst(lib, "constVBCost16")
	util.AssertNoError(err)
	res, err = lib.EvalFromSource(nil, "constDescription")
	util.AssertNoError(err)
	Const.Description = string(res)

	// delegation related
	Const.SafeRevocationSlots, err = _uint32FromConst(lib, "constDelegationSafeRevocationSlots")
	util.AssertNoError(err)
	Const.DelegationEpochSlots, err = _uint32FromConst(lib, "constDelegationEpochSlots")
	util.AssertNoError(err)
	Const.MaxFrozenEpochs, err = _uint32FromConst(lib, "constDelegationMaxFrozenEpochs")
	util.AssertNoError(err)
}

func _uint64FromConst(lib *easyfl.Library[*EvalContext], constName string) (uint64, error) {
	res, err := lib.EvalFromSource(nil, constName)
	if err != nil {
		return 0, err
	}
	return easyfl_util.Uint64FromBytes(res)
}

func _uint32FromConst(lib *easyfl.Library[*EvalContext], constName string) (uint32, error) {
	res, err := lib.EvalFromSource(nil, constName)
	if err != nil {
		return 0, err
	}
	return easyfl_util.Uint32FromBytes(res)
}

func (c *Constants) Lines(prefix ...string) *lines.Lines {
	originChainID := OriginChainID()
	return lines.New(prefix...).
		Add("Description: '%s'", c.Description).
		Add("Initial supply: %s", util.Th(c.InitialSupply)).
		Add("Genesis controller public key: %s", hex.EncodeToString(c.GenesisControllerPublicKey)).
		Add("Genesis controller address (calculated): %s", c.GenesisControlledAddress().String()).
		Add("Genesis Unix time: %d (%s)", c.GenesisTimeUnix, c.GenesisTime().Format(time.RFC3339)).
		Add("Tick duration: %v", c.TickDuration).
		Add("Slot inflation base (constant C): %s", util.Th(c.SlotInflationBase)).
		Add("Minimum inflatable amount: %s", util.Th(c.MinimumInflatableAmount0)).
		Add("Constant initial supply/slot inflation base: %s", util.Th(c.InitialSupply/c.SlotInflationBase)).
		Add("Branch inflation bonus base: %s", util.Th(c.BranchInflationBonusBase)).
		Add("Pre-branch consolidation ticks: %v", c.PreBranchConsolidationTicks).
		Add("Post-branch consolidation ticks: %v", c.PostBranchConsolidationTicks).
		Add("Minimum amount on sequencer: %s", util.Th(c.MinimumAmountOnSequencer)).
		Add("Transaction pace: %d", c.TransactionPace).
		Add("Sequencer pace: %d", c.TransactionPaceSequencer).
		Add("VB cost: %d", c.VBCost).
		Add("Max number of endorsements: %d", c.MaxNumberOfEndorsements).
		Add("Origin chain c (calculated): %s", originChainID.String()).
		Add("Maximum frozen epochs: %d", c.MaxFrozenEpochs).
		Add("Delegation epoch slots: %d", c.DelegationEpochSlots).
		Add("Safe revocation slots: %d", c.SafeRevocationSlots)
}

func OriginChainID() base.ChainID {
	oid := base.GenesisOutputID()
	return base.MakeOriginChainID(oid)
}

func (c *Constants) GenesisControlledAddress() AddressED25519 {
	return AddressED25519FromPublicKey(c.GenesisControllerPublicKey)
}

func (c *Constants) GenesisTime() time.Time {
	return time.Unix(int64(c.GenesisTimeUnix), 0)
}

func (c *Constants) SlotDuration() time.Duration {
	return c.TickDuration * time.Duration(base.TicksPerSlot)
}

func (c *Constants) SlotsPerDay() int {
	return int(24 * time.Hour / c.SlotDuration())
}

func (c *Constants) SlotsPerYear() int {
	return 365 * c.SlotsPerDay()
}

func (c *Constants) TicksPerYear() int {
	return c.SlotsPerYear() * base.TicksPerSlot
}

// TimeToTicksSinceGenesis converts time value into ticks since genesis
func (c *Constants) TimeToTicksSinceGenesis(nowis time.Time) int64 {
	timeSinceGenesis := nowis.Sub(c.GenesisTime())
	return int64(timeSinceGenesis / c.TickDuration)
}

func (c *Constants) LedgerTimeFromClockTime(nowis time.Time) base.LedgerTime {
	ret, err := base.LedgerTimeFromTicksSinceGenesis(c.TimeToTicksSinceGenesis(nowis))
	util.AssertNoError(err)
	return ret
}
