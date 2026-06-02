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
	SetCoverageContributionBounds  bool   // true for testing only
	CoverageContributionLowerBound uint64 // 0 = default formula, >0 = constant bound (for testing)
	CoverageContributionUpperBound uint64 // 0 = default formula, >0 = constant bound (for testing)
	// Healthy-branch coverage fraction (numerator/denominator). 0/0 means "use default 7/12".
	// Tests with small synthetic coverage typically set HealthyCoverageNumerator=0 to relax
	// the on-chain healthiness check (matches the WithCoverageContributionBounds(0,0) pattern).
	HealthyCoverageNumerator   uint64
	HealthyCoverageDenominator uint64
}

// default ledger init parameters

const (
	defaultTickDuration  = 80 * time.Millisecond
	DefaultInitialSupply = GProxi

	defaultTransactionPace          = 12
	defaultTransactionPaceSequencer = 12
	defaultDescription              = "Proxima ledger definitions"

	defaultAttachmentCostBudget = 550  // > than max transaction with 256 inputs and 256 outputs
	defaultTxIDStateTTLSlots    = 8640 // 24 hours with 10 sec slots

	BaseTokenName       = "Proxi"
	BaseTokenNameTicker = "PRXI"
	DustTokenName       = "dust"
)

const (
	Proxi  = 1_000_000
	KProxi = 1_000 * Proxi
	MProxi = 1_000 * KProxi
	GProxi = 1_000 * MProxi
	TProxi = 1_000 * GProxi
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
		Description:                   dscr,
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
	DescriptionHex                 string
	SetCoverageContributionBounds  bool
	CoverageContributionLowerBound uint64 // 0 = use default formula
	CoverageContributionUpperBound uint64 // 0 = use default formula
	HealthyCoverageNumerator       uint64
	HealthyCoverageDenominator     uint64
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
		DescriptionHex:                 hex.EncodeToString([]byte(par.Description)),
		SetCoverageContributionBounds:  par.SetCoverageContributionBounds,
		CoverageContributionLowerBound: par.CoverageContributionLowerBound,
		CoverageContributionUpperBound: par.CoverageContributionUpperBound,
		HealthyCoverageNumerator:       num,
		HealthyCoverageDenominator:     den,
	}
	var buf bytes.Buffer
	if err := _constantsTemplate.Execute(&buf, data); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
