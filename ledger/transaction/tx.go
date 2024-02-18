package transaction

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/ed25519"
)

// Transaction provides access to the tree of transferable transaction
type (
	Transaction struct {
		tree                     *lazybytes.Tree
		txHash                   ledger.TransactionIDShort
		sequencerMilestoneFlag   bool
		branchTransactionFlag    bool
		sender                   ledger.AddressED25519
		timestamp                ledger.Time
		totalAmount              uint64                    // persisted in tx
		totalInflation           uint64                    // calculated
		sequencerTransactionData *SequencerTransactionData // if != nil it is sequencer milestone transaction
	}

	TxValidationOption func(tx *Transaction) error

	// SequencerTransactionData represents sequencer and stem data on the transaction
	SequencerTransactionData struct {
		SequencerOutputData  *ledger.SequencerOutputData
		StemOutputData       *ledger.StemLock // nil if does not contain stem output
		SequencerID          ledger.ChainID   // adjusted for chain origin
		SequencerOutputIndex byte
		StemOutputIndex      byte // 0xff if not a branch transaction
	}
)

// MainTxValidationOptions is all except Base, time bounds and input context validation
var MainTxValidationOptions = []TxValidationOption{
	ScanSequencerData(),
	CheckSender(),
	CheckNumElements(),
	CheckTimePace(),
	CheckEndorsements(),
	CheckUniqueness(),
	ScanOutputs(),
	CheckSizeOfOutputCommitment(),
}

func FromBytes(txBytes []byte, opt ...TxValidationOption) (*Transaction, error) {
	ret, err := transactionFromBytes(txBytes, BaseValidation())
	if err != nil {
		return nil, fmt.Errorf("transaction.FromBytes: basic parse failed: '%v'", err)
	}
	if err = ret.Validate(opt...); err != nil {
		return ret, fmt.Errorf("FromBytes: validation failed, txid = %s: '%v'", ret.IDShortString(), err)
	}
	return ret, nil
}

func FromBytesMainChecksWithOpt(txBytes []byte, additional ...TxValidationOption) (*Transaction, error) {
	tx, err := FromBytes(txBytes, MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	if err = tx.Validate(additional...); err != nil {
		return nil, err
	}
	return tx, nil
}

func transactionFromBytes(txBytes []byte, opts ...TxValidationOption) (*Transaction, error) {
	ret := &Transaction{
		tree: lazybytes.TreeFromBytesReadOnly(txBytes),
	}
	if err := ret.Validate(opts...); err != nil {
		return nil, err
	}
	return ret, nil
}

func IDAndTimestampFromTransactionBytes(txBytes []byte) (ledger.TransactionID, ledger.Time, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return ledger.TransactionID{}, ledger.Time{}, err
	}
	return *tx.ID(), tx.Timestamp(), nil
}

func IDFromTransactionBytes(txBytes []byte) (ledger.TransactionID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return ledger.TransactionID{}, err
	}
	return *tx.ID(), nil
}

