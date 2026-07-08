package ledger

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"text/template"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
)

// InitParameters contains parameters which can be set as ledger constant values when creating genesis
type InitParameters struct {
	Description                    string
	GenesisTimeUnix                uint32
	GenesisControllerPublicKey     ed25519.PublicKey
	TickDuration                   time.Duration
	TransactionPaceTicks           int
	TransactionPaceSequencerTicks  int
	AttachmentCostBudget           int
	TxIDStateTTLSlots              int
	BranchTxIDStateTTLSlots        int
	SetCoverageContributionBounds  bool   // true for testing only
	CoverageContributionLowerBound uint64 // 0 = default formula, >0 = constant bound (for testing)
	CoverageContributionUpperBound uint64 // 0 = default formula, >0 = constant bound (for testing)
	// Healthy-branch coverage fraction (numerator/denominator). 0/0 means "use default 7/12".
	// Tests with small synthetic coverage typically set HealthyCoverageNumerator=0 to relax
	// the on-chain healthiness check (matches the WithCoverageContributionBounds(0,0) pattern).
	HealthyCoverageNumerator   uint64
	HealthyCoverageDenominator uint64
	// EnforceCoverageDeltaMonotonicity gates the per-milestone coverageDelta
	// enforcement (on-chain within-slot strict-increase rule + attacher
	// computed-vs-declared cross-check). Production = true (set by
	// DefaultParameters). Certain attacher tests that hand-build milestones set
	// it false via WithEnforceCoverageDeltaMonotonicity(false).
	EnforceCoverageDeltaMonotonicity bool
	// Fair-launch mine-chain policy (see claude/fairlaunch.md). Configurable
	// like tick duration so tests can set a low difficulty and mine instantly.
	MineAmount          uint64 // A: motes minted per transit
	MineMinPace         int    // P: minimum pace in slots
	MineBaseDifficulty  int    // B: base/max difficulty (trailing zero bits at pace P)
	MineFloorDifficulty int    // E: floor difficulty (0 < E < B)
	MineRemainingInit   uint64 // R_init: initial remaining-mintable counter (ceiling T = InitialSupply + R_init)
}

// default ledger init parameters

const (
	defaultTickDuration = 80 * time.Millisecond
	// DefaultTargetBaseSupply is the fair-launch supply ceiling T; genesis mints
	// one tenth of it, the rest is mined (see claude/fairlaunch.md). Kept in
	// sync with constTargetBaseSupply / constInitialSupply in def_constants0.json.
	DefaultTargetBaseSupply = base.GPROX
	DefaultInitialSupply    = DefaultTargetBaseSupply / 10

	defaultTransactionPace          = 12
	defaultTransactionPaceSequencer = 12
	defaultDescription              = "Proxima ledger definitions"

	// Fair-launch mine-chain defaults (see claude/fairlaunch.md §1).
	DefaultMineAmount          = 500 * base.PROX       // A = 500 PROX
	defaultMineMinPace         = 1                     // P
	defaultMineBaseDifficulty  = 24                    // B (testnet)
	defaultMineFloorDifficulty = 22                    // E (testnet)
	defaultMineRemainingInit   = 900_000_000 * base.PROX // R_init = 9e14 motes (T = InitialSupply + R_init)

	defaultAttachmentCostBudget = 550 // > than max transaction with 256 inputs and 256 outputs
	// Non-branch txid records are needed only to detect a fully-consumed-in-delta ancestor while a
	// descendant is still being solidified — a window of the solidification/pull lag. Kept short:
	// this population is ~99.9% of state txid records. See claude/txid_ttl_tiered.md.
	defaultTxIDStateTTLSlots = 60
	// Branch txid records are read by LRB detection, baseline-branch resolution and the sync path;
	// their horizon is the deepest fork/partition across which a common committed ancestor must
	// still be identifiable. Branches are rare (~1 per sequencer per slot) so keeping them long is
	// cheap. The sync/too-old horizon is half of this.
	defaultBranchTxIDStateTTLSlots = 17480 // = 8740 * 2
)


