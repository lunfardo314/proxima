package txbuildercore

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
)

// Constants is the wallet-side view of the runtime ledger constants
// the host extracts from its active library. Flat struct of plain Go
// types; no imports of the ledger package. Populated either by the
// API's /ledger_constants endpoint (see claude/wallet_eval_api.md) or
// by a future top-level field in library.json. Field set mirrors the
// subset of ledger.Constants that wallets plausibly need.
//
// Wire encoding (see MarshalJSON/UnmarshalJSON):
//   - every numeric field serialises as a JSON integer;
//   - Hash and GenesisControllerPublicKey serialise as plain hex
//     strings (no "0x" prefix);
//   - TickDuration serialises as integer nanoseconds (Go default for
//     time.Duration).
type Constants struct {
	// Library hash (blake2b-256 over the canonical library bytes).
	Hash [32]byte
	// Free-form library description supplied at genesis.
	Description string
	// Genesis controller's ED25519 public key (32 bytes).
	GenesisControllerPublicKey ed25519.PublicKey
	// Unix epoch (seconds) of ledger genesis.
	GenesisTimeUnix uint32
	// Per-tick duration.
	TickDuration time.Duration
	// Number of ticks per slot.
	TicksPerSlot uint64
	// Target base supply T: the ceiling of base-token supply once mining
	// exhausts R_init. Supply-relative policy is anchored here (see
	// claude/fairlaunch.md).
	TargetBaseSupply uint64
	// Initial supply at genesis (one tenth of TargetBaseSupply).
	InitialSupply uint64
	// Token denomination (compile-time, from ledger/base): the base
	// token's full name and ticker, the name of the smallest indivisible
	// amount, and how many smallest amounts make one whole base token.
	// Exposed so wallet UIs can format amounts without hard-coding them.
	BaseTokenName               string
	BaseTokenNameTicker         string
	SmallestAmountName          string
	SmallestAmountsPerBaseToken uint64
	// Inflation-related.
	SlotInflationBase        uint64
	MinimumInflatableAmount0 uint64
	// Fair-launch mine chain policy (see claude/fairlaunch.md). A is the
	// fixed amount minted per transit, [E, C] the retarget band, P the
	// minimum chain pace in slots and MineTargetPace the slots-per-transit
	// the retarget aims at. The mutable difficulty B lives in the mine
	// output's lock, not here; the wallet needs the band and the target to
	// mirror the retarget when building a successor.
	MineAmount          uint64
	MineFloorDifficulty uint64
	MineMaxDifficulty   uint64
	MineTargetPace      uint64
	MineReliefPace      uint64
	MineMinPace         uint64
	// Pace constants.
	TransactionPace          byte
	TransactionPaceSequencer byte
	// Endorsement count cap.
	MaxNumberOfEndorsements uint64
	// PreBranchConsolidationTicks enforces endorsement-only constraint
	// for that many ticks before the slot boundary.
	PreBranchConsolidationTicks byte
	// Delegation parameters (defaults + bounds; per-chain values are
	// inlined as the two args of the chain's sequencer constraint at
	// SequencerConstraintFixedIndex).
	SafeRevocationSlots          uint32
	DelegationEpochSlots         uint32
	DelegationEpochSlotsMin      uint32
	DelegationEpochSlotsMax      uint32
	DelegationMaxFrozenEpochsMin uint32
	DelegationMaxFrozenEpochsMax uint32
	// Tag-along window.
	TagAlongSlots        uint32
	TagAlongReclaimSlots uint32
	// Attachment-cost ceiling for a tx + its non-rooted past cone.
	AttachmentCostBudget int
	// GC: TTL of committed transaction IDs in the state trie. Tiered by branch flag —
	// non-branch records are pruned fast; branch records (read by LRB/sync) are kept far longer.
	TxIDStateTTLSlots       uint32
	BranchTxIDStateTTLSlots uint32
	// Healthy-coverage fraction (numerator / denominator).
	HealthyCoverageNumerator   uint64
	HealthyCoverageDenominator uint64
	// EnforceCoverageDeltaMonotonicity gates the per-milestone coverageDelta
	// enforcement (on-chain within-slot strict-increase rule + the attacher's
	// computed-vs-declared cross-check). Production = true.
	EnforceCoverageDeltaMonotonicity bool
}

