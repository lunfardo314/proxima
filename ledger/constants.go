package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// Constants contains constant values of the ledger
type Constants struct {
	Hash [32]byte
	//
	TxIntegrityValidatorPartialContextName string
	TxIntegrityValidatorFullContextName    string
	// arbitrary string up 255 bytes
	Description string
	// genesis time unix seconds
	GenesisTimeUnix uint32
	// ED25519 public key of the controller
	GenesisControllerPublicKey ed25519.PublicKey
	// time tick duration in nanoseconds
	TickDuration time.Duration
	TicksPerSlot uint64
	// initial supply of tokens
	InitialSupply uint64
	// ----------- begin inflation-related
	SlotInflationBase        uint64 // inflation of the total initial supply in slot 0
	MinimumInflatableAmount0 uint64 // initial supply / slot inflation base
	// ----------- end inflation-related
	// number of ticks between non-sequencer transactions
	TransactionPace byte
	// number of ticks between sequencer transactions
	TransactionPaceSequencer byte
	// limit maximum number of endorsements. For determinism
	MaxNumberOfEndorsements uint64
	// PreBranchConsolidationTicks enforces endorsement-only constraint for specified amount of ticks
	// before the slot boundary. It means, sequencer transaction can have only one input, its own predecessor
	// for any transaction with timestamp ticks > MaxTickValueInSlot - PreBranchConsolidationTicks
	// value 0 of PreBranchConsolidationTicks effectively means no constraint
	PreBranchConsolidationTicks uint8
	// -------------- delegation related
	// number of slots where target cannot consume delegation output
	SafeRevocationSlots uint32
	// number of slot in the delegation epoch. Default value consulted by
	// proxi when creating chains that opt in to accept delegations.
	// The per-chain value is carried in the chain's delegationParams
	// constraint (see claude/delegation_epoch_params.md).
	DelegationEpochSlots uint32
	// maximum number of frozen epochs. Default value (see above).
	MaxFrozenEpochs uint32
	// Bounds enforced by the delegationParams constraint at chain origin.
	DelegationEpochSlotsMin      uint32
	DelegationEpochSlotsMax      uint32
	DelegationMaxFrozenEpochsMin uint32
	DelegationMaxFrozenEpochsMax uint32
	// ---------- tag-along related
	TagAlongSlots        uint32
	TagAlongReclaimSlots uint32
	// ---------- attachment related
	AttachmentCostBudget int
	// ---------- GC related
	// number of slots to keep committed transaction IDs in state before GC
	TxIDStateTTLSlots uint32
	// ---------- branch coverage related
	// Branch coverage bounds are now slot-dependent functions accessed via Library methods:
	// Library.BranchCoverageLowerBound(slot) and Library.BranchCoverageUpperBound(slot)
	// ---------- healthy branch fraction (single source of truth) ----------
	// The branch is "healthy" iff coverageDelta * Denominator > 2 * supply * Numerator.
	// The same predicate is enforced inside the stemLock constraint via the
	// `healthyCoverageDelta(supply, covDelta)` EasyFL function.
	HealthyCoverageNumerator   uint64
	HealthyCoverageDenominator uint64
}

