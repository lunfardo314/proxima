package transaction

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txcore"
	"github.com/lunfardo314/proxima/util"
)

// Size limits for transaction elements.
// These are enforced during parsing and scanning to prevent oversized transactions
// from consuming resources. They complement the network-level limits (P2P: 65,531 bytes,
// API: 65,536 bytes) by providing validation-level enforcement.
const (
	MaxTransactionSize  = 65536 // 64KB, matches network/API limits
	MaxOutputSize       = 8192  // 8KB per individual produced output
	MaxUnlockParamsSize = 1024  // 1KB per input's unlock params block
)

// Transaction provides access to the tuple tree of transferable transaction data
type Transaction struct {
	*ledger.Library           // cached library for this transaction's slot
	*tuples.Tree              // the tuple tree with full or partial context. i.e. augmented with one level more for consumed UTXOs
	txid                      base.TransactionID
	timestamp                 base.LedgerTime
	producedAmountTotals      []int64 // calculated by summing up amount vectors
	totalConsumedTokenBalance int64
	sequencerTransactionData  *ledger.SequencerTransactionData // if != nil it is sequencer milestone transaction
	partialContextValidated   bool
	fullContextValidated      bool
	// fullContextOnce serialises SetFullContext so concurrent attachers can't race
	// on the "set only once" invariant. The first caller runs the setup; others wait
	// on Do and then see the already-populated tree (and the first call's error, if any).
	fullContextOnce sync.Once
	fullContextErr  error
	// redeemedScripts is the per-tx commitment list of local-script hashes
	// committed by redeemScript constraints. nil until first AddRedeemedScript;
	// linear scan is fine because typical txs have 0 entries (lazy alloc on
	// first use) and feature-using txs have 1-2.
	redeemedScripts [][32]byte
	// nativeTokenAggregator is the per-tx per-tag aggregator populated
	// lazily on the first token() builtin call. nil until first
	// NativeTokenAggregator() invocation.
	nativeTokenAggregator *ledger.NativeTokenAggregator
}

// ParseLibraryAgnostic parses tx bytes into a *Transaction without
// touching the ledger library cache (no `ledger.L(slot)` call, no
// TxVersion-vs-UpgradeIndex check, no produced-amount allocation). The
// returned object has Library == nil and producedAmountTotals == nil.
//
// Library-free accessors are safe to call: ID, Timestamp,
// IsBranchTransaction, IsSequencerTransaction, NumInputs, MustInputAt,
// NumEndorsements, MustEndorsementAt, ExplicitBaseline, Signature.
// Anything that runs EasyFL constraints, decodes outputs, or relies on
// `tx.Library` will panic.
//
// Used by tools that need to walk transaction structure (txid graph,
// past-cone audit, dump) before — or independently of — the multistate
// DB being initialised.
func ParseLibraryAgnostic(txBytes []byte) (*Transaction, error) {
	if len(txBytes) > MaxTransactionSize {
		return nil, fmt.Errorf("tx.Parse: transaction size %d exceeds maximum %d bytes", len(txBytes), MaxTransactionSize)
	}
	txTree, err := tuples.TreeFromBytesReadOnly(txBytes)
	if err != nil {
		return nil, fmt.Errorf("tx.Parse: %v", err)
	}
	if txTree.NumElements() != int(ledger.TxTreeTupleNumElements) {
		return nil, fmt.Errorf("tx.Parse: expected %d elements in the top tuple, got %d", ledger.TxTreeTupleNumElements, txTree.NumElements())
	}
	ret := &Transaction{
		// index 0 for transaction, index 1 for consumed outputs
		Tree: tuples.TreeFromTreesReadOnly(txTree, tuples.MakeTupleFromDataElements(nil).AsTree()),
	}
	// partial context: dummy nil data instead of the tuple of consumed UTXOs
	// create partial context with dummy consumed UTXOs
	ret.txid, err = TxIDFromTransactionDataTree(txTree)
	if err != nil {
		return nil, fmt.Errorf("tx.Parse: %v", err)
	}
	ret.timestamp = ret.txid.Timestamp()
	return ret, nil
}