// constantsJSON is the wire shape. Numeric fields stay as JSON
// integers (Go default); Hash and GenesisControllerPublicKey become
// plain hex strings (no "0x" prefix) so the document reads cleanly
// across languages.
type constantsJSON struct {
	Hash                         string `json:"hash"`
	Description                  string `json:"description"`
	GenesisControllerPublicKey   string `json:"genesis_controller_public_key"`
	GenesisTimeUnix              uint32 `json:"genesis_time_unix"`
	TickDuration                 int64  `json:"tick_duration_ns"`
	TicksPerSlot                 uint64 `json:"ticks_per_slot"`
	TargetBaseSupply             uint64 `json:"target_base_supply"`
	InitialSupply                uint64 `json:"initial_supply"`
	BaseTokenName                string `json:"base_token_name"`
	BaseTokenNameTicker          string `json:"base_token_name_ticker"`
	SmallestAmountName           string `json:"smallest_amount_name"`
	SmallestAmountsPerBaseToken  uint64 `json:"smallest_amounts_per_base_token"`
	SlotInflationBase            uint64 `json:"slot_inflation_base"`
	MinimumInflatableAmount0     uint64 `json:"minimum_inflatable_amount_0"`
	MineAmount                   uint64 `json:"mine_amount"`
	MineFloorDifficulty          uint64 `json:"mine_floor_difficulty"`
	MineMaxDifficulty            uint64 `json:"mine_max_difficulty"`
	MineTargetPace               uint64 `json:"mine_target_pace"`
	MineReliefPace               uint64 `json:"mine_relief_pace"`
	MineMinPace                  uint64 `json:"mine_min_pace"`
	TransactionPace              byte   `json:"transaction_pace"`
	TransactionPaceSequencer     byte   `json:"transaction_pace_sequencer"`
	MaxNumberOfEndorsements      uint64 `json:"max_number_of_endorsements"`
	PreBranchConsolidationTicks  byte   `json:"pre_branch_consolidation_ticks"`
	SafeRevocationSlots          uint32 `json:"safe_revocation_slots"`
	DelegationEpochSlots         uint32 `json:"delegation_epoch_slots"`
	DelegationEpochSlotsMin      uint32 `json:"delegation_epoch_slots_min"`
	DelegationEpochSlotsMax      uint32 `json:"delegation_epoch_slots_max"`
	DelegationMaxFrozenEpochsMin uint32 `json:"delegation_max_frozen_epochs_min"`
	DelegationMaxFrozenEpochsMax uint32 `json:"delegation_max_frozen_epochs_max"`
	TagAlongSlots                uint32 `json:"tag_along_slots"`
	TagAlongReclaimSlots         uint32 `json:"tag_along_reclaim_slots"`
	AttachmentCostBudget         int    `json:"attachment_cost_budget"`
	TxIDStateTTLSlots            uint32 `json:"tx_id_state_ttl_slots"`
	BranchTxIDStateTTLSlots      uint32 `json:"branch_tx_id_state_ttl_slots"`
	HealthyCoverageNumerator     uint64 `json:"healthy_coverage_numerator"`
	HealthyCoverageDenominator   uint64 `json:"healthy_coverage_denominator"`
	EnforceCoverageDeltaMonotonicity bool `json:"enforce_coverage_delta_monotonicity"`
}

func (c *Constants) MarshalJSON() ([]byte, error) {
	return json.Marshal(constantsJSON{
		Hash:                         hex.EncodeToString(c.Hash[:]),
		Description:                  c.Description,
		GenesisControllerPublicKey:   hex.EncodeToString(c.GenesisControllerPublicKey),
		GenesisTimeUnix:              c.GenesisTimeUnix,
		TickDuration:                 int64(c.TickDuration),
		TicksPerSlot:                 c.TicksPerSlot,
		TargetBaseSupply:             c.TargetBaseSupply,
		InitialSupply:                c.InitialSupply,
		BaseTokenName:                c.BaseTokenName,
		BaseTokenNameTicker:          c.BaseTokenNameTicker,
		SmallestAmountName:           c.SmallestAmountName,
		SmallestAmountsPerBaseToken:  c.SmallestAmountsPerBaseToken,
		SlotInflationBase:            c.SlotInflationBase,
		MinimumInflatableAmount0:     c.MinimumInflatableAmount0,
		MineAmount:                   c.MineAmount,
		MineFloorDifficulty:          c.MineFloorDifficulty,
		MineMaxDifficulty:            c.MineMaxDifficulty,
		MineTargetPace:               c.MineTargetPace,
		MineReliefPace:               c.MineReliefPace,
		MineMinPace:                  c.MineMinPace,
		TransactionPace:              c.TransactionPace,
		TransactionPaceSequencer:     c.TransactionPaceSequencer,
		MaxNumberOfEndorsements:      c.MaxNumberOfEndorsements,
		PreBranchConsolidationTicks:  c.PreBranchConsolidationTicks,
		SafeRevocationSlots:          c.SafeRevocationSlots,
		DelegationEpochSlots:         c.DelegationEpochSlots,
		DelegationEpochSlotsMin:      c.DelegationEpochSlotsMin,
		DelegationEpochSlotsMax:      c.DelegationEpochSlotsMax,
		DelegationMaxFrozenEpochsMin: c.DelegationMaxFrozenEpochsMin,
		DelegationMaxFrozenEpochsMax: c.DelegationMaxFrozenEpochsMax,
		TagAlongSlots:                c.TagAlongSlots,
		TagAlongReclaimSlots:         c.TagAlongReclaimSlots,
		AttachmentCostBudget:         c.AttachmentCostBudget,
		TxIDStateTTLSlots:            c.TxIDStateTTLSlots,
		BranchTxIDStateTTLSlots:      c.BranchTxIDStateTTLSlots,
		HealthyCoverageNumerator:     c.HealthyCoverageNumerator,
		HealthyCoverageDenominator:   c.HealthyCoverageDenominator,
		EnforceCoverageDeltaMonotonicity: c.EnforceCoverageDeltaMonotonicity,
	})
}

