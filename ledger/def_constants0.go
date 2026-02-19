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
	Description                   string
	GenesisTimeUnix               uint32
	GenesisControllerPublicKey    ed25519.PublicKey
	TickDuration                  time.Duration
	TransactionPaceTicks          int
	TransactionPaceSequencerTicks int
	AttachmentCostBudget          int
	TxIDStateTTLSlots             int
}

// default ledger init parameters

const (
	defaultTickDuration = 80 * time.Millisecond

	DustPerProxi         = 1_000_000
	PRXI                 = DustPerProxi
	initialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = initialSupplyProxi * PRXI

	defaultTransactionPace          = 12
	defaultTransactionPaceSequencer = 2
	defaultDescription              = "Proxima ledger definitions"

	defaultAttachmentCostBudget = 550  // > than max transaction with 256 inputs and 256 outputs
	defaultTxIDStateTTLSlots    = 8640 // 24 hours with 10 sec slots
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

//go:embed def/def_constants0.yaml.template
var _definitionsLedgerConstantsTemplateUpgrade0 string

// constantsTemplateData holds the values injected into the YAML template
type constantsTemplateData struct {
	GenesisControllerPublicKeyHex string
	GenesisTimeUnix               uint32
	TickDurationNano              uint64
	MaxTickValue                  int
	TicksPerSlot                  int
	TransactionPaceTicks          int
	TransactionPaceSequencerTicks int
	AttachmentCostBudget          int
	TxIDStateTTLSlots             int
	DescriptionHex                string
}

var _constantsTemplate = template.Must(template.New("constants0").Parse(_definitionsLedgerConstantsTemplateUpgrade0))

func ConstantsYAMLFromParamsUpgrade0(par InitParameters) []byte {
	data := constantsTemplateData{
		GenesisControllerPublicKeyHex: hex.EncodeToString(par.GenesisControllerPublicKey),
		GenesisTimeUnix:               par.GenesisTimeUnix,
		TickDurationNano:              uint64(par.TickDuration),
		MaxTickValue:                  base.MaxTickValue,
		TicksPerSlot:                  base.MaxTickValue + 1,
		TransactionPaceTicks:          par.TransactionPaceTicks,
		TransactionPaceSequencerTicks: par.TransactionPaceSequencerTicks,
		AttachmentCostBudget:          par.AttachmentCostBudget,
		TxIDStateTTLSlots:             par.TxIDStateTTLSlots,
		DescriptionHex:                hex.EncodeToString([]byte(par.Description)),
	}
	var buf bytes.Buffer
	if err := _constantsTemplate.Execute(&buf, data); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