// Parse parses main elements of the transaction and creates Transaction ID and transaction structure:
// This is STAGE 1 of transaction validation. This is minimal check to pass for the blob to be a raw transaction.
// If it is impossible to extract txid from the blob, it is not a transaction
func Parse(txBytes []byte) (*Transaction, error) {
	ret, err := ParseLibraryAgnostic(txBytes)
	if err != nil {
		return nil, err
	}

	// Cache the library for this transaction's slot once, to avoid repeated L(slot) calls
	ret.Library = ledger.L(ret.timestamp.Slot)

	// Validate TxVersion: must match the library's upgrade index for this transaction's slot
	versionBytes, err := ret.Tree.BytesAtPath(ledger.PathToTxVersion)
	if err != nil {
		return nil, fmt.Errorf("tx.Parse: can't read TxVersion: %v", err)
	}
	if len(versionBytes) != 2 {
		return nil, fmt.Errorf("tx.Parse: TxVersion must be exactly 2 bytes, got %d", len(versionBytes))
	}
	txVersion := binary.BigEndian.Uint16(versionBytes)
	if txVersion != ret.Library.UpgradeIndex() {
		return nil, fmt.Errorf("tx.Parse: TxVersion mismatch: transaction has %d, library expects %d", txVersion, ret.Library.UpgradeIndex())
	}

	// producedAmountTotals sums the amounts vectors of all produced
	// outputs. After Phase 4 of delegation_epoch_params, each
	// delegation can target a chain with its own maxFrozenEpochs (up to
	// DelegationMaxFrozenEpochsMax). Size to that upper bound so any
	// legitimate delegation in the tx fits.
	ret.producedAmountTotals = make([]int64, ret.Library.DelegationMaxFrozenEpochsMax+uint32(ledger.AmountIndexFrozenCoverage))
	return ret, nil
}

// ParseWithPartialValidation parses transaction and runs validation with the partial context
// This is STAGE 1 and 2 of the transaction validation. It does not require availability of the past cone
func ParseWithPartialValidation(txBytes []byte) (*Transaction, error) {
	tx, err := Parse(txBytes)
	if err != nil {
		return nil, err
	}
	return tx, tx.ValidatePartialContext(true)
}

// TxIDFromTransactionDataTree is a thin compatibility shim that delegates
// to txcore.TxIDFromTree (the canonical, wallet-shared implementation).
// Server-side callers (Parse) use this so the byte-level txid math lives
// in exactly one place.
func TxIDFromTransactionDataTree(txTree *tuples.Tree) (base.TransactionID, error) {
	return txcore.TxIDFromTree(txTree)
}

func IDAndTimestampFromParsedTransactionBytes(txBytes []byte) (base.TransactionID, base.LedgerTime, error) {
	tx, err := Parse(txBytes)
	if err != nil {
		return base.TransactionID{}, base.LedgerTime{}, err
	}
	return tx.ID(), tx.Timestamp(), nil
}