// ConstantsFromLibrary loads all constants from library definition into a runtime structure
func ConstantsFromLibrary(lib *easyfl.Library[*EvalContext]) *Constants {
	ret := &Constants{Hash: lib.LibraryHash()}
	var err error
	var res []byte

	if len(lib.VersionData) > 0 {
		var marshalled map[string]string
		err = json.Unmarshal(lib.VersionData, &marshalled)
		util.AssertNoError(err, "unmarshalling version data JSON")
		ret.TxIntegrityValidatorPartialContextName = marshalled["txIntegrityValidatorPartialContext"]
		util.Assertf(ret.TxIntegrityValidatorPartialContextName != "", "txIntegrityValidatorPartialContext not specified")
		ret.TxIntegrityValidatorFullContextName = marshalled["txIntegrityValidatorFullContext"]
		util.Assertf(ret.TxIntegrityValidatorFullContextName != "", "txIntegrityValidatorFullContext not specified")
	}

	ret.InitialSupply, err = _uint64FromConst(lib, "constInitialSupply")
	util.AssertNoError(err)
	res, err = lib.EvalFromSource(nil, "constGenesisControllerPublicKey")
	util.AssertNoError(err)
	ret.GenesisControllerPublicKey = res
	gt, err := _uint64FromConst(lib, "constGenesisTimeUnix")
	util.AssertNoError(err)
	ret.GenesisTimeUnix = uint32(gt)
	ret.TicksPerSlot, err = _uint64FromConst(lib, "ticksPerSlot64")
	util.AssertNoError(err)
	td, err := _uint64FromConst(lib, "constTickDuration")
	util.AssertNoError(err)
	ret.TickDuration = time.Duration(td)
	ret.SlotInflationBase, err = _uint64FromConst(lib, "constSlotInflationBase")
	util.AssertNoError(err)

	ret.MinimumInflatableAmount0, err = _uint64FromConst(lib, "minimumInflatableAmount0")
	util.AssertNoError(err)
	util.Assertf(ret.MinimumInflatableAmount0 == ret.InitialSupply/ret.SlotInflationBase, "ret.MinimumInflatableAmount0 == ret.InitialSupply / ret.SlotInflationBase")

	ret.MaxNumberOfEndorsements, err = _uint64FromConst(lib, "constMaxNumberOfEndorsements")
	util.AssertNoError(err)
	pb, err := _uint64FromConst(lib, "constPreBranchConsolidationTicks")
	util.AssertNoError(err)
	ret.PreBranchConsolidationTicks = byte(pb)
	tp, err := _uint64FromConst(lib, "constTransactionPace")
	util.AssertNoError(err)
	ret.TransactionPace = byte(tp)
	tp, err = _uint64FromConst(lib, "constTransactionPaceSequencer")
	util.AssertNoError(err)
	ret.TransactionPaceSequencer = byte(tp)
	res, err = lib.EvalFromSource(nil, "constDescription")
	util.AssertNoError(err)
	ret.Description = string(res)

	// delegation related
	ret.SafeRevocationSlots, err = _uint32FromConst(lib, "constDelegationSafeRevocationSlots")
	util.AssertNoError(err)
	ret.DelegationEpochSlots, err = _uint32FromConst(lib, "constDelegationEpochSlots")
	util.AssertNoError(err)
	ret.MaxFrozenEpochs, err = _uint32FromConst(lib, "constDelegationMaxFrozenEpochs")
	util.AssertNoError(err)
	// Per-target delegation params bounds (claude/delegation_epoch_params.md)
	ret.DelegationEpochSlotsMin, err = _uint32FromConst(lib, "constDelegationEpochSlotsMin")
	util.AssertNoError(err)
	ret.DelegationEpochSlotsMax, err = _uint32FromConst(lib, "constDelegationEpochSlotsMax")
	util.AssertNoError(err)
	ret.DelegationMaxFrozenEpochsMin, err = _uint32FromConst(lib, "constDelegationMaxFrozenEpochsMin")
	util.AssertNoError(err)
	ret.DelegationMaxFrozenEpochsMax, err = _uint32FromConst(lib, "constDelegationMaxFrozenEpochsMax")
	util.AssertNoError(err)

	// tag-along related
	var t64 uint64
	t64, err = _uint64FromConst(lib, "constTagAlongSlots")
	util.AssertNoError(err)
	util.Assertf(t64 < math.MaxUint32, "constTagAlongSlots: %d", t64)
	ret.TagAlongSlots = uint32(t64)

	t64, err = _uint64FromConst(lib, "constTagAlongReclaimSlots")
	util.AssertNoError(err)
	util.Assertf(t64 < math.MaxUint32, "constTagAlongReclaimSlots: %d", t64)
	ret.TagAlongReclaimSlots = uint32(t64)

	// attachment related
	t64, err = _uint64FromConst(lib, "constAttachmentCostBudget")
	util.AssertNoError(err)
	ret.AttachmentCostBudget = int(t64)

	// GC related
	t64, err = _uint64FromConst(lib, "constTxIDStateTTLSlots")
	util.AssertNoError(err)
	util.Assertf(t64 < math.MaxUint32, "constTxIDStateTTLSlots: %d", t64)
	ret.TxIDStateTTLSlots = uint32(t64)

	// healthy-branch fraction (numerator / denominator) — single source of truth
	ret.HealthyCoverageNumerator, err = _uint64FromConst(lib, "constHealthyCoverageNumerator")
	util.AssertNoError(err)
	ret.HealthyCoverageDenominator, err = _uint64FromConst(lib, "constHealthyCoverageDenominator")
	util.AssertNoError(err)
	util.Assertf(ret.HealthyCoverageDenominator > 0, "constHealthyCoverageDenominator must be > 0")

	return ret
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
	ret := lines.New(prefix...).
		Add("Library hash: %s", hex.EncodeToString(c.Hash[:])).
		Add("Description: '%s'", c.Description).
		Add("Initial supply: %s", util.Th(c.InitialSupply)).
		Add("Genesis controller public key: %s", hex.EncodeToString(c.GenesisControllerPublicKey)).
		Add("Genesis controller address: %s", c.GenesisControlledAddress().String()).
		Add("Genesis Unix time: %d (%s)", c.GenesisTimeUnix, c.GenesisTime().Format(time.DateTime)).
		Add("Tick duration: %v", c.TickDuration).
		Add("Ticks per slot: %d", c.TicksPerSlot).
		Add("Slot duration: %v", c.SlotDuration()).
		Add("Slot inflation base: %s", util.Th(c.SlotInflationBase)).
		Add("Minimum inflatable amount in slot 0: %s", util.Th(c.MinimumInflatableAmount0)).
		Add("Pre-branch consolidation ticks: %v", c.PreBranchConsolidationTicks).
		Add("Transaction pace: %d", c.TransactionPace).
		Add("Sequencer pace: %d", c.TransactionPaceSequencer).
		Add("Max number of endorsements: %d", c.MaxNumberOfEndorsements).
		Add("Tx integrity validator (partial context): '%s'", c.TxIntegrityValidatorPartialContextName).
		Add("Tx integrity validator (full context): '%s'", c.TxIntegrityValidatorFullContextName)
	epochDuration := time.Duration(c.DelegationEpochSlots) * c.SlotDuration()
	ret.Add("Delegation epoch slots (default): %d, epoch duration: %v", c.DelegationEpochSlots, epochDuration)
	ret.Add("Delegation epoch slots bounds: [%d, %d]", c.DelegationEpochSlotsMin, c.DelegationEpochSlotsMax)
	maxFrozenDuration := time.Duration(c.MaxFrozenEpochs) * epochDuration
	ret.Add("Maximum frozen delegation epochs (default): %d (%v)", c.MaxFrozenEpochs, maxFrozenDuration)
	ret.Add("Maximum frozen delegation epochs bounds: [%d, %d]", c.DelegationMaxFrozenEpochsMin, c.DelegationMaxFrozenEpochsMax)
	safeDuration := time.Duration(c.SafeRevocationSlots) * c.SlotDuration()
	ret.Add("Safe revocation slots: %d (%v)", c.SafeRevocationSlots, safeDuration).
		Add("Bootstrap sequencer ID (calculated): %s", originChainID.String()).
		Add("Attachment cost budget: %d", c.AttachmentCostBudget).
		Add("TxID state TTL slots: %d (%v)", c.TxIDStateTTLSlots, time.Duration(c.TxIDStateTTLSlots)*c.SlotDuration())

	return ret
}

