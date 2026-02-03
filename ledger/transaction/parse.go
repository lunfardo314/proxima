package transaction

import (
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/crypto/blake2b"
)

// Transaction provides access to the tree of transferable transaction
type (
	Transaction struct {
		*ledger.Library // cached library for this transaction's slot
		*tuples.Tree
		ctx                      *TxContext
		txid                     base.TransactionID
		timestamp                base.LedgerTime
		producedAmountTotals     [15]int64                        // calculated by summing up amount vectors
		sequencerTransactionData *ledger.SequencerTransactionData // if != nil it is sequencer milestone transaction
		traceOption              int
	}

	TxOption func(tx *Transaction) error
)

// MainTxValidationOptions is all except Base, ParseSender, the time bounds and input context validation. Fastest first
var MainTxValidationOptions = []TxOption{
	ParseSequencerData,
	ScanInputs,
	ScanEndorsements,
	ScanOutputs,
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

func FromBytes(txBytes []byte, opt ...TxOption) (*Transaction, error) {
	tree, err := tuples.TreeFromBytesReadOnly(txBytes)
	if err != nil {
		return nil, err
	}
	ret := &Transaction{Tree: tree, traceOption: TraceOptionNone}
	ret.txid, err = TxIDFromTransactionDataTree(ret.Tree)
	if err != nil {
		return nil, err
	}
	ret.timestamp = ret.txid.Timestamp()
	// Cache the library for this transaction's slot once, to avoid repeated L(slot) calls
	ret.Library = ledger.L(ret.timestamp.Slot)
	ret.ctx = ret.contextSkeleton()
	if err = ret.Validate(opt...); err != nil {
		return nil, err
	}
	return ret, nil
}

func FromBytesMainChecksWithOpt(txBytes []byte) (*Transaction, error) {
	tx, err := FromBytes(txBytes, MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// TxIDFromTransactionDataTree validates timestamp, sequencer and stem indices and makes transaction ChainID
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
	// replace first 5 bytes with transaction ChainID prefix
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
	tx, err := FromBytes(txBytes)
	if err != nil {
		return base.TransactionID{}, base.LedgerTime{}, err
	}
	return tx.ID(), tx.Timestamp(), nil
}

func IDFromParsedTransactionBytes(txBytes []byte) (base.TransactionID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	return tx.ID(), nil
}

func (tx *Transaction) SetTraceOption(opt int) {
	tx.traceOption = opt
}

func (tx *Transaction) Validate(opt ...TxOption) error {
	return util.CatchPanicOrError(func() error {
		if err := tx.ctx.Validate(); err != nil {
			return err
		}
		for _, fun := range opt {
			if err := fun(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

func CheckTimestampUpperBound(upperBound time.Time) TxOption {
	return func(tx *Transaction) error {
		ts := ledger.ClockTime(tx.timestamp)
		if ts.After(upperBound) {
			return fmt.Errorf("transaction is %d msec too far in the future", int64(ts.Sub(upperBound))/int64(time.Millisecond))
		}
		return nil
	}
}

// ParseSequencerData validates and parses sequencer data if relevant. Data is cached for frequent extraction
func ParseSequencerData(tx *Transaction) error {
	if !tx.txid.IsSequencerTransaction() {
		// it is known from parsing the txID
		return nil
	}
	seqDataBytes := ledger.MustSequencerDataBytesFromBytes(tx.MustBytesAtPath(Path(ledger.TxSequencerDataBytes)))

	// check sequencer output
	if int(seqDataBytes.SequencerOutputIndex) >= tx.NumProducedOutputs() {
		return fmt.Errorf("wrong sequencer output index")
	}
	out, err := tx.ProducedOutputWithIDAt(seqDataBytes.SequencerOutputIndex)
	if err != nil {
		return fmt.Errorf("ParseSequencerData: '%v' at produced output %d", err, seqDataBytes.SequencerOutputIndex)
	}
	seqOutputData, valid := out.Output.SequencerOutputData()
	if !valid {
		return fmt.Errorf("ParseSequencerData: invalid sequencer output data")
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
		return fmt.Errorf("ParseSequencerData: not a stem lock")
	}
	return nil
}

// ScanInputs validation option scans all inputs:
// - checks number of them
// - check if number of inputs is equal to the number of unlock datas
// - checks for repeating inputs
// - enforces pace constraints
func ScanInputs(tx *Transaction) error {
	numInputs, err := tx.NumElementsAtPath(Path(ledger.TxInputIDs))
	if err != nil {
		return fmt.Errorf("scanning inputs: '%v'", err)
	}
	var oid base.OutputID

	// enforce non-empty input set
	if numInputs <= 0 {
		return fmt.Errorf("number of inputs can't be 0")
	}
	// enforce exactly one unlock data for one input
	numUnlock, err := tx.NumElementsAtPath(Path(ledger.TxUnlockData))
	if err != nil {
		return fmt.Errorf("scanning inputs: '%v'", err)
	}
	if numInputs != numUnlock {
		return fmt.Errorf("number of unlock datas must be equal to the number of inputs")
	}

	ts := tx.Timestamp()
	isSequencer := tx.IsSequencerTransaction()
	path := []byte{ledger.TxInputIDs, 0}
	inps := set.New[base.OutputID]()

	for i := 0; i < numInputs; i++ {
		path[1] = byte(i)
		// parse output ChainID
		oid, err = base.OutputIDFromBytes(tx.MustBytesAtPath(path))
		if err != nil {
			return fmt.Errorf("parsing input #%d: '%v'", i, err)
		}
		// check uniqueness
		if inps.Contains(oid) {
			return fmt.Errorf("repeating input #%d: %s", i, oid.StringShort())
		}
		inps.Insert(oid)
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

// ScanEndorsements
// - parses and checks validity of each endorsement
// - checks repeating endorsements (no very necessary)
// - enforces sequencer pace constraint
func ScanEndorsements(tx *Transaction) error {
	numEndorsements, err := tx.NumElementsAtPath(Path(ledger.TxEndorsements))
	if err != nil {
		return fmt.Errorf("scanning endorsements: '%v'", err)
	}
	if numEndorsements == 0 {
		return nil
	}
	// check max number of endorsements
	txTs := tx.Timestamp()
	if numEndorsements > int(tx.MaxNumberOfEndorsements) {
		return fmt.Errorf("number of endorsements should not exceed %d", tx.MaxNumberOfEndorsements)
	}
	// enforce only sequencer transaction can endorse
	if !tx.IsSequencerTransaction() {
		return fmt.Errorf("non-sequencer transaction cannot contain endorsements")
	}

	var endorsementID base.TransactionID

	unique := set.New[base.TransactionID]()

	path := []byte{ledger.TxEndorsements, 0}
	for i := 0; i < numEndorsements; i++ {
		path[1] = byte(i)
		// parse transaction ChainID
		endorsementID, err = base.TransactionIDFromBytes(tx.MustBytesAtPath(path))
		if err != nil {
			return fmt.Errorf("parsing endorsement #%d: '%v'", i, err)
		}
		// check uniqueness
		if unique.Contains(endorsementID) {
			return fmt.Errorf("repeating endorsement #%d: %s", i, endorsementID.StringShort())
		}
		unique.Insert(endorsementID)
		// check cross-slot endorsements
		if txTs.Slot != endorsementID.Slot() {
			return fmt.Errorf("cross-slot endorsements are not allowed:  %s ->  %s", tx.IDShortString(), endorsementID.StringShort())
		}
		// check time pace
		if !ledger.ValidSequencerPace(endorsementID.Timestamp(), txTs) {
			return fmt.Errorf("endorsement #%d violates sequencer time pace constraint: %s -> %s", i, txTs.String(), endorsementID.StringShort())
		}
	}
	return nil
}

// ScanOutputs
// - scans all outputs
// - enforces the existence of the mandatory constrains,
// - computes total of outputs and total inflation
func ScanOutputs(tx *Transaction) error {
	numOutputs, err := tx.NumElementsAtPath(Path(ledger.TxOutputs))
	if err != nil {
		return fmt.Errorf("scanning outputs: '%v'", err)
	}
	var amounts ledger.Amounts

	pathToAmounts := []byte{ledger.TxOutputs, 0, 0}
	pathToLock := []byte{ledger.TxOutputs, 0, 1}

	for i := 0; i < numOutputs; i++ {
		pathToAmounts[1] = byte(i)
		amounts, err = ledger.AmountsFromBytesWithLib(tx.MustBytesAtPath(pathToAmounts), tx.Library)
		if err != nil {
			return fmt.Errorf("scanning output #%d: '%v'", i, err)
		}

		// just enforcing known lock at index 1
		if _, err = ledger.LockFromBytesWithLib(tx.MustBytesAtPath(pathToLock), tx.Library); err != nil {
			return fmt.Errorf("scanning output #%d: '%v'", i, err)
		}
		if overflow := amounts.AddToVector(&tx.producedAmountTotals); overflow {
			return fmt.Errorf("scanning output #%d: 'arithmetic overflow while calculating total of outputs'", i)
		}
	}
	return nil
}

func ValidateOptionWithFullContext(inputLoaderByIndex func(i byte) (*ledger.Output, error)) TxOption {
	return func(tx *Transaction) error {
		var ctx *TxContext
		var err error
		if __printLogOnFail.Load() {
			ctx, err = tx.ContextFull(inputLoaderByIndex, TraceOptionAll)
		} else {
			ctx, err = tx.ContextFull(inputLoaderByIndex)
		}
		if err != nil {
			return err
		}
		return ctx.Validate()
	}
}