func (c *Constants) UnmarshalJSON(data []byte) error {
	var raw constantsJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	hashBytes, err := hex.DecodeString(raw.Hash)
	if err != nil {
		return fmt.Errorf("Constants.UnmarshalJSON: hash hex decode: %w", err)
	}
	if len(hashBytes) != 32 {
		return fmt.Errorf("Constants.UnmarshalJSON: hash must be 32 bytes, got %d", len(hashBytes))
	}
	copy(c.Hash[:], hashBytes)
	pubBytes, err := hex.DecodeString(raw.GenesisControllerPublicKey)
	if err != nil {
		return fmt.Errorf("Constants.UnmarshalJSON: pubkey hex decode: %w", err)
	}
	c.GenesisControllerPublicKey = ed25519.PublicKey(pubBytes)
	c.Description = raw.Description
	c.GenesisTimeUnix = raw.GenesisTimeUnix
	c.TickDuration = time.Duration(raw.TickDuration)
	c.TicksPerSlot = raw.TicksPerSlot
	c.TargetBaseSupply = raw.TargetBaseSupply
	c.InitialSupply = raw.InitialSupply
	c.BaseTokenName = raw.BaseTokenName
	c.BaseTokenNameTicker = raw.BaseTokenNameTicker
	c.SmallestAmountName = raw.SmallestAmountName
	c.SmallestAmountsPerBaseToken = raw.SmallestAmountsPerBaseToken
	c.SlotInflationBase = raw.SlotInflationBase
	c.MinimumInflatableAmount0 = raw.MinimumInflatableAmount0
	c.MineAmount = raw.MineAmount
	c.MineFloorDifficulty = raw.MineFloorDifficulty
	c.MineMaxDifficulty = raw.MineMaxDifficulty
	c.MineTargetPace = raw.MineTargetPace
	c.MineReliefPace = raw.MineReliefPace
	c.MineMinPace = raw.MineMinPace
	c.TransactionPace = raw.TransactionPace
	c.TransactionPaceSequencer = raw.TransactionPaceSequencer
	c.MaxNumberOfEndorsements = raw.MaxNumberOfEndorsements
	c.PreBranchConsolidationTicks = raw.PreBranchConsolidationTicks
	c.SafeRevocationSlots = raw.SafeRevocationSlots
	c.DelegationEpochSlots = raw.DelegationEpochSlots
	c.DelegationEpochSlotsMin = raw.DelegationEpochSlotsMin
	c.DelegationEpochSlotsMax = raw.DelegationEpochSlotsMax
	c.DelegationMaxFrozenEpochsMin = raw.DelegationMaxFrozenEpochsMin
	c.DelegationMaxFrozenEpochsMax = raw.DelegationMaxFrozenEpochsMax
	c.TagAlongSlots = raw.TagAlongSlots
	c.TagAlongReclaimSlots = raw.TagAlongReclaimSlots
	c.AttachmentCostBudget = raw.AttachmentCostBudget
	c.TxIDStateTTLSlots = raw.TxIDStateTTLSlots
	c.BranchTxIDStateTTLSlots = raw.BranchTxIDStateTTLSlots
	c.HealthyCoverageNumerator = raw.HealthyCoverageNumerator
	c.HealthyCoverageDenominator = raw.HealthyCoverageDenominator
	c.EnforceCoverageDeltaMonotonicity = raw.EnforceCoverageDeltaMonotonicity
	return nil
}