func (c *Constants) TimeConstantsToString() string {
	nowis := time.Now()
	timestampNowis := c.LedgerTimeFromClockTime(nowis)

	// TODO sometimes fails
	//util.Assertf(util.Abs(nowis.UnixNano()-timestampNowis.UnixNano()) < int64(TickDuration()),
	//	"nowis.UnixNano()(%d)-timestampNowis.UnixNano()(%d) = %d < int64(TickDuration())(%d)",
	//	nowis.UnixNano(), timestampNowis.UnixNano(), nowis.UnixNano()-timestampNowis.UnixNano(), int64(TickDuration()))

	maxYears := base.MaxSlot / (c.SlotsPerDay() * 365)
	return lines.New().
		Add("TickDuration = %v", c.TickDuration).
		Add("SlotDuration = %v", c.SlotDuration()).
		Add("SlotsPerDay = %d", c.SlotsPerDay()).
		Add("MaxYears = %d", maxYears).
		Add("seconds per year = %d", 60*60*24*365).
		Add("GenesisTime = %v", c.GenesisTime().Format(time.StampNano)).
		Add("nowis %v", nowis.Format(time.StampNano)).
		Add("nowis nano %d", nowis.UnixNano()).
		Add("GenesisTimeUnix = %d", c.GenesisTimeUnix).
		Add("GenesisTimeUnixNano = %d", c.GenesisTimeUnixNano()).
		Add("ticks since genesis: %d", c.TimeToTicksSinceGenesis(nowis)).
		Add("timestampNowis = %s ", timestampNowis.String()).
		Add("timestampNowis.ClockTime() = %v ", ClockTime(timestampNowis)).
		Add("timestampNowis.ClockTime().UnixNano() = %v ", ClockTime(timestampNowis).UnixNano()).
		Add("timestampNowis.UnixNano() = %v ", UnixNanoFromLedgerTime(timestampNowis)).
		Add("rounding: nowis.UnixNano() - timestampNowis.UnixNano() = %d", nowis.UnixNano()-UnixNanoFromLedgerTime(timestampNowis)).
		Add("tick duration nano = %d", int64(TickDuration())).
		String()
}

