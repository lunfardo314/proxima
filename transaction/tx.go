package transaction

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/ed25519"
)

// Transaction provides access to the tree of transferable transaction
type (
	Transaction struct {
		tree                     *lazybytes.Tree
		txHash                   core.TransactionIDShort
		sequencerMilestoneFlag   bool
		branchTransactionFlag    bool
		sender                   core.AddressED25519
		timestamp                core.LogicalTime
		totalAmount              core.Amount
		sequencerTransactionData *SequencerTransactionData // if != nil it is sequencer milestone transaction
		byteSize                 int
	}

	TxValidationOption func(tx *Transaction) error

	// SequencerTransactionData represents sequencer and stem data on the transaction
	SequencerTransactionData struct {
		SequencerOutputData  *core.SequencerOutputData
		StemOutputData       *core.StemLock // nil if does not contain stem output
		SequencerID          core.ChainID   // adjusted for chain origin
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
		return nil, fmt.Errorf("FromBytes: validation failed, txid = %s: '%v'", ret.IDShort(), err)
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

func transactionFromBytes(txBytes []byte, opt ...TxValidationOption) (*Transaction, error) {
	ret := &Transaction{
		tree:     lazybytes.TreeFromBytesReadOnly(txBytes),
		byteSize: len(txBytes),
	}
	if err := ret.Validate(opt...); err != nil {
		return nil, err
	}
	return ret, nil
}

func IDAndTimestampFromTransactionBytes(txBytes []byte) (core.TransactionID, core.LogicalTime, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return core.TransactionID{}, core.LogicalTime{}, err
	}
	return *tx.ID(), tx.Timestamp(), nil
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
		tsBin = tx.tree.BytesAtPath(Path(core.TxTimestamp))
		var err error
		outputIndexData := tx.tree.BytesAtPath(Path(core.TxSequencerAndStemOutputIndices))
		if len(outputIndexData) != 2 {
			return fmt.Errorf("wrong sequencer and stem output indices, must be 2 bytes")
		}

		tx.sequencerMilestoneFlag, tx.branchTransactionFlag = outputIndexData[0] != 0xff, outputIndexData[1] != 0xff
		if tx.branchTransactionFlag && !tx.sequencerMilestoneFlag {
			return fmt.Errorf("wrong branch transaction flag")
		}

		if tx.timestamp, err = core.LogicalTimeFromBytes(tsBin); err != nil {
			return err
		}
		if tx.timestamp.TimeTick() == 0 && tx.sequencerMilestoneFlag && !tx.branchTransactionFlag {
			// enforcing only branch milestones on the time slot boundary (i.e. with tick = 0)
			// non-sequencer transactions with tick == 0 are still allowed
			return fmt.Errorf("when on time slot boundary, a sequencer transaction must be a branch")
		}

		totalAmountBin := tx.tree.BytesAtPath(Path(core.TxTotalProducedAmount))
		if len(totalAmountBin) != 8 {
			return fmt.Errorf("wrong total amount bytes, must be 8 bytes")
		}
		tx.totalAmount = core.Amount(binary.BigEndian.Uint64(totalAmountBin))

		tx.txHash = core.HashTransactionBytes(tx.tree.Bytes())

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
		outputIndexData := tx.tree.BytesAtPath(Path(core.TxSequencerAndStemOutputIndices))
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

		var sequencerID core.ChainID
		if seqOutputData.ChainConstraint.IsOrigin() {
			sequencerID = core.OriginChainID(&out.ID)
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
		if lock.Name() != core.StemLockName {
			return fmt.Errorf("ScanSequencerData: not a stem lock")
		}
		tx.sequencerTransactionData.StemOutputData = lock.(*core.StemLock)
		return nil
	}
}

// CheckSender returns a signature validator. It also set the sender field
func CheckSender() TxValidationOption {
	return func(tx *Transaction) error {
		// mandatory sender signature
		sigData := tx.tree.BytesAtPath(Path(core.TxSignature))
		senderPubKey := ed25519.PublicKey(sigData[64:])
		tx.sender = core.AddressED25519FromPublicKey(senderPubKey)
		if !ed25519.Verify(senderPubKey, tx.EssenceBytes(), sigData[0:64]) {
			return fmt.Errorf("invalid signature")
		}
		return nil
	}
}

func CheckNumElements() TxValidationOption {
	return func(tx *Transaction) error {
		if tx.tree.NumElements(Path(core.TxOutputs)) <= 0 {
			return fmt.Errorf("number of outputs can't be 0")
		}

		numInputs := tx.tree.NumElements(Path(core.TxInputIDs))
		if numInputs <= 0 {
			return fmt.Errorf("number of inputs can't be 0")
		}

		if numInputs != tx.tree.NumElements(Path(core.TxUnlockParams)) {
			return fmt.Errorf("number of unlock params must be equal to the number of inputs")
		}

		if tx.tree.NumElements(Path(core.TxEndorsements)) > core.MaxNumberOfEndorsements {
			return fmt.Errorf("number of endorsements exceeds limit of %d", core.MaxNumberOfEndorsements)
		}
		return nil
	}
}

func CheckUniqueness() TxValidationOption {
	return func(tx *Transaction) error {
		var err error
		// check if inputs are unique
		inps := make(map[core.OutputID]struct{})
		tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
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
		endorsements := make(map[core.TransactionID]struct{})
		tx.ForEachEndorsement(func(i byte, txid *core.TransactionID) bool {
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
		tx.ForEachInput(func(_ byte, oid *core.OutputID) bool {
			if !core.ValidTimePace(oid.Timestamp(), ts) {
				err = fmt.Errorf("timestamp of input violates time pace constraint: %s", oid.Short())
				return false
			}
			return true
		})
		return err
	}
}

// CheckEndorsements endorsed transactions must be sequencer transaction from the current slot
func CheckEndorsements() TxValidationOption {
	return func(tx *Transaction) error {
		var err error

		if !tx.IsSequencerMilestone() && tx.NumEndorsements() > 0 {
			return fmt.Errorf("non-sequencer tx can't contain endorsements: %s", tx.IDShort())
		}

		txSlot := tx.Timestamp().TimeSlot()
		tx.ForEachEndorsement(func(_ byte, endorsedTxID *core.TransactionID) bool {
			if !endorsedTxID.SequencerFlagON() {
				err = fmt.Errorf("tx %s contains endorsement of non-sequencer transaction: %s", tx.IDShort(), endorsedTxID.StringShort())
				return false
			}
			if endorsedTxID.TimeSlot() != txSlot {
				err = fmt.Errorf("tx %s can't endorse tx from another slot: %s", tx.IDShort(), endorsedTxID.StringShort())
				return false
			}
			return true
		})
		return err
	}
}

// ScanOutputs validation option scans all inputs, enforces existence of mandatory constrains and computes total of outputs
func ScanOutputs() TxValidationOption {
	return func(tx *Transaction) error {
		numOutputs := tx.tree.NumElements(Path(core.TxOutputs))
		ret := make([]*core.Output, numOutputs)
		var err error
		var amount, totalAmount core.Amount

		path := []byte{core.TxOutputs, 0}
		for i := range ret {
			path[1] = byte(i)
			ret[i], amount, _, err = core.OutputFromBytesMain(tx.tree.BytesAtPath(path))
			if err != nil {
				return fmt.Errorf("scanning output #%d: '%v'", i, err)
			}
			if amount > math.MaxUint64-totalAmount {
				return fmt.Errorf("scanning output #%d: 'arithmetic overflow while calculating total of outputs'", i)
			}
			totalAmount += amount
		}
		if tx.totalAmount != totalAmount {
			return fmt.Errorf("wrong total produced amount value")
		}
		return nil
	}
}

func CheckSizeOfOutputCommitment() TxValidationOption {
	return func(tx *Transaction) error {
		data := tx.tree.BytesAtPath(Path(core.TxInputCommitment))
		if len(data) != 32 {
			return fmt.Errorf("input commitment must be 32-bytes long")
		}
		return nil
	}
}

func ValidateOptionWithFullContext(inputLoaderByIndex func(i byte) (*core.Output, error)) TxValidationOption {
	return func(tx *Transaction) error {
		var ctx *TransactionContext
		var err error
		if __printLogOnFail.Load() {
			ctx, err = ContextFromTransaction(tx, inputLoaderByIndex, TraceOptionAll)
		} else {
			ctx, err = ContextFromTransaction(tx, inputLoaderByIndex)
		}
		if err != nil {
			return err
		}
		return ctx.Validate()
	}
}

func (tx *Transaction) ID() *core.TransactionID {
	ret := core.NewTransactionID(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
	return &ret
}

func (tx *Transaction) IDString() string {
	return core.TransactionIDString(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
}

func (tx *Transaction) IDShort() string {
	return core.TransactionIDStringShort(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
}

func (tx *Transaction) IDVeryShort() string {
	return core.TransactionIDStringVeryShort(tx.timestamp, tx.txHash, tx.sequencerMilestoneFlag, tx.branchTransactionFlag)
}

func (tx *Transaction) TimeSlot() core.TimeSlot {
	return tx.timestamp.TimeSlot()
}

func (tx *Transaction) Hash() core.TransactionIDShort {
	return tx.txHash
}

func (tx *Transaction) ByteSize() int {
	return tx.byteSize
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
		seqMeta.SequencerID.VeryShort(),
		tx.NumInputs(),
		tx.NumProducedOutputs(),
		seqMeta.SequencerOutputData.AmountOnChain,
		seqMeta.StemOutputData != nil,
	)
}

func (tx *Transaction) IsBranchTransaction() bool {
	return tx.sequencerMilestoneFlag && tx.branchTransactionFlag
}

func (tx *Transaction) StemOutputData() *core.StemLock {
	if tx.sequencerTransactionData != nil {
		return tx.sequencerTransactionData.StemOutputData
	}
	return nil
}

func (m *SequencerTransactionData) Short() string {
	return fmt.Sprintf("SEQ(%s)", m.SequencerID.VeryShort())
}

func (tx *Transaction) SequencerOutput() *core.OutputWithID {
	util.Assertf(tx.IsSequencerMilestone(), "tx.IsSequencerMilestone()")
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
}

func (tx *Transaction) StemOutput() *core.OutputWithID {
	util.Assertf(tx.IsBranchTransaction(), "tx.IsBranchTransaction()")
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().StemOutputIndex)
}

func (tx *Transaction) SenderAddress() core.AddressED25519 {
	return tx.sender
}

func (tx *Transaction) Timestamp() core.LogicalTime {
	return tx.timestamp
}

func (tx *Transaction) TimestampTime() time.Time {
	return tx.timestamp.Time()
}

func (tx *Transaction) TotalAmount() core.Amount {
	return tx.totalAmount
}

func EssenceBytesFromTransactionDataTree(txTree *lazybytes.Tree) []byte {
	return common.Concat(
		txTree.BytesAtPath([]byte{core.TxInputIDs}),
		txTree.BytesAtPath([]byte{core.TxOutputs}),
		txTree.BytesAtPath([]byte{core.TxTimestamp}),
		txTree.BytesAtPath([]byte{core.TxSequencerAndStemOutputIndices}),
		txTree.BytesAtPath([]byte{core.TxInputCommitment}),
		txTree.BytesAtPath([]byte{core.TxEndorsements}),
	)
}

func (tx *Transaction) Bytes() []byte {
	return tx.tree.Bytes()
}

func (tx *Transaction) EssenceBytes() []byte {
	return EssenceBytesFromTransactionDataTree(tx.tree)
}

func (tx *Transaction) NumProducedOutputs() int {
	return tx.tree.NumElements(Path(core.TxOutputs))
}

func (tx *Transaction) NumInputs() int {
	return tx.tree.NumElements(Path(core.TxInputIDs))
}

func (tx *Transaction) NumEndorsements() int {
	return tx.tree.NumElements(Path(core.TxEndorsements))
}

func (tx *Transaction) MustOutputDataAt(idx byte) []byte {
	return tx.tree.BytesAtPath(common.Concat(core.TxOutputs, idx))
}

func (tx *Transaction) MustProducedOutputAt(idx byte) *core.Output {
	ret, err := core.OutputFromBytesReadOnly(tx.MustOutputDataAt(idx))
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputAt(idx byte) (*core.Output, error) {
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
	}
	out, err := core.OutputFromBytesReadOnly(tx.MustOutputDataAt(idx))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (tx *Transaction) ProducedOutputWithIDAt(idx byte) (*core.OutputWithID, error) {
	ret, err := tx.ProducedOutputAt(idx)
	if err != nil {
		return nil, err
	}
	return &core.OutputWithID{
		ID:     tx.OutputID(idx),
		Output: ret,
	}, nil
}

func (tx *Transaction) MustProducedOutputWithIDAt(idx byte) *core.OutputWithID {
	ret, err := tx.ProducedOutputWithIDAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputs() []*core.OutputWithID {
	ret := make([]*core.OutputWithID, tx.NumProducedOutputs())
	for i := range ret {
		ret[i] = tx.MustProducedOutputWithIDAt(byte(i))
	}
	return ret
}

func (tx *Transaction) InputAt(idx byte) (ret core.OutputID, err error) {
	if int(idx) >= tx.NumInputs() {
		return [33]byte{}, fmt.Errorf("InputAt: wrong input index")
	}
	data := tx.tree.BytesAtPath(common.Concat(core.TxInputIDs, idx))
	ret, err = core.OutputIDFromBytes(data)
	return
}

func (tx *Transaction) MustInputAt(idx byte) core.OutputID {
	ret, err := tx.InputAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) MustOutputIndexOfTheInput(inputIdx byte) byte {
	return core.MustOutputIndexFromIDBytes(tx.tree.BytesAtPath(common.Concat(core.TxInputIDs, inputIdx)))
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
	return ret.Short()
}

func (tx *Transaction) Inputs() []core.OutputID {
	ret := make([]core.OutputID, tx.NumInputs())
	for i := range ret {
		ret[i] = tx.MustInputAt(byte(i))
	}
	return ret
}

func (tx *Transaction) ConsumedOutputAt(idx byte, fetchOutput func(id *core.OutputID) ([]byte, bool)) (*core.OutputDataWithID, error) {
	oid, err := tx.InputAt(idx)
	if err != nil {
		return nil, err
	}
	ret, ok := fetchOutput(&oid)
	if !ok {
		return nil, fmt.Errorf("can't fetch output %s", oid.Short())
	}
	return &core.OutputDataWithID{
		ID:         oid,
		OutputData: ret,
	}, nil
}

func (tx *Transaction) EndorsementAt(idx byte) core.TransactionID {
	data := tx.tree.BytesAtPath(common.Concat(core.TxEndorsements, idx))
	ret, err := core.TransactionIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

// HashInputsAndEndorsements blake2b of concatenated input IDs and endorsements
// independent on any other tz data but inputs
func (tx *Transaction) HashInputsAndEndorsements() [32]byte {
	var buf bytes.Buffer

	buf.Write(tx.tree.BytesAtPath(Path(core.TxInputIDs)))
	buf.Write(tx.tree.BytesAtPath(Path(core.TxEndorsements)))

	return blake2b.Sum256(buf.Bytes())
}

func (tx *Transaction) ForEachInput(fun func(i byte, oid *core.OutputID) bool) {
	tx.tree.ForEach(func(i byte, data []byte) bool {
		oid, err := core.OutputIDFromBytes(data)
		util.Assertf(err == nil, "ForEachInput @ %d: %v", i, err)
		return fun(i, &oid)
	}, Path(core.TxInputIDs))
}

func (tx *Transaction) ForEachEndorsement(fun func(idx byte, txid *core.TransactionID) bool) {
	tx.tree.ForEach(func(i byte, data []byte) bool {
		txid, err := core.TransactionIDFromBytes(data)
		util.Assertf(err == nil, "ForEachEndorsement @ %d: %v", i, err)
		return fun(i, &txid)
	}, Path(core.TxEndorsements))
}

func (tx *Transaction) ForEachOutputData(fun func(idx byte, oData []byte) bool) {
	tx.tree.ForEach(func(i byte, data []byte) bool {
		return fun(i, data)
	}, Path(core.TxOutputs))
}

// ForEachProducedOutput traverses all produced outputs
// Inside callback function the correct outputID must be obtained with OutputID(idx byte) core.OutputID
// because stem output ID has special form
func (tx *Transaction) ForEachProducedOutput(fun func(idx byte, o *core.Output, oid *core.OutputID) bool) {
	tx.ForEachOutputData(func(idx byte, oData []byte) bool {
		o, _ := core.OutputFromBytesReadOnly(oData)
		oid := tx.OutputID(idx)
		if !fun(idx, o, &oid) {
			return false
		}
		return true
	})
}

func (tx *Transaction) InputTransactionIDs() map[core.TransactionID]struct{} {
	ret := make(map[core.TransactionID]struct{})
	tx.ForEachInput(func(_ byte, oid *core.OutputID) bool {
		ret[oid.TransactionID()] = struct{}{}
		return true
	})
	return ret
}

func (tx *Transaction) SequencerAndStemOutputIndices() (byte, byte) {
	ret := tx.tree.BytesAtPath([]byte{core.TxSequencerAndStemOutputIndices})
	util.Assertf(len(ret) == 2, "len(ret)==2")
	return ret[0], ret[1]
}

func (tx *Transaction) OutputID(idx byte) core.OutputID {
	return core.NewOutputID(tx.ID(), idx)
}

func (tx *Transaction) InflationAmount() uint64 {
	if tx.IsBranchTransaction() {
		return tx.SequencerTransactionData().StemOutputData.InflationAmount
	}
	return 0
}

func OutputWithIDFromTransactionBytes(txBytes []byte, idx byte) (*core.OutputWithID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
	}
	return tx.ProducedOutputWithIDAt(idx)
}

func OutputsWithIDFromTransactionBytes(txBytes []byte) ([]*core.OutputWithID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return nil, err
	}

	ret := make([]*core.OutputWithID, tx.NumProducedOutputs())
	for idx := 0; idx < tx.NumProducedOutputs(); idx++ {
		ret[idx], err = tx.ProducedOutputWithIDAt(byte(idx))
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (tx *Transaction) ToString(fetchOutput func(oid *core.OutputID) ([]byte, bool)) string {
	ctx, err := ContextFromTransaction(tx, func(i byte) (*core.Output, error) {
		oid, err1 := tx.InputAt(i)
		if err1 != nil {
			return nil, err1
		}
		oData, ok := fetchOutput(&oid)
		if !ok {
			return nil, fmt.Errorf("output %s has not been found", oid.Short())
		}
		o, err1 := core.OutputFromBytesReadOnly(oData)
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

func (tx *Transaction) InputLoaderByIndex(fetchOutput func(oid *core.OutputID) ([]byte, bool)) func(byte) (*core.Output, error) {
	return func(idx byte) (*core.Output, error) {
		inp := tx.MustInputAt(idx)
		odata, ok := fetchOutput(&inp)
		if !ok {
			return nil, fmt.Errorf("can't load input #%d: %s", idx, inp.String())
		}
		o, err := core.OutputFromBytesReadOnly(odata)
		if err != nil {
			return nil, fmt.Errorf("can't load input #%d: %s, '%v'", idx, inp.String(), err)
		}
		return o, nil
	}
}

func (tx *Transaction) InputLoaderFromState(rdr general.StateReader) func(idx byte) (*core.Output, error) {
	return tx.InputLoaderByIndex(func(oid *core.OutputID) ([]byte, bool) {
		return rdr.GetUTXO(oid)
	})
}

// SequencerChainPredecessorOutputID returns chain predecessor output ID
// If it is chain origin, it returns nil
// Otherwise, it may or may not be a sequencer ID
func (tx *Transaction) SequencerChainPredecessorOutputID() *core.OutputID {
	seqMeta := tx.SequencerTransactionData()
	util.Assertf(seqMeta != nil, "SequencerChainPredecessorOutputID: must be a sequencer transaction")

	if seqMeta.SequencerOutputData.ChainConstraint.IsOrigin() {
		return nil
	}

	ret, err := tx.InputAt(seqMeta.SequencerOutputData.ChainConstraint.PredecessorInputIndex)
	util.AssertNoError(err)
	// The following is ensured by the 'chain' and 'sequencer' constraints on the transaction
	// Returned predecessor outputID must be:
	// - if the transaction is branch tx, then it return tx ID which may or may not be a sequencer transaction ID
	// - if the transaction is not a branch tx, it must always return sequencer tx ID (which may or may not be a branch)
	return &ret
}

func (tx *Transaction) FindChainOutput(chainID core.ChainID) *core.OutputWithID {
	var ret *core.OutputWithID
	tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		cc, idx := o.ChainConstraint()
		if idx == 0xff {
			return true
		}
		cID := cc.ID
		if cc.IsOrigin() {
			cID = core.OriginChainID(oid)
		}
		if cID == chainID {
			ret = &core.OutputWithID{
				ID:     *oid,
				Output: o,
			}
			return false
		}
		return true
	})
	return ret
}

func (tx *Transaction) FindStemProducedOutput() *core.OutputWithID {
	if !tx.IsBranchTransaction() {
		return nil
	}
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().StemOutputIndex)
}

func (tx *Transaction) EndorsementsVeryShort() string {
	ret := make([]string, tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid *core.TransactionID) bool {
		ret[idx] = txid.StringVeryShort()
		return true
	})
	return strings.Join(ret, ", ")
}

func (tx *Transaction) ProducedOutputsToString() string {
	ret := make([]string, 0)
	tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		ret = append(ret, fmt.Sprintf("  %d :", idx), o.ToString("    "))
		return true
	})
	return strings.Join(ret, "\n")
}

func (tx *Transaction) StateMutations() *multistate.Mutations {
	ret := multistate.NewMutations()
	tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		ret.InsertDelOutputMutation(*oid)
		return true
	})
	tx.ForEachProducedOutput(func(_ byte, o *core.Output, oid *core.OutputID) bool {
		ret.InsertAddOutputMutation(*oid, o)
		return true
	})
	ret.InsertAddTxMutation(*tx.ID(), tx.TimeSlot())
	return ret
}

func (tx *Transaction) Lines(inputLoaderByIndex func(i byte) (*core.Output, error), prefix ...string) *lines.Lines {
	ctx, err := ContextFromTransaction(tx, inputLoaderByIndex)
	if err != nil {
		ret := lines.New(prefix...)
		ret.Add("can't create context of transaction %s: '%v'", tx.IDShort(), err)
		return ret
	}
	return ctx.Lines(prefix...)
}
