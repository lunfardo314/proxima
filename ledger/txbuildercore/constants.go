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
	// Initial supply at genesis.
	InitialSupply uint64
	// Inflation-related.
	SlotInflationBase        uint64
	MinimumInflatableAmount0 uint64
	// Pace constants.
	TransactionPace          byte
	TransactionPaceSequencer byte
	// Endorsement count cap.
	MaxNumberOfEndorsements uint64
	// PreBranchConsolidationTicks enforces endorsement-only constraint
	// for that many ticks before the slot boundary.
	PreBranchConsolidationTicks byte
	// Delegation parameters (defaults + bounds; per-chain values live
	// inside each chain's delegationParams constraint).
	SafeRevocationSlots          uint32
	DelegationEpochSlots         uint32
	MaxFrozenEpochs              uint32
	DelegationEpochSlotsMin      uint32
	DelegationEpochSlotsMax      uint32
	DelegationMaxFrozenEpochsMin uint32
	DelegationMaxFrozenEpochsMax uint32
	// Tag-along window.
	TagAlongSlots        uint32
	TagAlongReclaimSlots uint32
	// Attachment-cost ceiling for a tx + its non-rooted past cone.
	AttachmentCostBudget int
	// GC: TTL of committed transaction IDs in the state trie.
	TxIDStateTTLSlots uint32
	// Healthy-coverage fraction (numerator / denominator).
	HealthyCoverageNumerator   uint64
	HealthyCoverageDenominator uint64
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
	InitialSupply                uint64 `json:"initial_supply"`
	SlotInflationBase            uint64 `json:"slot_inflation_base"`
	MinimumInflatableAmount0     uint64 `json:"minimum_inflatable_amount_0"`
	TransactionPace              byte   `json:"transaction_pace"`
	TransactionPaceSequencer     byte   `json:"transaction_pace_sequencer"`
	MaxNumberOfEndorsements      uint64 `json:"max_number_of_endorsements"`
	PreBranchConsolidationTicks  byte   `json:"pre_branch_consolidation_ticks"`
	SafeRevocationSlots          uint32 `json:"safe_revocation_slots"`
	DelegationEpochSlots         uint32 `json:"delegation_epoch_slots"`
	MaxFrozenEpochs              uint32 `json:"max_frozen_epochs"`
	DelegationEpochSlotsMin      uint32 `json:"delegation_epoch_slots_min"`
	DelegationEpochSlotsMax      uint32 `json:"delegation_epoch_slots_max"`
	DelegationMaxFrozenEpochsMin uint32 `json:"delegation_max_frozen_epochs_min"`
	DelegationMaxFrozenEpochsMax uint32 `json:"delegation_max_frozen_epochs_max"`
	TagAlongSlots                uint32 `json:"tag_along_slots"`
	TagAlongReclaimSlots         uint32 `json:"tag_along_reclaim_slots"`
	AttachmentCostBudget         int    `json:"attachment_cost_budget"`
	TxIDStateTTLSlots            uint32 `json:"tx_id_state_ttl_slots"`
	HealthyCoverageNumerator     uint64 `json:"healthy_coverage_numerator"`
	HealthyCoverageDenominator   uint64 `json:"healthy_coverage_denominator"`
}

func (c *Constants) MarshalJSON() ([]byte, error) {
	return json.Marshal(constantsJSON{
		Hash:                         hex.EncodeToString(c.Hash[:]),
		Description:                  c.Description,
		GenesisControllerPublicKey:   hex.EncodeToString(c.GenesisControllerPublicKey),
		GenesisTimeUnix:              c.GenesisTimeUnix,
		TickDuration:                 int64(c.TickDuration),
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
	c.InitialSupply = raw.InitialSupply
	c.SlotInflationBase = raw.SlotInflationBase
	c.MinimumInflatableAmount0 = raw.MinimumInflatableAmount0
	c.TransactionPace = raw.TransactionPace
	c.TransactionPaceSequencer = raw.TransactionPaceSequencer
	c.MaxNumberOfEndorsements = raw.MaxNumberOfEndorsements
	c.PreBranchConsolidationTicks = raw.PreBranchConsolidationTicks
	c.SafeRevocationSlots = raw.SafeRevocationSlots
	c.DelegationEpochSlots = raw.DelegationEpochSlots
	c.MaxFrozenEpochs = raw.MaxFrozenEpochs
	c.DelegationEpochSlotsMin = raw.DelegationEpochSlotsMin
	c.DelegationEpochSlotsMax = raw.DelegationEpochSlotsMax
	c.DelegationMaxFrozenEpochsMin = raw.DelegationMaxFrozenEpochsMin
	c.DelegationMaxFrozenEpochsMax = raw.DelegationMaxFrozenEpochsMax
	c.TagAlongSlots = raw.TagAlongSlots
	c.TagAlongReclaimSlots = raw.TagAlongReclaimSlots
	c.AttachmentCostBudget = raw.AttachmentCostBudget
	c.TxIDStateTTLSlots = raw.TxIDStateTTLSlots
	c.HealthyCoverageNumerator = raw.HealthyCoverageNumerator
	c.HealthyCoverageDenominator = raw.HealthyCoverageDenominator
	return nil
}

// SlotDuration returns the wall-clock duration of one slot.
func (c *Constants) SlotDuration() time.Duration {
	return c.TickDuration * time.Duration(base.TicksPerSlot)
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