func (c *Constants) String() string {
	return c.Lines("    ").String()
}

func OriginChainID() base.ChainID {
	oid := base.GenesisOutputID()
	return base.MakeOriginChainID(oid)
}

func (c *Constants) GenesisControlledAddress() SigLock {
	return SigLockFromED25519PublicKey(c.GenesisControllerPublicKey)
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

func (c *Constants) IsPreBranchConsolidationTimestamp(ts base.LedgerTime) bool {
	return ts.Tick > base.MaxTickValue-c.PreBranchConsolidationTicks
}

func (c *Constants) GenesisTimeUnixNano() int64 {
	return time.Unix(int64(c.GenesisTimeUnix), 0).UnixNano()
}

// ToWalletConstants converts the host-side *Constants into the
// wallet-side txbuildercore.Constants. Field set on the wallet side
// is intentionally a subset (no server-internal validator names);
// keep this adapter in sync as fields land on both sides. See
// claude/wallet_eval_api.md (Phase A2).
func (c *Constants) ToWalletConstants() *txbuildercore.Constants {
	return &txbuildercore.Constants{
		Hash:                         c.Hash,
		Description:                  c.Description,
		GenesisControllerPublicKey:   append(ed25519.PublicKey(nil), c.GenesisControllerPublicKey...),
		GenesisTimeUnix:              c.GenesisTimeUnix,
		TickDuration:                 c.TickDuration,
		TicksPerSlot:                 c.TicksPerSlot,
		InitialSupply:                c.InitialSupply,
		SlotInflationBase:            c.SlotInflationBase,
		MinimumInflatableAmount0:     c.MinimumInflatableAmount0,
		TransactionPace:              c.TransactionPace,
		TransactionPaceSequencer:     c.TransactionPaceSequencer,
		MaxNumberOfEndorsements:      c.MaxNumberOfEndorsements,
		PreBranchConsolidationTicks:  c.PreBranchConsolidationTicks,
		SafeRevocationSlots:          c.SafeRevocationSlots,
		DelegationEpochSlots:         c.DelegationEpochSlots,
		MaxFrozenEpochs:              c.MaxFrozenEpochs,
		DelegationEpochSlotsMin:      c.DelegationEpochSlotsMin,
		DelegationEpochSlotsMax:      c.DelegationEpochSlotsMax,
		DelegationMaxFrozenEpochsMin: c.DelegationMaxFrozenEpochsMin,
		DelegationMaxFrozenEpochsMax: c.DelegationMaxFrozenEpochsMax,
		TagAlongSlots:                c.TagAlongSlots,
		TagAlongReclaimSlots:         c.TagAlongReclaimSlots,
		AttachmentCostBudget:         c.AttachmentCostBudget,
		TxIDStateTTLSlots:            c.TxIDStateTTLSlots,
		HealthyCoverageNumerator:     c.HealthyCoverageNumerator,
		HealthyCoverageDenominator:   c.HealthyCoverageDenominator,
	}
}