func (tx *Transaction) Validate(opt ...TxValidationOption) error {
	return util.CatchPanicOrError(func() error {
		for _, fun := range opt {
			if err := fun(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

// BaseValidation is a checking of being able to extract ID. If not, bytes are not identifiable as a transaction
func BaseValidation() TxValidationOption {
	return func(tx *Transaction) error {
		var tsBin []byte
		tsBin = tx.tree.BytesAtPath(Path(ledger.TxTimestamp))
		var err error
		outputIndexData := tx.tree.BytesAtPath(Path(ledger.TxSequencerAndStemOutputIndices))
		if len(outputIndexData) != 2 {
			return fmt.Errorf("wrong sequencer and stem output indices, must be 2 bytes")
		}

		tx.sequencerMilestoneFlag, tx.branchTransactionFlag = outputIndexData[0] != 0xff, outputIndexData[1] != 0xff
		if tx.branchTransactionFlag && !tx.sequencerMilestoneFlag {
			return fmt.Errorf("wrong branch transaction flag")
		}

		if tx.timestamp, err = ledger.TimeFromBytes(tsBin); err != nil {
			return err
		}
		if tx.timestamp.Tick() == 0 && tx.sequencerMilestoneFlag && !tx.branchTransactionFlag {
			// enforcing only branch milestones on the time slot boundary (i.e. with tick = 0)
			// non-sequencer transactions with tick == 0 are still allowed
			return fmt.Errorf("when on time slot boundary, a sequencer transaction must be a branch: %s", tx.IDShortString())
		}

		totalAmountBin := tx.tree.BytesAtPath(Path(ledger.TxTotalProducedAmount))
		if len(totalAmountBin) != 8 {
			return fmt.Errorf("wrong total amount bytes, must be 8 bytes")
		}
		tx.totalAmount = binary.BigEndian.Uint64(totalAmountBin)

		tx.txHash = ledger.HashTransactionBytes(tx.tree.Bytes())

		return nil
	}
}

func CheckTimestampLowerBound(lowerBound time.Time) TxValidationOption {
	return func(tx *Transaction) error {
		if tx.timestamp.Time().Before(lowerBound) {
			return fmt.Errorf("transaction is too old")
		}
		return nil
	}
}

func CheckTimestampUpperBound(upperBound time.Time) TxValidationOption {
	return func(tx *Transaction) error {
		ts := tx.timestamp.Time()
		if ts.After(upperBound) {
			return fmt.Errorf("transaction is %d msec too far in the future", int64(ts.Sub(upperBound))/int64(time.Millisecond))
		}
		return nil
	}
}

func ScanSequencerData() TxValidationOption {
	return func(tx *Transaction) error {
		util.Assertf(tx.sequencerMilestoneFlag || !tx.branchTransactionFlag, "tx.sequencerMilestoneFlag || !tx.branchTransactionFlag")
		if !tx.sequencerMilestoneFlag {
			return nil
		}
		outputIndexData := tx.tree.BytesAtPath(Path(ledger.TxSequencerAndStemOutputIndices))
		util.Assertf(len(outputIndexData) == 2, "len(outputIndexData) == 2")
		sequencerOutputIndex, stemOutputIndex := outputIndexData[0], outputIndexData[1]

		// -------------------- check sequencer output
		if int(sequencerOutputIndex) >= tx.NumProducedOutputs() {
			return fmt.Errorf("wrong sequencer output index")
		}

		out, err := tx.ProducedOutputWithIDAt(sequencerOutputIndex)
		if err != nil {
			return fmt.Errorf("ScanSequencerData: '%v' at produced output %d", err, sequencerOutputIndex)
		}

		seqOutputData, valid := out.Output.SequencerOutputData()
		if !valid {
			return fmt.Errorf("ScanSequencerData: invalid sequencer output data")
		}

		var sequencerID ledger.ChainID
		if seqOutputData.ChainConstraint.IsOrigin() {
			sequencerID = ledger.MakeOriginChainID(&out.ID)
		} else {
			sequencerID = seqOutputData.ChainConstraint.ID
		}

		// it is sequencer milestone transaction
		tx.sequencerTransactionData = &SequencerTransactionData{
			SequencerOutputData: seqOutputData,
			SequencerID:         sequencerID,
			StemOutputIndex:     stemOutputIndex,
			StemOutputData:      nil,
		}

		// ---  check stem output data

		if !tx.branchTransactionFlag {
			// not a branch transaction
			return nil
		}
		if stemOutputIndex == sequencerOutputIndex || int(stemOutputIndex) >= tx.NumProducedOutputs() {
			return fmt.Errorf("ScanSequencerData: wrong stem output index")
		}
		outStem, err := tx.ProducedOutputWithIDAt(stemOutputIndex)
		if err != nil {
			return fmt.Errorf("ScanSequencerData stem: %v", err)
		}
		lock := outStem.Output.Lock()
		if lock.Name() != ledger.StemLockName {
			return fmt.Errorf("ScanSequencerData: not a stem lock")
		}
		tx.sequencerTransactionData.StemOutputData = lock.(*ledger.StemLock)
		return nil
	}
}

// CheckSender returns a signature validator. It also sets the sender field
func CheckSender() TxValidationOption {
	return func(tx *Transaction) error {
		// mandatory sender signature
		sigData := tx.tree.BytesAtPath(Path(ledger.TxSignature))
		senderPubKey := ed25519.PublicKey(sigData[64:])
		tx.sender = ledger.AddressED25519FromPublicKey(senderPubKey)
		if !ed25519.Verify(senderPubKey, tx.EssenceBytes(), sigData[0:64]) {
			return fmt.Errorf("invalid signature")
		}
		return nil
	}
}

func CheckNumElements() TxValidationOption {
	return func(tx *Transaction) error {
		if tx.tree.NumElements(Path(ledger.TxOutputs)) <= 0 {
			return fmt.Errorf("number of outputs can't be 0")
		}

		numInputs := tx.tree.NumElements(Path(ledger.TxInputIDs))
		if numInputs <= 0 {
			return fmt.Errorf("number of inputs can't be 0")
		}

		if numInputs != tx.tree.NumElements(Path(ledger.TxUnlockData)) {
			return fmt.Errorf("number of unlock params must be equal to the number of inputs")
		}

		if tx.tree.NumElements(Path(ledger.TxEndorsements)) > ledger.MaxNumberOfEndorsements {
			return fmt.Errorf("number of endorsements exceeds limit of %d", ledger.MaxNumberOfEndorsements)
		}
		return nil
	}
}

func CheckUniqueness() TxValidationOption {
	return func(tx *Transaction) error {
		var err error
		// check if inputs are unique
		inps := make(map[ledger.OutputID]struct{})
		tx.ForEachInput(func(i byte, oid *ledger.OutputID) bool {
			_, already := inps[*oid]
			if already {
				err = fmt.Errorf("repeating input @ %d", i)
				return false
			}
			inps[*oid] = struct{}{}
			return true
		})
		if err != nil {
			return err
		}

		// check if endorsements are unique
		endorsements := make(map[ledger.TransactionID]struct{})
		tx.ForEachEndorsement(func(i byte, txid *ledger.TransactionID) bool {
			_, already := endorsements[*txid]
			if already {
				err = fmt.Errorf("repeating endorsement @ %d", i)
				return false
			}
			endorsements[*txid] = struct{}{}
			return true
		})
		if err != nil {
			return err
		}
		return nil
	}
}

// CheckTimePace consumed outputs must satisfy time pace constraint
func CheckTimePace() TxValidationOption {
	return func(tx *Transaction) error {
		var err error
		ts := tx.Timestamp()
		if tx.IsSequencerMilestone() {
			tx.ForEachInput(func(_ byte, oid *ledger.OutputID) bool {
				if !ledger.ValidSequencerPace(oid.Timestamp(), ts) {
					err = fmt.Errorf("timestamp of input violates sequencer time pace constraint: %s", oid.StringShort())
					return false
				}
				return true
			})
		} else {
			tx.ForEachInput(func(_ byte, oid *ledger.OutputID) bool {
				if !ledger.ValidTransactionPace(oid.Timestamp(), ts) {
					err = fmt.Errorf("timestamp of input violates non-sequencer time pace constraint: %s", oid.StringShort())
					return false
				}
				return true
			})
		}
		return err
	}
}

// CheckEndorsements endorsed transactions must be sequencer transaction from the current slot
func CheckEndorsements() TxValidationOption {
	return func(tx *Transaction) error {
		var err error

		if !tx.IsSequencerMilestone() && tx.NumEndorsements() > 0 {
			return fmt.Errorf("non-sequencer tx can't contain endorsements: %s", tx.IDShortString())
		}

		txSlot := tx.Timestamp().Slot()
		tx.ForEachEndorsement(func(_ byte, endorsedTxID *ledger.TransactionID) bool {
			if !endorsedTxID.IsSequencerMilestone() {
				err = fmt.Errorf("tx %s contains endorsement of non-sequencer transaction: %s", tx.IDShortString(), endorsedTxID.StringShort())
				return false
			}
			if endorsedTxID.Slot() != txSlot {
				err = fmt.Errorf("tx %s can't endorse tx from another slot: %s", tx.IDShortString(), endorsedTxID.StringShort())
				return false
			}
			return true
		})
		return err
	}
}

// ScanOutputs validation option scans all inputs, enforces existence of mandatory constrains,
// computes total of outputs and total inflation
func ScanOutputs() TxValidationOption {
	return func(tx *Transaction) error {
		numOutputs := tx.tree.NumElements(Path(ledger.TxOutputs))
		var err error
		var totalAmount uint64
		var amount ledger.Amount

		// TODO inflation

		var o *ledger.Output
		path := []byte{ledger.TxOutputs, 0}
		for i := 0; i < numOutputs; i++ {
			path[1] = byte(i)
			o, amount, _, err = ledger.OutputFromBytesMain(tx.tree.BytesAtPath(path))
			if err != nil {
				return fmt.Errorf("scanning output #%d: '%v'", i, err)
			}
			if uint64(amount) > math.MaxUint64-totalAmount {
				return fmt.Errorf("scanning output #%d: 'arithmetic overflow while calculating total of outputs'", i)
			}
			totalAmount += uint64(amount)
			tx.totalInflation += o.Inflation()
		}
		if tx.totalAmount != totalAmount {
			return fmt.Errorf("wrong total produced amount")
		}
		return nil
	}
}

func CheckSizeOfOutputCommitment() TxValidationOption {
	return func(tx *Transaction) error {
		data := tx.tree.BytesAtPath(Path(ledger.TxInputCommitment))
		if len(data) != 32 {
			return fmt.Errorf("input commitment must be 32-bytes long")
		}
		return nil
	}
}

func ValidateOptionWithFullContext(inputLoaderByIndex func(i byte) (*ledger.Output, error)) TxValidationOption {
	return func(tx *Transaction) error {
		var ctx *TxContext
		var err error
		if __printLogOnFail.Load() {
			ctx, err = TxContextFromTransaction(tx, inputLoaderByIndex, TraceOptionAll)
		} else {
			ctx, err = TxContextFromTransaction(tx, inputLoaderByIndex)
		}
		if err != nil {
			return err
		}
		return ctx.Validate()
	}
}

func (tx *Transaction) ID() *ledger.TransactionID {
	ret := ledger.NewTransactionID(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
	return &ret
}

func (tx *Transaction) IDString() string {
	return ledger.TransactionIDString(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
}

func (tx *Transaction) IDShortString() string {
	return ledger.TransactionIDStringShort(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
}

func (tx *Transaction) IDVeryShort() string {
	return ledger.TransactionIDStringVeryShort(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
}

func (tx *Transaction) Slot() ledger.Slot {
	return tx.timestamp.Slot()
}

func (tx *Transaction) Hash() ledger.TransactionIDShort {
	return tx.txHash
}

// SequencerTransactionData returns nil it is not a sequencer milestone
func (tx *Transaction) SequencerTransactionData() *SequencerTransactionData {
	return tx.sequencerTransactionData
}

func (tx *Transaction) IsSequencerMilestone() bool {
	return tx.sequencerMilestoneFlag
}

func (tx *Transaction) SequencerInfoString() string {
	if !tx.IsSequencerMilestone() {
		return "(not a sequencer ms)"
	}
	seqMeta := tx.SequencerTransactionData()
	return fmt.Sprintf("SEQ(%s), in: %d, out:%d, amount on chain: %d, stem output: %v",
		seqMeta.SequencerID.StringVeryShort(),
		tx.NumInputs(),
		tx.NumProducedOutputs(),
		seqMeta.SequencerOutputData.AmountOnChain,
		seqMeta.StemOutputData != nil,
	)
}

func (tx *Transaction) IsBranchTransaction() bool {
	return tx.sequencerMilestoneFlag && tx.branchTransactionFlag
}

func (tx *Transaction) StemOutputData() *ledger.StemLock {
	if tx.sequencerTransactionData != nil {
		return tx.sequencerTransactionData.StemOutputData
	}
	return nil
}

func (m *SequencerTransactionData) Short() string {
	return fmt.Sprintf("SEQ(%s)", m.SequencerID.StringVeryShort())
}

func (tx *Transaction) SequencerOutput() *ledger.OutputWithID {
	util.Assertf(tx.IsSequencerMilestone(), "tx.IsSequencerMilestone()")
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
}

func (tx *Transaction) StemOutput() *ledger.OutputWithID {
	util.Assertf(tx.IsBranchTransaction(), "tx.IsBranchTransaction()")
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().StemOutputIndex)
}

func (tx *Transaction) SenderAddress() ledger.AddressED25519 {
	return tx.sender
}

func (tx *Transaction) Timestamp() ledger.Time {
	return tx.timestamp
}

func (tx *Transaction) TimestampTime() time.Time {
	return tx.timestamp.Time()
}

func (tx *Transaction) TotalAmount() uint64 {
	return tx.totalAmount
}

func EssenceBytesFromTransactionDataTree(txTree *lazybytes.Tree) []byte {
	return common.Concat(
		txTree.BytesAtPath([]byte{ledger.TxInputIDs}),
		txTree.BytesAtPath([]byte{ledger.TxOutputs}),
		txTree.BytesAtPath([]byte{ledger.TxTimestamp}),
		txTree.BytesAtPath([]byte{ledger.TxSequencerAndStemOutputIndices}),
		txTree.BytesAtPath([]byte{ledger.TxInputCommitment}),
		txTree.BytesAtPath([]byte{ledger.TxEndorsements}),
	)
}

func (tx *Transaction) Bytes() []byte {
	return tx.tree.Bytes()
}

func (tx *Transaction) EssenceBytes() []byte {
	return EssenceBytesFromTransactionDataTree(tx.tree)
}

func (tx *Transaction) NumProducedOutputs() int {
	return tx.tree.NumElements(Path(ledger.TxOutputs))
}

func (tx *Transaction) NumInputs() int {
	return tx.tree.NumElements(Path(ledger.TxInputIDs))
}

func (tx *Transaction) NumEndorsements() int {
	return tx.tree.NumElements(Path(ledger.TxEndorsements))
}

func (tx *Transaction) MustOutputDataAt(idx byte) []byte {
	return tx.tree.BytesAtPath(common.Concat(ledger.TxOutputs, idx))
}

func (tx *Transaction) MustProducedOutputAt(idx byte) *ledger.Output {
	ret, err := ledger.OutputFromBytesReadOnly(tx.MustOutputDataAt(idx))
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputAt(idx byte) (*ledger.Output, error) {
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
	}
	out, err := ledger.OutputFromBytesReadOnly(tx.MustOutputDataAt(idx))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (tx *Transaction) ProducedOutputWithIDAt(idx byte) (*ledger.OutputWithID, error) {
	ret, err := tx.ProducedOutputAt(idx)
	if err != nil {
		return nil, err
	}
	return &ledger.OutputWithID{
		ID:     tx.OutputID(idx),
		Output: ret,
	}, nil
}

func (tx *Transaction) MustProducedOutputWithIDAt(idx byte) *ledger.OutputWithID {
	ret, err := tx.ProducedOutputWithIDAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputs() []*ledger.OutputWithID {
	ret := make([]*ledger.OutputWithID, tx.NumProducedOutputs())
	for i := range ret {
		ret[i] = tx.MustProducedOutputWithIDAt(byte(i))
	}
	return ret
}

func (tx *Transaction) InputAt(idx byte) (ret ledger.OutputID, err error) {
	if int(idx) >= tx.NumInputs() {
		return [33]byte{}, fmt.Errorf("InputAt: wrong input index")
	}
	data := tx.tree.BytesAtPath(common.Concat(ledger.TxInputIDs, idx))
	ret, err = ledger.OutputIDFromBytes(data)
	return
}

func (tx *Transaction) MustInputAt(idx byte) ledger.OutputID {
	ret, err := tx.InputAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) MustOutputIndexOfTheInput(inputIdx byte) byte {
	return ledger.MustOutputIndexFromIDBytes(tx.tree.BytesAtPath(common.Concat(ledger.TxInputIDs, inputIdx)))
}

func (tx *Transaction) InputAtString(idx byte) string {
	ret, err := tx.InputAt(idx)
	if err != nil {
		return err.Error()
	}
	return ret.String()
}

func (tx *Transaction) InputAtShort(idx byte) string {
	ret, err := tx.InputAt(idx)
	if err != nil {
		return err.Error()
	}
	return ret.StringShort()
}

func (tx *Transaction) Inputs() []ledger.OutputID {
	ret := make([]ledger.OutputID, tx.NumInputs())
	for i := range ret {
		ret[i] = tx.MustInputAt(byte(i))
	}
	return ret
}

func (tx *Transaction) MustUnlockDataAt(idx byte) []byte {
	return tx.tree.BytesAtPath(common.Concat(ledger.TxUnlockData, idx))
}

func (tx *Transaction) ConsumedOutputAt(idx byte, fetchOutput func(id *ledger.OutputID) ([]byte, bool)) (*ledger.OutputDataWithID, error) {
	oid, err := tx.InputAt(idx)
	if err != nil {
		return nil, err
	}
	ret, ok := fetchOutput(&oid)
	if !ok {
		return nil, fmt.Errorf("can't fetch output %s", oid.StringShort())
	}
	return &ledger.OutputDataWithID{
		ID:         oid,
		OutputData: ret,
	}, nil
}

func (tx *Transaction) EndorsementAt(idx byte) ledger.TransactionID {
	data := tx.tree.BytesAtPath(common.Concat(ledger.TxEndorsements, idx))
	ret, err := ledger.TransactionIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

// HashInputsAndEndorsements blake2b of concatenated input IDs and endorsements
// independent on any other tz data but inputs
func (tx *Transaction) HashInputsAndEndorsements() [32]byte {
	var buf bytes.Buffer

	buf.Write(tx.tree.BytesAtPath(Path(ledger.TxInputIDs)))
	buf.Write(tx.tree.BytesAtPath(Path(ledger.TxEndorsements)))

	return blake2b.Sum256(buf.Bytes())
}

func (tx *Transaction) ForEachInput(fun func(i byte, oid *ledger.OutputID) bool) {
	tx.tree.ForEach(func(i byte, data []byte) bool {
		oid, err := ledger.OutputIDFromBytes(data)
		util.Assertf(err == nil, "ForEachInput @ %d: %v", i, err)
		return fun(i, &oid)
	}, Path(ledger.TxInputIDs))
}

func (tx *Transaction) ForEachEndorsement(fun func(idx byte, txid *ledger.TransactionID) bool) {
	tx.tree.ForEach(func(i byte, data []byte) bool {
		txid, err := ledger.TransactionIDFromBytes(data)
		util.Assertf(err == nil, "ForEachEndorsement @ %d: %v", i, err)
		return fun(i, &txid)
	}, Path(ledger.TxEndorsements))
}

func (tx *Transaction) ForEachOutputData(fun func(idx byte, oData []byte) bool) {
	tx.tree.ForEach(func(i byte, data []byte) bool {
		return fun(i, data)
	}, Path(ledger.TxOutputs))
}

// ForEachProducedOutput traverses all produced outputs
// Inside callback function the correct outputID must be obtained with OutputID(idx byte) ledger.OutputID
// because stem output ID has special form
func (tx *Transaction) ForEachProducedOutput(fun func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool) {
	tx.ForEachOutputData(func(idx byte, oData []byte) bool {
		o, _ := ledger.OutputFromBytesReadOnly(oData)
		oid := tx.OutputID(idx)
		if !fun(idx, o, &oid) {
			return false
		}
		return true
	})
}

func (tx *Transaction) PredecessorTransactionIDs() set.Set[ledger.TransactionID] {
	ret := set.New[ledger.TransactionID]()
	tx.ForEachInput(func(_ byte, oid *ledger.OutputID) bool {
		ret.Insert(oid.TransactionID())
		return true
	})
	tx.ForEachEndorsement(func(_ byte, txid *ledger.TransactionID) bool {
		ret.Insert(*txid)
		return true
	})
	return ret
}

// SequencerAndStemOutputIndices return seq output index and stem output index
func (tx *Transaction) SequencerAndStemOutputIndices() (byte, byte) {
	ret := tx.tree.BytesAtPath([]byte{ledger.TxSequencerAndStemOutputIndices})
	util.Assertf(len(ret) == 2, "len(ret)==2")
	return ret[0], ret[1]
}

func (tx *Transaction) OutputID(idx byte) ledger.OutputID {
	return ledger.NewOutputID(tx.ID(), idx)
}

func (tx *Transaction) InflationAmount() uint64 {
	return tx.totalInflation
}

func OutputWithIDFromTransactionBytes(txBytes []byte, idx byte) (*ledger.OutputWithID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
	}
	return tx.ProducedOutputWithIDAt(idx)
}

func OutputsWithIDFromTransactionBytes(txBytes []byte) ([]*ledger.OutputWithID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return nil, err
	}

	ret := make([]*ledger.OutputWithID, tx.NumProducedOutputs())
	for idx := 0; idx < tx.NumProducedOutputs(); idx++ {
		ret[idx], err = tx.ProducedOutputWithIDAt(byte(idx))
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (tx *Transaction) ToString(fetchOutput func(oid *ledger.OutputID) ([]byte, bool)) string {
	ctx, err := TxContextFromTransaction(tx, func(i byte) (*ledger.Output, error) {
		oid, err1 := tx.InputAt(i)
		if err1 != nil {
			return nil, err1
		}
		oData, ok := fetchOutput(&oid)
		if !ok {
			return nil, fmt.Errorf("output %s has not been found", oid.StringShort())
		}
		o, err1 := ledger.OutputFromBytesReadOnly(oData)
		if err1 != nil {
			return nil, err1
		}
		return o, nil
	})
	if err != nil {
		return err.Error()
	}
	return ctx.String()
}

func (tx *Transaction) ToStringWithInputLoaderByIndex(fetchOutput func(i byte) (*ledger.Output, error)) string {
	ctx, err := TxContextFromTransaction(tx, fetchOutput)
	if err != nil {
		return err.Error()
	}
	return ctx.String()
}

func (tx *Transaction) InputLoaderByIndex(fetchOutput func(oid *ledger.OutputID) ([]byte, bool)) func(byte) (*ledger.Output, error) {
	return func(idx byte) (*ledger.Output, error) {
		inp := tx.MustInputAt(idx)
		odata, ok := fetchOutput(&inp)
		if !ok {
			return nil, fmt.Errorf("can't load input #%d: %s", idx, inp.String())
		}
		o, err := ledger.OutputFromBytesReadOnly(odata)
		if err != nil {
			return nil, fmt.Errorf("can't load input #%d: %s, '%v'", idx, inp.String(), err)
		}
		return o, nil
	}
}

func (tx *Transaction) InputLoaderFromState(rdr global.StateReader) func(idx byte) (*ledger.Output, error) {
	return tx.InputLoaderByIndex(func(oid *ledger.OutputID) ([]byte, bool) {
		return rdr.GetUTXO(oid)
	})
}

// SequencerChainPredecessor returns chain predecessor output ID
// If it is chain origin, it returns nil. Otherwise, it may or may not be a sequencer ID
// It also returns index of the inout
func (tx *Transaction) SequencerChainPredecessor() (*ledger.OutputID, byte) {
	seqMeta := tx.SequencerTransactionData()
	util.Assertf(seqMeta != nil, "SequencerChainPredecessor: must be a sequencer transaction")

	if seqMeta.SequencerOutputData.ChainConstraint.IsOrigin() {
		return nil, 0xff
	}

	ret, err := tx.InputAt(seqMeta.SequencerOutputData.ChainConstraint.PredecessorInputIndex)
	util.AssertNoError(err)
	// The following is ensured by the 'chain' and 'sequencer' constraints on the transaction
	// Returned predecessor outputID must be:
	// - if the transaction is branch tx, then it returns tx ID which may or may not be a sequencer transaction ID
	// - if the transaction is not a branch tx, it must always return sequencer tx ID (which may or may not be a branch)
	return &ret, seqMeta.SequencerOutputData.ChainConstraint.PredecessorInputIndex
}

func (tx *Transaction) FindChainOutput(chainID ledger.ChainID) *ledger.OutputWithID {
	var ret *ledger.OutputWithID
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool {
		cc, idx := o.ChainConstraint()
		if idx == 0xff {
			return true
		}
		cID := cc.ID
		if cc.IsOrigin() {
			cID = ledger.MakeOriginChainID(oid)
		}
		if cID == chainID {
			ret = &ledger.OutputWithID{
				ID:     *oid,
				Output: o,
			}
			return false
		}
		return true
	})
	return ret
}

func (tx *Transaction) FindStemProducedOutput() *ledger.OutputWithID {
	if !tx.IsBranchTransaction() {
		return nil
	}
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().StemOutputIndex)
}

func (tx *Transaction) EndorsementsVeryShort() string {
	ret := make([]string, tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid *ledger.TransactionID) bool {
		ret[idx] = txid.StringVeryShort()
		return true
	})
	return strings.Join(ret, ", ")
}

func (tx *Transaction) ProducedOutputsToString() string {
	ret := make([]string, 0)
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool {
		ret = append(ret, fmt.Sprintf("  %d :", idx), o.ToString("    "))
		return true
	})
	return strings.Join(ret, "\n")
}

func (tx *Transaction) StateMutations() *multistate.Mutations {
	ret := multistate.NewMutations()
	tx.ForEachInput(func(i byte, oid *ledger.OutputID) bool {
		ret.InsertDelOutputMutation(*oid)
		return true
	})
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid *ledger.OutputID) bool {
		ret.InsertAddOutputMutation(*oid, o)
		return true
	})
	ret.InsertAddTxMutation(*tx.ID(), tx.Slot(), byte(tx.NumProducedOutputs()-1))
	return ret
}

func (tx *Transaction) Lines(inputLoaderByIndex func(i byte) (*ledger.Output, error), prefix ...string) *lines.Lines {
	ctx, err := TxContextFromTransaction(tx, inputLoaderByIndex)
	if err != nil {
		ret := lines.New(prefix...)
		ret.Add("can't create context of transaction %s: '%v'", tx.IDShortString(), err)
		return ret
	}
	return ctx.Lines(prefix...)
}

func (tx *Transaction) ProducedOutputsWithTargetLock(lock ledger.Lock) []*ledger.OutputWithID {
	ret := make([]*ledger.OutputWithID, 0)
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid *ledger.OutputID) bool {
		if ledger.EqualConstraints(lock, o.Lock()) {
			ret = append(ret, &ledger.OutputWithID{
				ID:     *oid,
				Output: o,
			})
		}
		return true
	})
	return ret
}

func (tx *Transaction) LinesShort(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("ID: %s", tx.ID().String())
	ret.Add("Sender address: %s", tx.SenderAddress().String())
	ret.Add("Total: %s", util.GoTh(tx.TotalAmount()))
	ret.Add("Inflation: %s", util.GoTh(tx.InflationAmount()))
	if tx.IsSequencerMilestone() {
		ret.Add("Sequencer output index: %d, Stem output index: %d", tx.sequencerTransactionData.SequencerOutputIndex, tx.sequencerTransactionData.StemOutputIndex)
	}
	ret.Add("Endorsements (%d):", tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid *ledger.TransactionID) bool {
		ret.Add("    %3d: %s", idx, txid.String())
		return true
	})
	ret.Add("Inputs (%d):", tx.NumInputs())
	tx.ForEachInput(func(i byte, oid *ledger.OutputID) bool {
		ret.Add("    %3d: %s", i, oid.String())
		ret.Add("       Unlock data: %s", UnlockDataToString(tx.MustUnlockDataAt(i)))
		return true
	})
	ret.Add("Outputs (%d):", tx.NumProducedOutputs())
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool {
		ret.Add("%s", oid.StringShort())
		ret.Append(o.Lines(pref + "    "))
		return true
	})
	return ret
}

func (tx *Transaction) String() string {
	return tx.LinesShort().String()
}
