package ledger

import (
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

// ConstantsFromLibrary loads all runtime constants from the supplied
// EasyFL library by evaluating its named constant expressions. Returns
// the wallet-shaped struct; the two server-only validator names live on
// *Library directly and are populated by the caller from the same
// VersionData payload.
func ConstantsFromLibrary(lib *easyfl.Library[*EvalContext]) *txbuildercore.Constants {
	ret := &txbuildercore.Constants{Hash: lib.LibraryHash()}
	var err error
	var res []byte

	ret.InitialSupply, err = _uint64FromConst(lib, "constInitialSupply")
	util.AssertNoError(err)
	// Token denomination: compile-time constants from ledger/base, not
	// library-derived. Copied into the wallet-facing struct so wallet/UI
	// consumers (incl. wasm) get the names + scale over the wire.
	ret.BaseTokenName = base.BaseTokenName
	ret.BaseTokenNameTicker = base.BaseTokenNameTicker
	ret.SmallestAmountName = base.SmallestAmountName
	ret.SmallestAmountsPerBaseToken = base.PROX
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

	// per-milestone coverageDelta enforcement flag: truthy (non-empty) when active
	res, err = lib.EvalFromSource(nil, "constEnforceCoverageDeltaMonotonicity")
	util.AssertNoError(err)
	ret.EnforceCoverageDeltaMonotonicity = len(res) > 0

	return ret
}

// VersionDataIntegrityValidatorNames extracts the
// (partialContextName, fullContextName) pair from the library's
// VersionData JSON blob. Server-side initialisation only.
func VersionDataIntegrityValidatorNames(versionData []byte) (partialName, fullName string) {
	if len(versionData) == 0 {
		return "", ""
	}
	var marshalled map[string]string
	err := json.Unmarshal(versionData, &marshalled)
	util.AssertNoError(err, "unmarshalling version data JSON")
	partialName = marshalled["txIntegrityValidatorPartialContext"]
	util.Assertf(partialName != "", "txIntegrityValidatorPartialContext not specified")
	fullName = marshalled["txIntegrityValidatorFullContext"]
	util.Assertf(fullName != "", "txIntegrityValidatorFullContext not specified")
	return partialName, fullName
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

// OriginChainID returns the chain ID derived from the genesis output ID.
func OriginChainID() base.ChainID {
	oid := base.GenesisOutputID()
	return base.MakeOriginChainID(oid)
}

// GenesisControlledAddress returns the SigLock of the genesis controller
// (derived from this library's GenesisControllerPublicKey).
func (lib *Library) GenesisControlledAddress() SigLock {
	return SigLockFromED25519PublicKey(lib.GenesisControllerPublicKey)
}

// ConstantsLines renders the runtime constants of this library in a
// human-readable form.
func (lib *Library) ConstantsLines(prefix ...string) *lines.Lines {
	return constantsLines(lib.Constants, lib.TxIntegrityValidatorPartialContextName, lib.TxIntegrityValidatorFullContextName, prefix...)
}

// ConstantsString is the indented String form of ConstantsLines.
func (lib *Library) ConstantsString() string {
	return lib.ConstantsLines("    ").String()
}

// ConstantsLinesFromLibrary renders the runtime constants of a
// freshly-parsed library. Used by offline proxi utilities that don't
// build a full *ledger.Library wrapper.
func ConstantsLinesFromLibrary(lib *easyfl.Library[*EvalContext], prefix ...string) *lines.Lines {
	partialName, fullName := VersionDataIntegrityValidatorNames(lib.VersionData)
	return constantsLines(ConstantsFromLibrary(lib), partialName, fullName, prefix...)
}

// ConstantsStringFromLibrary is the indented String form of
// ConstantsLinesFromLibrary.
func ConstantsStringFromLibrary(lib *easyfl.Library[*EvalContext]) string {
	return ConstantsLinesFromLibrary(lib, "    ").String()
}

func constantsLines(c *txbuildercore.Constants, partialName, fullName string, prefix ...string) *lines.Lines {
	originChainID := OriginChainID()
	ret := lines.New(prefix...).
		Add("Library hash: %x", c.Hash[:]).
		Add("Description: '%s'", c.Description).
		Add("Initial supply: %s", util.Th(c.InitialSupply)).
		Add("Base token: %s (ticker %s); smallest amount: %s; 1 %s = %s %s",
			c.BaseTokenName, c.BaseTokenNameTicker, c.SmallestAmountName,
			c.BaseTokenName, util.Th(c.SmallestAmountsPerBaseToken), c.SmallestAmountName).
		Add("Genesis controller public key: %x", []byte(c.GenesisControllerPublicKey)).
		Add("Genesis controller address: %s", SigLockFromED25519PublicKey(c.GenesisControllerPublicKey).String()).
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
		Add("Tx integrity validator (partial context): '%s'", partialName).
		Add("Tx integrity validator (full context): '%s'", fullName)
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

// TimeConstantsToString prints diagnostic timing info derived from this
// library's tick / slot / genesis constants.
func (lib *Library) TimeConstantsToString() string {
	c := lib.Constants
	nowis := time.Now()
	timestampNowis := c.LedgerTimeFromClockTime(nowis)
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