// SlotDuration returns the wall-clock duration of one slot.
func (c *Constants) SlotDuration() time.Duration {
	return c.TickDuration * time.Duration(base.TicksPerSlot)
}

// SlotsPerDay returns the number of slots in a 24-hour wall-clock day.
// Pure arithmetic on SlotDuration. Mirrors ledger.Constants.SlotsPerDay.
func (c *Constants) SlotsPerDay() int {
	return int(24 * time.Hour / c.SlotDuration())
}

// SlotsPerYear returns the number of slots in a 365-day year. Mirrors
// ledger.Constants.SlotsPerYear.
func (c *Constants) SlotsPerYear() int {
	return 365 * c.SlotsPerDay()
}

// GenesisTime returns the genesis Unix-seconds value as a time.Time.
func (c *Constants) GenesisTime() time.Time {
	return time.Unix(int64(c.GenesisTimeUnix), 0)
}

// GenesisTimeUnixNano returns the genesis timestamp in nanoseconds.
func (c *Constants) GenesisTimeUnixNano() int64 {
	return time.Unix(int64(c.GenesisTimeUnix), 0).UnixNano()
}

// TimeToTicksSinceGenesis converts wall-clock time to ticks since
// genesis.
func (c *Constants) TimeToTicksSinceGenesis(t time.Time) int64 {
	return int64(t.Sub(c.GenesisTime()) / c.TickDuration)
}

// LedgerTimeFromClockTime maps a wall-clock instant to a base.LedgerTime
// using the genesis epoch + tick duration. Panics on overflow (would
// only happen far past base.MaxSlot).
func (c *Constants) LedgerTimeFromClockTime(t time.Time) base.LedgerTime {
	ret, err := base.LedgerTimeFromTicksSinceGenesis(c.TimeToTicksSinceGenesis(t))
	if err != nil {
		panic(fmt.Errorf("LedgerTimeFromClockTime: %w", err))
	}
	return ret
}

// IsPreBranchConsolidationTimestamp reports whether ts.Tick lies
// inside the pre-branch consolidation window — i.e. only endorsement
// inputs are allowed at that timestamp.
func (c *Constants) IsPreBranchConsolidationTimestamp(ts base.LedgerTime) bool {
	return ts.Tick > base.MaxTickValue-c.PreBranchConsolidationTicks
}

// EpochOffsetSlotsDirect returns the per-target slot offset of the
// delegation epoch grid. Each chain's ChainID defines its own grid;
// the offset spreads delegation-output consumption across sequencers.
// Pure arithmetic — first 4 bytes of targetID interpreted as a big-
// endian uint32, modulo epochSlots. Mirrors
// ledger.Constants.EpochOffsetSlotsDirect.
func (c *Constants) EpochOffsetSlotsDirect(targetID base.ChainID, epochSlots uint32) uint32 {
	return binary.BigEndian.Uint32(targetID[:4]) % epochSlots
}

// EpochLimits returns (firstSlot, lastSlot) of the given delegation
// epoch on the target chain's grid. Epoch 0 has firstSlot == 0;
// every other epoch is `epochSlots` wide. Mirrors
// ledger.Constants.EpochLimits.
func (c *Constants) EpochLimits(targetID base.ChainID, epoch, epochSlots uint32) (firstSlot, lastSlot uint32) {
	offs := c.EpochOffsetSlotsDirect(targetID, epochSlots)
	lastSlot = epoch*epochSlots + offs
	if epoch > 0 {
		firstSlot = lastSlot - epochSlots + 1
	}
	return
}

// LastSlotInEpochDirect is the last slot of a delegation epoch on the
// target's grid. Mirrors ledger.Constants.LastSlotInEpochDirect.
func (c *Constants) LastSlotInEpochDirect(targetID base.ChainID, epoch, epochSlots uint32) uint32 {
	_, lastSlot := c.EpochLimits(targetID, epoch, epochSlots)
	return lastSlot
}

// EpochFromSlotDirect returns which delegation epoch (on the target's
// grid) `slot` belongs to. Slots before the first epoch boundary
// belong to epoch 0. Mirrors ledger.Constants.EpochFromSlotDirect.
func (c *Constants) EpochFromSlotDirect(targetID base.ChainID, slot, epochSlots uint32) uint32 {
	offs := c.EpochOffsetSlotsDirect(targetID, epochSlots)
	if slot > offs {
		return (slot-offs-1)/epochSlots + 1
	}
	return 0
}

