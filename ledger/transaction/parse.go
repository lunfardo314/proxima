package transaction

import (
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// Transaction provides access to the tree of transferable transaction
type (
	Transaction struct {
		*ledger.Library           // cached library for this transaction's slot
		*tuples.Tree              // the tuple tree with full or skeleton context. i.e. augmented with one level more for consumed UTXOs
		txid                      base.TransactionID
		timestamp                 base.LedgerTime
		producedAmountTotals      [15]int64 // calculated by summing up amount vectors
		totalConsumedTokenBalance int64
		sequencerTransactionData  *ledger.SequencerTransactionData // if != nil it is sequencer milestone transaction
		traceOption               int
		partialContextValidated   bool
		fullContextValidated      bool
	}

	TxOption func(tx *Transaction) error
)

// Parse parses main elements of the transaction and creates Transaction structure
func Parse(txBytes []byte) (*Transaction, error) {
	txTree, err := tuples.TreeFromBytesReadOnly(txBytes)
	if err != nil {
		return nil, fmt.Errorf("tx.Parse: %v", err)
	}
	// dummy empty tuple of consumed UTXOs for the skeleton context
	ret := &Transaction{traceOption: TraceOptionNone}
	e := tuples.MakeTupleFromSerializableElements(tuples.EmptyTupleEditable())
	// create skeleton context with dummy consumed UTXOs
	ret.Tree = tuples.TreeFromTreesReadOnly(txTree, e.AsTree()) // index 0 for transaction, index 1 for consumed outputs
	// precalculate txid
	ret.txid, err = TxIDFromTransactionDataTree(txTree)
	if err != nil {
		return nil, fmt.Errorf("tx.Parse: %v", err)
	}
	ret.timestamp = ret.txid.Timestamp()
	// Cache the library for this transaction's slot once, to avoid repeated L(slot) calls
	ret.Library = ledger.L(ret.timestamp.Slot)
	return ret, nil
}

func ParseWithPartialValidation(txBytes []byte) (*Transaction, error) {
	tx, err := Parse(txBytes)
	if err != nil {
		return nil, err
	}
	return tx, tx.ValidatePartialContext()
}

// TxIDFromTransactionDataTree takes raw tx bytes and validates timestamp, sequencer data bytes and makes transaction ID
// This is minimal check to pass for the blob to be a raw transaction.
// If it is impossible to extract txid from the blob, it is not a transaction
func TxIDFromTransactionDataTree(txTree *tuples.Tree) (ret base.TransactionID, err error) {
	var tsBin []byte
	if tsBin, err = txTree.BytesAtPath([]byte{ledger.TxTimestamp}); err != nil {
		err = fmt.Errorf("can't parse timestamp: %w", err)
		return
	}
	if _, err = base.LedgerTimeFromBytes(tsBin); err != nil {
		err = fmt.Errorf("wrong timestamp: %w", err)
		return
	}
	var seqBin []byte
	seqBin, err = txTree.BytesAtPath([]byte{ledger.TxSequencerDataBytes})
	if err != nil {
		err = fmt.Errorf("can't get sequencer data bytes: %w", err)
		return
	}
	seqDataBytes, err := ledger.SequencerDataBytesFromBytes(seqBin)
	if err != nil {
		err = fmt.Errorf("can't parse sequencer data bytes: %w", err)
	}

	isSeqTx := seqDataBytes != nil // is it a sequencer transaction
	if ret, err = hashEssenceBytesFromTransactionDataTree(txTree); err != nil {
		return
	}
	// replace first 5 bytes with transaction ID prefix and set the sequencer tx flag
	copy(ret[:], tsBin)
	if isSeqTx {
		ret[base.TickByteIndex] |= base.SequencerBitMaskInTick
	}
	// set the number of produced outputs byte
	nUTXO, err := txTree.NumElementsAtPath([]byte{ledger.TxOutputs})
	if err != nil {
		return
	}
	if nUTXO == 0 || nUTXO > 256 {
		err = fmt.Errorf("wrong number of produced outputs")
		return
	}
	ret[base.LedgerTimeByteLength] = byte(nUTXO - 1)
	util.Assertf(len(seqBin) > 0 || !ret.IsSequencerTransaction(), "len(seqBin)>0||!ret.IsSequencerTransaction()")
	return
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

func (tx *Transaction) SetTraceOption(opt int) {
	tx.traceOption = opt
}

//func (tx *Transaction) Validate(opt ...TxOption) error {
//	return util.CatchPanicOrError(func() error {
//		if err := tx.Validate(); err != nil {
//			return err
//		}
//		for _, fun := range opt {
//			if err := fun(tx); err != nil {
//				return err
//			}
//		}
//		return nil
//	})
//}

func CheckTimestampUpperBound(upperBound time.Time) TxOption {
	return func(tx *Transaction) error {
		ts := ledger.ClockTime(tx.timestamp)
		if ts.After(upperBound) {
			return fmt.Errorf("transaction is %d msec too far in the future", int64(ts.Sub(upperBound))/int64(time.Millisecond))
		}
		return nil
	}
}

// tx essence is concatenation of all top level elements except signature
var _essenceIndices []byte

func init() {
	_essenceIndices = make([]byte, 20)
	for i := byte(0); i < ledger.TxTreeTupleNumElements; i++ {
		if i != ledger.TxSignatureData {
			_essenceIndices = append(_essenceIndices, i)
		}
	}
}

func hashEssenceBytesFromTransactionDataTree(txTree *tuples.Tree) (ret [32]byte, err error) {
	hasher, err := blake2b.New256(nil)
	util.AssertNoError(err)

	var d []byte
	for _, i := range _essenceIndices {
		d, err = txTree.BytesAtPath([]byte{i})
		if err != nil {
			return [32]byte{}, err
		}
		hasher.Write(d)
	}
	copy(ret[:], hasher.Sum(nil))
	return
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
func (tx *Transaction) scanInputs() error {
	numInputs, err := tx.NumElementsAtPath(ledger.PathToInputIDs)
	if err != nil {
		return fmt.Errorf("scanInputs: '%v'", err)
	}
	var oid base.OutputID

	ts := tx.Timestamp()
	isSequencer := tx.IsSequencerTransaction()
	path := easyfl_util.Concat(ledger.PathToInputIDs, 0)

	// we do not use ForEachInputID because it assumes all inputs valid

	for i := 0; i < numInputs; i++ {
		path[len(ledger.PathToInputIDs)] = byte(i)
		// parse output ChainID
		oid, err = base.OutputIDFromBytes(tx.MustBytesAtPath(path))
		if err != nil {
			return fmt.Errorf("parsing input #%d: '%v'", i, err)
		}
		// check time pace constraint
		if isSequencer {
			if !ledger.ValidSequencerPace(oid.Timestamp(), ts) {
				return fmt.Errorf("input #%d violates sequencer time pace constraint: %s", i, oid.StringShort())
			}
		} else {
			if !ledger.ValidTransactionPace(oid.Timestamp(), ts) {
				return fmt.Errorf("input #%d violates transaction time pace constraint: %s", i, oid.StringShort())
			}
		}
	}
	return nil
}

// scanEndorsements
// - parses and checks validity of each endorsement
// - enforces no cross-slot endorsements
// - enforces sequencer pace constraint
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
		// check time pace
		if !ledger.ValidSequencerPace(endorsementID.Timestamp(), txTs) {
			return fmt.Errorf("scanEndorsements: endorsement #%d violates sequencer time pace constraint: %s -> %s",
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

	pathToAmounts := easyfl_util.Concat(ledger.PathToProducedOutputs, 0, 0)
	pathToLock := easyfl_util.Concat(ledger.PathToProducedOutputs, 0, 1)

	for i := 0; i < numOutputs; i++ {
		pathToAmounts[len(ledger.PathToProducedOutputs)] = byte(i)
		pathToLock[len(ledger.PathToProducedOutputs)] = byte(i)

		amounts, err = ledger.AmountsFromBytesWithLib(tx.MustBytesAtPath(pathToAmounts), tx.Library)
		if err != nil {
			return fmt.Errorf("scanProducedOutputs: UTXO #%d: '%v'", i, err)
		}

		// just enforcing known lock at index 1
		if _, err = ledger.LockFromBytesWithLib(tx.MustBytesAtPath(pathToLock), tx.Library); err != nil {
			return fmt.Errorf("scanProducedOutputs: UTXO #%d: '%v'", i, err)
		}
		if overflow := amounts.AddToVector(&tx.producedAmountTotals); overflow {
			return fmt.Errorf("scanProducedOutputs: UTXO #%d: 'arithmetic overflow while calculating total of outputs'", i)
		}
	}
	if tx.producedAmountTotals[0] <= 0 {
		return fmt.Errorf("scanProducedOutputs:total produced amount must be positive, got %s", util.Th(tx.producedAmountTotals[0]))
	}
	return nil
}