func DefaultParameters(privateKey ed25519.PrivateKey, genesisTimeUnix uint32, description ...string) InitParameters {
	dscr := defaultDescription
	if len(description) > 0 {
		dscr = description[0]
	}
	return InitParameters{
		GenesisTimeUnix:               genesisTimeUnix,
		GenesisControllerPublicKey:    privateKey.Public().(ed25519.PublicKey),
		TickDuration:                  defaultTickDuration,
		TransactionPaceTicks:          defaultTransactionPace,
		TransactionPaceSequencerTicks: defaultTransactionPaceSequencer,
		AttachmentCostBudget:          defaultAttachmentCostBudget,
		TxIDStateTTLSlots:             defaultTxIDStateTTLSlots,
		BranchTxIDStateTTLSlots:       defaultBranchTxIDStateTTLSlots,
		Description:                   dscr,
		// per-milestone coverageDelta enforcement is ON by default
		EnforceCoverageDeltaMonotonicity: true,
		MineAmount:                       DefaultMineAmount,
		MineMinPace:                      defaultMineMinPace,
		MineBaseDifficulty:               defaultMineBaseDifficulty,
		MineFloorDifficulty:              defaultMineFloorDifficulty,
		MineRemainingInit:                defaultMineRemainingInit,
	}
}

//go:embed def/def_constants0.json
var _definitionsLedgerConstantsTemplateUpgrade0 string

// constantsTemplateData holds the values injected into the JSON template
type constantsTemplateData struct {
	GenesisControllerPublicKeyHex  string
	GenesisTimeUnix                uint32
	TickDurationNano               uint64
	MaxTickValue                   int
	TicksPerSlot                   int
	TransactionPaceTicks           int
	TransactionPaceSequencerTicks  int
	AttachmentCostBudget           int
	TxIDStateTTLSlots              int
	BranchTxIDStateTTLSlots        int
	DescriptionHex                 string
	SetCoverageContributionBounds  bool
	CoverageContributionLowerBound uint64 // 0 = use default formula
	CoverageContributionUpperBound uint64 // 0 = use default formula
	HealthyCoverageNumerator         uint64
	HealthyCoverageDenominator       uint64
	EnforceCoverageDeltaMonotonicity bool
	MineAmount                       uint64
	MineMinPace                      int
	MineBaseDifficulty               int
	MineFloorDifficulty              int
	MineRemainingInit                uint64
}

var _constantsTemplate = template.Must(template.New("constants0").Parse(_definitionsLedgerConstantsTemplateUpgrade0))

// DefaultHealthyCoverageNumerator and DefaultHealthyCoverageDenominator define
// the production healthy-branch fraction (7/12). Used when InitParameters
// leaves the values at zero (test code can override).
const (
	DefaultHealthyCoverageNumerator   = 7
	DefaultHealthyCoverageDenominator = 12
)

func ConstantsJSONFromParamsUpgrade0(par InitParameters) []byte {
	num, den := par.HealthyCoverageNumerator, par.HealthyCoverageDenominator
	if num == 0 && den == 0 {
		num, den = DefaultHealthyCoverageNumerator, DefaultHealthyCoverageDenominator
	}
	data := constantsTemplateData{
		GenesisControllerPublicKeyHex:  hex.EncodeToString(par.GenesisControllerPublicKey),
		GenesisTimeUnix:                par.GenesisTimeUnix,
		TickDurationNano:               uint64(par.TickDuration),
		MaxTickValue:                   base.MaxTickValue,
		TicksPerSlot:                   base.MaxTickValue + 1,
		TransactionPaceTicks:           par.TransactionPaceTicks,
		TransactionPaceSequencerTicks:  par.TransactionPaceSequencerTicks,
		AttachmentCostBudget:           par.AttachmentCostBudget,
		TxIDStateTTLSlots:              par.TxIDStateTTLSlots,
		BranchTxIDStateTTLSlots:        par.BranchTxIDStateTTLSlots,
		DescriptionHex:                 hex.EncodeToString([]byte(par.Description)),
		SetCoverageContributionBounds:  par.SetCoverageContributionBounds,
		CoverageContributionLowerBound: par.CoverageContributionLowerBound,
		CoverageContributionUpperBound: par.CoverageContributionUpperBound,
		HealthyCoverageNumerator:         num,
		HealthyCoverageDenominator:       den,
		EnforceCoverageDeltaMonotonicity: par.EnforceCoverageDeltaMonotonicity,
		MineAmount:                       par.MineAmount,
		MineMinPace:                      par.MineMinPace,
		MineBaseDifficulty:               par.MineBaseDifficulty,
		MineFloorDifficulty:              par.MineFloorDifficulty,
		MineRemainingInit:                par.MineRemainingInit,
	}
	var buf bytes.Buffer
	if err := _constantsTemplate.Execute(&buf, data); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