func IDFromParsedTransactionBytes(txBytes []byte) (base.TransactionID, error) {
	tx, err := Parse(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	return tx.ID(), nil
}


func (tx *Transaction) scanPartialContext() (err error) {
	if err = tx.parseSequencerData(); err != nil {
		return err
	}
	if err = tx.scanInputs(); err != nil {
		return err
	}
	if err = tx.scanEndorsements(); err != nil {
		return err
	}
	if err = tx.scanProducedOutputs(); err != nil {
		return err
	}
	return nil
}

// parseSequencerData parses and caches sequencer data if relevant
func (tx *Transaction) parseSequencerData() error {
	if !tx.txid.IsSequencerTransaction() {
		// it is known from parsing the txID
		return nil
	}
	// already checked in tx.Parse()
	seqDataBytes := ledger.MustSequencerDataBytesFromBytes(tx.MustBytesAtPath(ledger.PathToSequencerDataBytes))

	// check sequencer output
	if int(seqDataBytes.SequencerOutputIndex) >= tx.NumProducedOutputs() {
		return fmt.Errorf("parseSequencerData: wrong sequencer output index")
	}
	out, err := tx.ProducedOutputWithIDAt(seqDataBytes.SequencerOutputIndex)
	if err != nil {
		return fmt.Errorf("parseSequencerData: '%v' at produced output #%d", err, seqDataBytes.SequencerOutputIndex)
	}
	seqOutputData, valid := out.Output.SequencerOutputData()
	if !valid {
		return fmt.Errorf("parseSequencerData: invalid sequencer output data in %s", out.String())
	}

	var sequencerID base.ChainID
	if seqOutputData.ChainConstraint.IsOrigin() {
		sequencerID = base.MakeOriginChainID(out.ID)
	} else {
		sequencerID = seqOutputData.ChainConstraint.ChainID
	}

	// it is a sequencer milestone transaction
	tx.sequencerTransactionData = &ledger.SequencerTransactionData{
		SequencerOutputData: seqOutputData,
		SequencerID:         sequencerID,
		SequencerDataBytes:  seqDataBytes,
		StemOutputData:      nil,
	}

	// ---  check stem output data
	if tx.timestamp.Tick != 0 {
		// not a branch transaction
		return nil
	}
	outStem, err := tx.ProducedOutputWithIDAt(seqDataBytes.StemOutputIndex)
	if err != nil {
		return fmt.Errorf("ParseSequencerData stem: %v", err)
	}
	lock := outStem.Output.Lock()
	var ok bool
	if tx.sequencerTransactionData.StemOutputData, ok = lock.(*ledger.StemLock); !ok {
		return fmt.Errorf("parseSequencerData: not a stem lock in %s", outStem.String())
	}
	return nil
}

// scanInputs validation option scans all inputs:
// - validates UTXO IDs
// - enforces pace constraints
// - enforces unlock params size limit
func (tx *Transaction) scanInputs() error {
	numInputs, err := tx.NumElementsAtPath(ledger.PathToInputIDs)
	if err != nil {
		return fmt.Errorf("scanInputs: '%v'", err)
	}
	var oid base.OutputID

	ts := tx.Timestamp()
	isSequencer := tx.IsSequencerTransaction()
	pathInput := easyfl_util.Concat(ledger.PathToInputIDs, 0)
	pathUnlock := easyfl_util.Concat(ledger.PathToUnlockParams, 0)

	// we do not use ForEachInputID because it assumes all inputs valid

	for i := 0; i < numInputs; i++ {
		pathInput[len(ledger.PathToInputIDs)] = byte(i)
		// parse output ChainID
		oid, err = base.OutputIDFromBytes(tx.MustBytesAtPath(pathInput))
		if err != nil {
			return fmt.Errorf("parsing input #%d: '%v'", i, err)
		}
		// pace constraint applies only to consumed inputs.
		// Two cases: sequencer consumer (incl. branch) → TransactionPaceSequencer;
		// non-sequencer consumer → TransactionPace.
		if isSequencer {
			if !ledger.ValidSequencerPace(oid.Timestamp(), ts) {
				return fmt.Errorf("input #%d violates sequencer time pace constraint: %s", i, oid.StringShort())
			}
		} else {
			if !ledger.ValidTransactionPace(oid.Timestamp(), ts) {
				return fmt.Errorf("input #%d violates transaction time pace constraint: %s", i, oid.StringShort())
			}
		}
		// check unlock params size limit
		pathUnlock[len(ledger.PathToUnlockParams)] = byte(i)
		unlockBytes := tx.MustBytesAtPath(pathUnlock)
		if len(unlockBytes) > MaxUnlockParamsSize {
			return fmt.Errorf("scanInputs: unlock params #%d size %d exceeds maximum %d bytes", i, len(unlockBytes), MaxUnlockParamsSize)
		}
	}
	return nil
}

// scanEndorsements
// - parses and checks validity of each endorsement
// - enforces no cross-slot endorsements
// - enforces strict monotonicity (≥1 tick) between endorsement and endorsing tx;
//   no ledger pace constant applies to endorsements
func (tx *Transaction) scanEndorsements() error {
	numEndorsements, err := tx.NumElementsAtPath(ledger.PathToEndorsements)
	if err != nil {
		return fmt.Errorf("scanEndorsements: '%v'", err)
	}
	if numEndorsements == 0 {
		return nil
	}
	txTs := tx.Timestamp()

	var endorsementID base.TransactionID
	path := easyfl_util.Concat(ledger.PathToEndorsements, 0)
	mutateIdx := len(ledger.PathToEndorsements)
	for i := 0; i < numEndorsements; i++ {
		path[mutateIdx] = byte(i)
		// parse transaction ChainID
		endorsementID, err = base.TransactionIDFromBytes(tx.MustBytesAtPath(path))
		if err != nil {
			return fmt.Errorf("scanEndorsements: parsing endorsement #%d: '%v'", i, err)
		}
		// check cross-slot endorsements
		if txTs.Slot != endorsementID.Slot() {
			return fmt.Errorf("scanEndorsements: cross-slot endorsements are not allowed:  %s ->  %s",
				tx.IDShortString(), endorsementID.StringShort())
		}
		// strict monotonicity: endorsement must be strictly earlier than endorsing tx
		if base.DiffTicks(txTs, endorsementID.Timestamp()) < 1 {
			return fmt.Errorf("scanEndorsements: endorsement #%d violates strict monotonicity: %s -> %s",
				i, txTs.String(), endorsementID.StringShort())
		}
	}
	return nil
}

// ScanOutputs
// - scans all outputs
// - enforces the existence of the mandatory constrains,
// - sums up total of outputs and total inflation
func (tx *Transaction) scanProducedOutputs() error {
	numOutputs, err := tx.NumElementsAtPath(ledger.PathToProducedOutputs)
	if err != nil {
		return fmt.Errorf("scanProducedOutputs: '%v'", err)
	}
	var amounts ledger.Amounts

	pathToOutput := easyfl_util.Concat(ledger.PathToProducedOutputs, 0)
	pathToAmounts := easyfl_util.Concat(ledger.PathToProducedOutputs, 0, ledger.ConstraintIndexAmounts)
	pathToIndexValues := easyfl_util.Concat(ledger.PathToProducedOutputs, 0, ledger.ConstraintIndexIndexValues)
	pathToLock := easyfl_util.Concat(ledger.PathToProducedOutputs, 0, ledger.ConstraintIndexLock)

	for i := 0; i < numOutputs; i++ {
		pathToOutput[len(ledger.PathToProducedOutputs)] = byte(i)
		pathToAmounts[len(ledger.PathToProducedOutputs)] = byte(i)
		pathToIndexValues[len(ledger.PathToProducedOutputs)] = byte(i)
		pathToLock[len(ledger.PathToProducedOutputs)] = byte(i)

		// check per-output size limit
		outBytes := tx.MustBytesAtPath(pathToOutput)
		if len(outBytes) > MaxOutputSize {
			return fmt.Errorf("scanProducedOutputs: output #%d size %d exceeds maximum %d bytes", i, len(outBytes), MaxOutputSize)
		}

		amounts, err = ledger.AmountsFromBytes(tx.MustBytesAtPath(pathToAmounts))
		if err != nil {
			return fmt.Errorf("scanProducedOutputs: UTXO #%d: '%v'", i, err)
		}

		// enforce that the lock at output element index 2 is parseable
		// from the index-value tuple (index 1) + lock bytecode (index 2).
		if _, err = ledger.LockFromOutputElementsWithLib(
			tx.MustBytesAtPath(pathToIndexValues),
			tx.MustBytesAtPath(pathToLock),
			tx.Library); err != nil {
			return fmt.Errorf("scanProducedOutputs: UTXO #%d: '%v'", i, err)
		}
		if overflow := amounts.AddToVector(tx.producedAmountTotals); overflow {
			return fmt.Errorf("scanProducedOutputs: UTXO #%d: 'arithmetic overflow while calculating total of outputs'", i)
		}
	}
	if tx.producedAmountTotals[0] <= 0 {
		return fmt.Errorf("scanProducedOutputs:total produced amount must be positive, got %s", util.Th(tx.producedAmountTotals[0]))
	}
	return nil
}
