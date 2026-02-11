package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
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

func ConstantsYAMLFromParamsUpgrade0(par InitParameters) []byte {
	return []byte(fmt.Sprintf(__definitionsLedgerConstantsYAMLUpgrade0,
		hex.EncodeToString(par.GenesisControllerPublicKey),
		par.GenesisTimeUnix,
		uint64(par.TickDuration),
		base.MaxTickValue,
		base.MaxTickValue+1,
		par.TransactionPaceTicks,
		par.TransactionPaceSequencerTicks,
		par.AttachmentCostBudget,
		par.TxIDStateTTLSlots,
		hex.EncodeToString([]byte(par.Description)),
	))
}

// TODO better use template?

const __definitionsLedgerConstantsYAMLUpgrade0 = `# 
# version data is JSON data. The key 'txValidation' contains name of the constraint that should be evaluated first.
# It validates main parts of the transaction layout
version_data: '{"txIntegrityValidatorPartialContext":"txIntegrityValidatorPartialContext0", "txIntegrityValidatorFullContext":"txIntegrityValidatorFullContext0"}'

# definitions of main ledger constants
functions:
   -
      sym: constInitialSupply
      description: Initial number of tokens in the ledger
      source: u64/1000000000000000
      immutable: true
   -
      sym: constGenesisControllerPublicKey
      description: Public key ED25519 of the genesis controller in hexadecimal format
      source: 0x%s
      immutable: true
   -
      sym: constGenesisTimeUnix
      description: Unix time in seconds when ledger was initiated. Timestamp 0|0 corresponds to the genesis time 
      source: u64/%d
      immutable: true
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
      source: u64/%d
   -
      sym: constSlotInflationBase
      description: maximum inflation of the total initial supply in slot 0 
      source: u64/33000000
   -
      sym: constBranchInflationBonusBase
      description: maximum value of the branch inflation bonus
      source: u64/5000000
   -
      sym: constMinimumAmountOnSequencer
      description: minimum amount of tokens on the sequencer output. For testnet it is 1000 * PRXI = 1000000000
      source: u64/1000000000
   -
      sym: constMaxNumberOfEndorsements
      description: up to 8 endorsements
      source: u64/8
   -
      sym: constPreBranchConsolidationTicks
      description: number of last ticks in a slot when sequencer transaction cannot consume more than 2 UTXOs
      source: u64/25
   -
      sym: constPostBranchConsolidationTicks
      description: number of first ticks in the timestamp of the sequencer transaction
      source: u64/12
   -
      sym: constTransactionPace
      description: minimum number of ticks between non-sequencer transaction and its inputs  
      source: u64/%d
   -
      sym: constTransactionPaceSequencer
      description: minimum number of ticks between sequencer transaction and its inputs and endorsed transactions
      source: u64/%d
   -
      sym: constAttachmentCostBudget
      description: maximum total attachment cost (pastCone + seqTx) for sequencer transaction validation
      source: u64/%d
   -
      sym: constTxIDStateTTLSlots
      description: number of slots to keep committed transaction IDs in the state before GC
      source: u64/%d
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
   - 
      sym: minimumInflatableAmount0
      description: minimum amount which gives non-zero inflation in the slot 0
      source: div(constInitialSupply, constSlotInflationBase)
   - 
      sym: chainInflationMultiStep
      description: calculates chain inflation generated  in $2 steps by amount $0 starting at slot $1
      source: mul($2,chainInflationOneSlot($0,$1))
   - 
      sym: chainInflationOneSlot
      description: calculates one-slot inflation in slot $0 of amount $1
      source: div($0,add(minimumInflatableAmount0,$1))
   - 
      sym: branchInflationBonus
      description: calculates pseudo-random yet deterministic value of branch inflation bonus based on VRF suppled as $0
      source: add(randomFromSeed($0, constBranchInflationBonusBase), u64/1)
   -  
      sym: storageDeposit
      description: returns storage deposit for the UTXO with byte size of $0
      source: if( lessThan($0, u64/303030), mul($0, 100), add(mul($0, 100), lshift64(sub($0,100), 2)))
`
