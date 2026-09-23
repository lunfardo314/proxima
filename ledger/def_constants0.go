package ledger

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"text/template"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// InitParameters contains parameters which can be set as ledger constant values when creating genesis
type InitParameters struct {
	Description     string
	GenesisTimeUnix uint32
	// Signs Description and GenesisTimeUnix into constGenesisControllerSignature,
	// which is how the controller's key claims the resulting library hash.
	GenesisControllerPrivateKey    ed25519.PrivateKey
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
	// Fair-launch mine-chain policy. Configurable
	// like tick duration so tests can set a low difficulty and mine instantly.
	MineAmountBase      uint64 // A during the flat phase, i.e. up to MineRampStartSlot
	MineRampStartSlot   uint32 // slot at which A stops being flat and starts growing
	MineAmountPerSlot   uint64 // motes added to A per slot after MineRampStartSlot
	MineMinPace         int    // P: minimum pace in slots
	MineBaseDifficulty  int    // B0: seed difficulty on the genesis mine output
	MineFloorDifficulty int    // E: floor difficulty of the retarget band
	MineMaxDifficulty   int    // C: ceiling difficulty of the retarget band (must be < 64)
	MineHardenAfter     int    // full slots in a row after which the retarget hardens one bit
	MineRemainingInit   uint64 // R_init: initial remaining-mintable counter (ceiling T = InitialSupply + R_init)
	MineTagAlongFee     uint64 // the fixed tag-along fee of every mine transit, in motes
}

// default ledger init parameters

const (
	defaultTickDuration = 80 * time.Millisecond
	// DefaultTargetBaseSupply is the fair-launch supply ceiling T; genesis mints
	// six per cent of it, the rest is mined. Kept in
	// sync with constTargetBaseSupply / constInitialSupply in def_constants0.json.
	DefaultTargetBaseSupply = base.GPROX
	DefaultInitialSupply    = DefaultTargetBaseSupply * 6 / 100

	defaultTransactionPace          = 12
	defaultTransactionPaceSequencer = 3
	defaultDescription              = "Proxima ledger definitions"

	// Fair-launch mine-chain defaults (kb/take1_todo.md, the emission schedule).
	//
	// A is flat at DefaultMineAmountBase up to defaultMineRampStartSlot, then
	// grows by defaultMineAmountPerSlot per slot. The pace is one step per slot;
	// emission is A over the pace actually realized, which the asymmetric
	// retarget settles at about (k+1)/k slots for k = defaultMineHardenAfter, so
	// the schedule is sized at 1.12. The base is set so the flat phase, 60 days,
	// carries mined supply to the point where the genesis capital can no longer
	// commit healthy branches alone, and the slope so that R_init is exhausted at
	// ~443 days with A near 530 PROX by then.
	DefaultMineAmountBase    = 95 * base.PROX
	defaultMineRampStartSlot = 506_250 // 60 days at 10.24s per slot
	defaultMineAmountPerSlot = 134
	// P: one step per slot; a miner builds on the transit it learned from the
	// stream, not on an LRB-confirmed one.
	defaultMineMinPace = 1
	// B0 seeds the retarget; the band [E, C] is deliberately wide. E is low so a
	// genesis-era network of one or two machines can still be tracked down to a
	// workable difficulty; C leaves headroom for real hashrate growth and stays
	// under the 64-bit PoW wall. At the ceiling nothing breaks: the slots are all
	// full and the canonical winner rule decides.
	defaultMineBaseDifficulty  = 24
	defaultMineFloorDifficulty = 10
	defaultMineMaxDifficulty   = 56
	// harden one bit after this many full slots in a row, ease one bit per empty
	// slot: about one slot in nine empty at equilibrium
	defaultMineHardenAfter   = 8
	defaultMineRemainingInit = 940_000_000 * base.PROX // R_init = 9.4e14 motes (T = InitialSupply + R_init)
	defaultMineTagAlongFee   = 1 * base.PROX           // every transit pays the same fee

	defaultAttachmentCostBudget = 550 // > than max transaction with 256 inputs and 256 outputs
	// Non-branch txid records are needed only to detect a fully-consumed-in-delta ancestor while a
	// descendant is still being solidified — a window of the solidification/pull lag. Kept short:
	// this population is ~99.9% of state txid records. See kb/archive/shipped/txid_ttl_tiered.md.
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
		GenesisControllerPrivateKey:   privateKey,
		TickDuration:                  defaultTickDuration,
		TransactionPaceTicks:          defaultTransactionPace,
		TransactionPaceSequencerTicks: defaultTransactionPaceSequencer,
		AttachmentCostBudget:          defaultAttachmentCostBudget,
		TxIDStateTTLSlots:             defaultTxIDStateTTLSlots,
		BranchTxIDStateTTLSlots:       defaultBranchTxIDStateTTLSlots,
		Description:                   dscr,
		// per-milestone coverageDelta enforcement is ON by default
		EnforceCoverageDeltaMonotonicity: true,
		MineAmountBase:                   DefaultMineAmountBase,
		MineRampStartSlot:                defaultMineRampStartSlot,
		MineAmountPerSlot:                defaultMineAmountPerSlot,
		MineMinPace:                      defaultMineMinPace,
		MineBaseDifficulty:               defaultMineBaseDifficulty,
		MineFloorDifficulty:              defaultMineFloorDifficulty,
		MineMaxDifficulty:                defaultMineMaxDifficulty,
		MineHardenAfter:                  defaultMineHardenAfter,
		MineRemainingInit:                defaultMineRemainingInit,
		MineTagAlongFee:                  defaultMineTagAlongFee,
	}
}