// CoveredSlotsInCurrentEpoch returns how many slots remain in the
// current epoch (the one `slot` belongs to) on the target's grid,
// counting `slot` itself. Mirrors
// ledger.Constants.CoveredSlotsInCurrentEpoch.
func (c *Constants) CoveredSlotsInCurrentEpoch(targetID base.ChainID, slot, epochSlots uint32) uint32 {
	last := c.LastSlotInEpochDirect(targetID, c.EpochFromSlotDirect(targetID, slot, epochSlots), epochSlots)
	if slot > last {
		// Should never happen given how EpochFromSlotDirect places `slot`;
		// the server-side equivalent asserts.
		return 0
	}
	return last - slot + 1
}

// FrozenSlotsFromFrozenEpochs returns the total number of slots a
// delegation output stays frozen if its freeze depth is
// `frozenEpochs` (≥ 1). It covers the rest of the current epoch
// (CoveredSlotsInCurrentEpoch) plus `frozenEpochs-1` full epochs.
// Mirrors ledger.Constants.FrozenSlotsFromFrozenEpochs.
func (c *Constants) FrozenSlotsFromFrozenEpochs(targetID base.ChainID, txSlot, epochSlots uint32, frozenEpochs byte) uint32 {
	if frozenEpochs == 0 {
		return 0
	}
	return c.CoveredSlotsInCurrentEpoch(targetID, txSlot, epochSlots) + uint32(frozenEpochs-1)*epochSlots
}

// ClockTime maps a base.LedgerTime back to a wall-clock time using
// the genesis Unix time + tick duration. Inverse of
// LedgerTimeFromClockTime. Mirrors ledger.ClockTime, which reaches
// into the singleton for the same two values.
func (c *Constants) ClockTime(t base.LedgerTime) time.Time {
	return c.GenesisTime().Add(time.Duration(t.TicksSinceGenesis()) * c.TickDuration)
}

// TicksPerYear returns the total tick count per year.
func (c *Constants) TicksPerYear() int {
	return c.SlotsPerYear() * base.TicksPerSlot
}

// DiffEpochs returns epoch(ts1) - epoch(ts2) on the target's delegation
// epoch grid (signed; ts1 < ts2 returns negative).
func (c *Constants) DiffEpochs(targetID base.ChainID, ts1, ts2 base.LedgerTime, epochSlots uint32) int {
	epoch1 := c.EpochFromSlotDirect(targetID, ts1.Slot, epochSlots)
	epoch2 := c.EpochFromSlotDirect(targetID, ts2.Slot, epochSlots)
	return int(epoch1) - int(epoch2)
}

// AdjustedAmount returns 'amount' adjusted to the maximum inflation,
// i.e. the value A such that A inflated to its maximum over 'slot' slots
// reaches 'amount'. When amount == totalSupply this gives the initialSupply.
// Wallet-side mirror of ledger.AdjustedAmount; computed purely from
// MinimumInflatableAmount0 so it is singleton-free.
func (c *Constants) AdjustedAmount(amount uint64, slot uint32) uint64 {
	return c.MinimumInflatableAmount0 * (amount / (c.MinimumInflatableAmount0 + uint64(slot)))
}

// AdjustFrozenCoverageVector shifts the predecessor's frozen-coverage
// vector forward by DiffEpochs(succTs, predTs) epochs and clamps the
// result to maxFrozenEpochs cells. Entries that fall off the front
// are dropped. Used by sequencer-side compose when carrying frozen
// coverage across an epoch boundary; semantics described in
// claude/delegation_epoch_params.md. Panics if succTs predates
// predTs.
func (c *Constants) AdjustFrozenCoverageVector(targetID base.ChainID, vect []int64, predTs, succTs base.LedgerTime, epochSlots uint32, maxFrozenEpochs byte) []int64 {
	shift := c.DiffEpochs(targetID, succTs, predTs, epochSlots)
	if shift < 0 {
		panic(fmt.Sprintf("AdjustFrozenCoverageVector: wrong order of timestamps %s and %s", predTs.String(), succTs.String()))
	}
	ret := make([]int64, maxFrozenEpochs)
	if shift >= int(maxFrozenEpochs) {
		return ret
	}
	for i, v := range vect[shift:] {
		ret[i] = v
	}
	return ret
}