//go:embed def/def_constants0.json
var _definitionsLedgerConstantsTemplateUpgrade0 string

// constantsTemplateData holds the values injected into the JSON template
type constantsTemplateData struct {
	GenesisControllerSignatureHex    string
	GenesisTimeUnix                  uint32
	TickDurationNano                 uint64
	MaxTickValue                     int
	TicksPerSlot                     int
	TransactionPaceTicks             int
	TransactionPaceSequencerTicks    int
	AttachmentCostBudget             int
	TxIDStateTTLSlots                int
	BranchTxIDStateTTLSlots          int
	DescriptionHex                   string
	SetCoverageContributionBounds    bool
	CoverageContributionLowerBound   uint64 // 0 = use default formula
	CoverageContributionUpperBound   uint64 // 0 = use default formula
	HealthyCoverageNumerator         uint64
	HealthyCoverageDenominator       uint64
	EnforceCoverageDeltaMonotonicity bool
	MineAmountBase                   uint64
	MineRampStartSlot                uint32
	MineAmountPerSlot                uint64
	MineMinPace                      int
	MineBaseDifficulty               int
	MineFloorDifficulty              int
	MineMaxDifficulty                int
	MineHardenAfter                  int
	MineRemainingInit                uint64
	MineTagAlongFee                  uint64
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
		GenesisControllerSignatureHex: hex.EncodeToString(base.SignatureDataED25519(par.GenesisControllerPrivateKey,
			txbuildercore.GenesisControllerSignedMessage(par.Description, par.GenesisTimeUnix))),
		GenesisTimeUnix:                  par.GenesisTimeUnix,
		TickDurationNano:                 uint64(par.TickDuration),
		MaxTickValue:                     base.MaxTickValue,
		TicksPerSlot:                     base.MaxTickValue + 1,
		TransactionPaceTicks:             par.TransactionPaceTicks,
		TransactionPaceSequencerTicks:    par.TransactionPaceSequencerTicks,
		AttachmentCostBudget:             par.AttachmentCostBudget,
		TxIDStateTTLSlots:                par.TxIDStateTTLSlots,
		BranchTxIDStateTTLSlots:          par.BranchTxIDStateTTLSlots,
		DescriptionHex:                   hex.EncodeToString([]byte(par.Description)),
		SetCoverageContributionBounds:    par.SetCoverageContributionBounds,
		CoverageContributionLowerBound:   par.CoverageContributionLowerBound,
		CoverageContributionUpperBound:   par.CoverageContributionUpperBound,
		HealthyCoverageNumerator:         num,
		HealthyCoverageDenominator:       den,
		EnforceCoverageDeltaMonotonicity: par.EnforceCoverageDeltaMonotonicity,
		MineAmountBase:                   par.MineAmountBase,
		MineRampStartSlot:                par.MineRampStartSlot,
		MineAmountPerSlot:                par.MineAmountPerSlot,
		MineMinPace:                      par.MineMinPace,
		MineBaseDifficulty:               par.MineBaseDifficulty,
		MineFloorDifficulty:              par.MineFloorDifficulty,
		MineMaxDifficulty:                par.MineMaxDifficulty,
		MineHardenAfter:                  par.MineHardenAfter,
		MineRemainingInit:                par.MineRemainingInit,
		MineTagAlongFee:                  par.MineTagAlongFee,
	}
	var buf bytes.Buffer
	if err := _constantsTemplate.Execute(&buf, data); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
