package txbuilder

import (
	"crypto"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

type (
	TxBuilder struct {
		ConsumedOutputs []*ledger.Output
		TransactionData *transactionData
	}

	transactionData struct {
		InputIDs         []*base.OutputID
		Outputs          []*ledger.Output
		UnlockBlocks     []*UnlockParams
		SignatureData    []byte
		Timestamp        base.LedgerTime
		InputCommitment  [32]byte
		Endorsements     []base.TransactionID
		ExplicitBaseline *base.TransactionID
		OtherData        [][]byte
		ledger.SequencerDataBytes
	}

	UnlockParams struct {
		array *tuples.TupleEditable
	}
)

func New() *TxBuilder {
	return &TxBuilder{
		ConsumedOutputs: make([]*ledger.Output, 0),
		TransactionData: &transactionData{
			InputIDs:           make([]*base.OutputID, 0),
			Outputs:            make([]*ledger.Output, 0),
			UnlockBlocks:       make([]*UnlockParams, 0),
			SequencerDataBytes: ledger.MustSequencerDataBytesFromBytes([]byte{0xff, 0xff, 0xff, 0xff}),
			Timestamp:          base.NilLedgerTime,
			InputCommitment:    [32]byte{},
			Endorsements:       make([]base.TransactionID, 0),
			OtherData:          make([][]byte, 0),
		},
	}
}

func (txb *TxBuilder) NumInputs() int {
	ret := len(txb.ConsumedOutputs)
	util.Assertf(ret == len(txb.TransactionData.InputIDs), "ret==len(ctx.Transaction.InputIDs)")
	return ret
}

func (txb *TxBuilder) NumOutputs() int {
	return len(txb.TransactionData.Outputs)
}

func (txb *TxBuilder) ConsumeOutput(out *ledger.Output, oid base.OutputID) (byte, error) {
	if txb.NumInputs() >= 256 {
		return 0, fmt.Errorf("too many consumed outputs")
	}
	txb.ConsumedOutputs = append(txb.ConsumedOutputs, out)
	txb.TransactionData.InputIDs = append(txb.TransactionData.InputIDs, &oid)
	txb.TransactionData.UnlockBlocks = append(txb.TransactionData.UnlockBlocks, NewUnlockBlock())

	return byte(len(txb.ConsumedOutputs) - 1), nil
}

func (txb *TxBuilder) ConsumeTagAlongOutputUnlock(o *ledger.Output, oid base.OutputID, chainInIdx byte) (byte, error) {
	lock := o.Lock()
	if lock.Name() != ledger.ChainLockName {
		return 0, fmt.Errorf("not a chain lock")
	}
	idx, err := txb.ConsumeOutput(o, oid)
	if err != nil {
		return 0, err
	}
	txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(chainInIdx))
	return idx, nil
}

func (txb *TxBuilder) ConsumeOutputsUnlock(outs ...*ledger.OutputWithID) (uint64, base.LedgerTime, error) {
	var err error
	if len(outs) >= 256 {
		return 0, base.LedgerTime{}, fmt.Errorf("ConsumeOutputsUnlock: number of inputs can't be greater than 256")
	}
	total := uint64(0)
	maxTs := base.LedgerTime{}
	for i, o := range outs {
		if o.Output.Lock().Name() != ledger.SigLockName {
			return 0, base.LedgerTime{}, fmt.Errorf("ConsumeOutputsUnlock: only SigLock locks are allowed")
		}
		if o.Output.TokenBalance() >= math.MaxUint64-total {
			return 0, base.LedgerTime{}, fmt.Errorf("ConsumeOutputsUnlock: amount overflow")
		}
		if _, err = txb.ConsumeOutput(o.Output, o.ID); err != nil {
			return 0, base.LedgerTime{}, err
		}
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return 0, base.LedgerTime{}, err
			}
		}
		total += o.Output.TokenBalance()
		maxTs = base.MaximumTime(maxTs, o.Timestamp())
	}
	return total, maxTs, nil
}

// ConsumeOutputsNoUnlock returns total sum and maximal timestamp
func (txb *TxBuilder) ConsumeOutputsNoUnlock(outs ...*ledger.OutputWithID) (uint64, base.LedgerTime, error) {
	retTotal := uint64(0)
	retTs := base.NilLedgerTime
	for _, o := range outs {
		if _, err := txb.ConsumeOutput(o.Output, o.ID); err != nil {
			return 0, base.NilLedgerTime, err
		}
		// safe arithmetics
		if o.Output.TokenBalance() > math.MaxUint64-retTotal {
			return 0, base.NilLedgerTime, fmt.Errorf("arithmetic overflow when calculating total ")
		}
		retTotal += o.Output.TokenBalance()
		retTs = base.MaximumTime(retTs, o.Timestamp())
	}
	return retTotal, retTs, nil
}

func (txb *TxBuilder) PutUnlockParams(inputIndex, constraintIndex byte, unlockParamData []byte, additionalBytes ...byte) {
	txb.TransactionData.UnlockBlocks[inputIndex].array.MustPutAtIdxWithPadding(constraintIndex, common.Concat(unlockParamData, additionalBytes))
}

// PutSignatureUnlock marker 0xff references the signature of the transaction.
// It can be distinguished from any reference because it cannot be strictly less than any other reference
func (txb *TxBuilder) PutSignatureUnlock(inputIndex byte, additionalBytes ...byte) {
	txb.PutUnlockParams(inputIndex, ledger.ConstraintIndexLock, append([]byte{0xff}, additionalBytes...))
}

// PutUnlockReference references some preceding output
func (txb *TxBuilder) PutUnlockReference(inputIndex, constraintIndex, referencedInputIndex byte) error {
	if referencedInputIndex >= inputIndex {
		return fmt.Errorf("referenced input index must be strongly less than the unlocked output index")
	}
	txb.PutUnlockParams(inputIndex, constraintIndex, []byte{referencedInputIndex})
	return nil
}

func (txb *TxBuilder) PutStandardInputUnlocks(n int) error {
	util.Assertf(n > 0, "n > 0")
	txb.PutSignatureUnlock(0)
	for i := 1; i < n; i++ {
		if err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
			return err
		}
	}
	return nil
}

func (txb *TxBuilder) PushEndorsements(txid ...base.TransactionID) {
	txb.TransactionData.Endorsements = append(txb.TransactionData.Endorsements, txid...)
}

func (txb *TxBuilder) PutExplicitBaseline(txid *base.TransactionID) {
	txb.TransactionData.ExplicitBaseline = txid
}

// ProduceOutput adds produced output to the tx builder. Chacks storage deposit
func (txb *TxBuilder) ProduceOutput(o *ledger.Output) (byte, error) {
	if err := o.EnoughAmountForStorageDeposit(); err != nil {
		return 0, fmt.Errorf("TxBuilder:ProduceOutput: %v", err)
	}
	o.MustValidOutput()
	if txb.NumOutputs() >= 256 {
		return 0, fmt.Errorf("too many produced outputs")
	}
	txb.TransactionData.Outputs = append(txb.TransactionData.Outputs, o)
	return byte(len(txb.TransactionData.Outputs) - 1), nil
}

func (txb *TxBuilder) ProduceOutputs(outs ...*ledger.Output) (uint64, error) {
	total := uint64(0)
	for _, o := range outs {
		if _, err := txb.ProduceOutput(o); err != nil {
			return 0, err
		}
		total += o.TokenBalance()
	}
	return total, nil
}

func (txb *TxBuilder) ConsumedAmount() uint64 {
	ret := uint64(0)
	for _, o := range txb.ConsumedOutputs {
		ret += o.TokenBalance()
	}
	return ret
}

func (txb *TxBuilder) Transaction() (*transaction.Transaction, error) {
	txBytes, _, txString, err := txb.BytesWithValidation()
	if err != nil {
		return nil, fmt.Errorf("%w\n==== failing transaction ====\n%s", err, txString)
	}
	return transaction.ParseWithPartialValidation(txBytes)
}

// BuildTransactionWithValidation builds transaction, parses it and validates with full context.
// In case validation fails with full cotext, it may return err != nil and tx != nil
func (txb *TxBuilder) BuildTransactionWithValidation() (*transaction.Transaction, error) {
	txBytes := txb.TransactionData.Bytes()
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return nil, fmt.Errorf("TxBuilder resulted in invalid transaction: %v", err)
	}
	if err = tx.SetFullContext(txb.LoadInput); err != nil {
		return tx, fmt.Errorf("TxBuilder resulted in invalid transaction: %v", err)
	}
	if err = tx.ValidateFullContext(); err != nil {
		return tx, fmt.Errorf("TxBuilder resulted in invalid transaction: %v", err)
	}
	return tx, nil
}

func (txb *TxBuilder) BytesWithValidation() ([]byte, base.TransactionID, string, error) {
	tx, err := txb.BuildTransactionWithValidation()
	if err != nil {
		if tx == nil {
			return nil, base.TransactionID{}, "", err
		}
		return tx.Bytes(), tx.ID(), tx.String(), err
	}
	return tx.Bytes(), tx.ID(), tx.String(), nil
}

func (txb *TxBuilder) ProducedAmount() (uint64, uint64) {
	retTotal := uint64(0)
	retInflation := uint64(0)
	for _, o := range txb.TransactionData.Outputs {
		retTotal += o.TokenBalance()
		retInflation += o.Inflation()
	}
	return retTotal, retInflation
}

// InsertSimpleChainTransition inserts a simple chain transition. Takes output with chain constraint from parameters,
// Produces identical output, only modifies timestamp. Unlocks chain-input lock with signature reference
func (txb *TxBuilder) InsertSimpleChainTransition(inChainData *ledger.OutputDataWithChainID, _ base.LedgerTime) error {
	// Use input's slot for parsing (output was created at that slot)
	chainIN, err := ledger.OutputFromBytesWithLib(inChainData.Data, ledger.L(inChainData.ID.Slot()))
	if err != nil {
		return err
	}
	cc := chainIN.ChainConstraint()
	if cc == nil {
		return fmt.Errorf("can't find chain constrain in the output")
	}
	predecessorOutputIndex, err := txb.ConsumeOutput(chainIN, inChainData.ID)
	if err != nil {
		return err
	}
	successor := ledger.NewChainConstraint(inChainData.ChainID, predecessorOutputIndex, cc.OriginSlot, cc.CumulativeChainInflation, cc.CumulativeBranchBonus, cc.TransitionCounter+1, cc.BranchCounter)
	chainOut := chainIN.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
	})
	successorOutputIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return err
	}
	txb.PutUnlockParams(predecessorOutputIndex, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(successorOutputIndex))
	txb.PutSignatureUnlock(successorOutputIndex)

	return nil
}

// LoadInput returns clone of the consumed output
func (txb *TxBuilder) LoadInput(i byte) (*ledger.Output, error) {
	if int(i) >= len(txb.ConsumedOutputs) {
		return nil, fmt.Errorf("can't load input #%d", i)
	}
	return txb.ConsumedOutputs[i].Clone(), nil
}

// CalcFrozenCoverageDelta sums up frozen coverage vectors of all delegation outputs
func (txb *TxBuilder) CalcFrozenCoverageDelta() ([]int64, error) {
	lib := ledger.L(txb.TransactionData.Timestamp.Slot)
	sum := make([]int64, lib.MaxFrozenEpochs+2)
	for _, o := range txb.TransactionData.Outputs {
		if o.Lock().Name() == ledger.DelegateLockName {
			if overflow := o.Amounts().AddToVector(sum); overflow {
				return nil, fmt.Errorf("CalcFrozenCoverageDelta: arithmetic overflow")
			}
		}
	}
	return sum[2 : 2+lib.MaxFrozenEpochs], nil
}

func (txb *TxBuilder) MustPutFrozenCoverage(producedOutputIdx byte, frozenCoverageDeltaVector []int64, targetTs base.LedgerTime) {
	o := txb.TransactionData.Outputs[producedOutputIdx]

	lib := ledger.L(targetTs.Slot)
	a := make([]int64, lib.MaxFrozenEpochs+2)
	a[0] = int64(o.TokenBalance())
	a[1] = int64(o.Inflation())
	copy(a[2:], frozenCoverageDeltaVector)

	// find the predecessor and adjust its vector
	cc := o.ChainConstraint()
	util.Assertf(cc != nil, "MustPutFrozenCoverage: inconsistency 1")
	oPred := txb.ConsumedOutputs[cc.PredecessorInputIndex]
	predVector := oPred.Amounts().FrozenCoverageVector(byte(lib.MaxFrozenEpochs))
	predTs := txb.TransactionData.InputIDs[cc.PredecessorInputIndex].Timestamp()
	predVectorAdjusted := lib.AdjustFrozenCoverageVector(cc.ChainID, predVector, predTs, targetTs)
	for i := range frozenCoverageDeltaVector {
		a[i+2] += predVectorAdjusted[i]
	}

	txb.TransactionData.Outputs[producedOutputIdx] = o.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(ledger.NewAmounts(a[:]...).Bytes(), ledger.ConstraintIndexAmounts)
	})
}

func (tx *transactionData) ToTuple() *tuples.Tuple {
	unlockParams := tuples.EmptyTupleEditable(256)
	inputIDs := tuples.EmptyTupleEditable(256)
	outputs := tuples.EmptyTupleEditable(256)
	endorsements := tuples.EmptyTupleEditable(256)
	var explicitBaseline []byte
	if tx.ExplicitBaseline != nil {
		explicitBaseline = tx.ExplicitBaseline[:]
	}

	for _, b := range tx.UnlockBlocks {
		unlockParams.MustPush(b.Bytes())
	}
	for _, oid := range tx.InputIDs {
		inputIDs.MustPush(oid[:])
	}
	for _, o := range tx.Outputs {
		outputs.MustPush(o.Bytes())
	}
	for _, e := range tx.Endorsements {
		endorsements.MustPush(e.Bytes())
	}

	total := uint64(0)
	for _, o := range tx.Outputs {
		total += o.TokenBalance()
	}
	elems := make([]any, ledger.TxTreeTupleNumElements)
	// TxVersion: uint16 big-endian, library upgrade index for the transaction's slot
	versionBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(versionBytes, ledger.L(tx.Timestamp.Slot).UpgradeIndex())
	elems[ledger.TxVersion] = versionBytes
	elems[ledger.TxConstraints] = nil
	elems[ledger.TxTimestamp] = tx.Timestamp.Bytes()
	if tx.SequencerOutputIndex != 0xff {
		elems[ledger.TxSequencerDataBytes] = tx.SequencerDataBytes.Bytes()
	}
	elems[ledger.TxSignatureData] = tx.SignatureData
	elems[ledger.TxInputCommitment] = tx.InputCommitment[:]
	elems[ledger.TxExplicitBaseline] = explicitBaseline
	elems[ledger.TxInputIDs] = inputIDs
	elems[ledger.TxUnlockData] = unlockParams
	elems[ledger.TxOutputs] = outputs
	elems[ledger.TxEndorsements] = endorsements
	elems[ledger.TxOtherData] = tuples.MakeTupleFromDataElements(tx.OtherData...)
	return tuples.MakeTupleFromSerializableElements(elems...)
}

func (tx *transactionData) Bytes() []byte {
	return tx.ToTuple().Bytes()
}

var rnd = rand.New(rand.NewSource(time.Now().UnixNano()))

func (txb *TxBuilder) SignED25519(privKey ed25519.PrivateKey) {
	txid, err := transaction.TxIDFromTransactionDataTree(txb.TransactionData.ToTuple().AsTree())
	util.AssertNoError(err)
	sig, err := privKey.Sign(rnd, txid[:], crypto.Hash(0))
	util.AssertNoError(err)
	pubKey := privKey.Public().(ed25519.PublicKey)
	// signature data in the transaction is <sig type byte> + <signature proper> + <public key>
	txb.TransactionData.SignatureData = common.Concat(base.SignatureTypeED25519, sig, []byte(pubKey))
}

type (
	TransferData struct {
		SenderPrivateKey ed25519.PrivateKey
		SenderPublicKey  ed25519.PublicKey
		SourceAccount    ledger.Controller
		Inputs           []*ledger.OutputWithID
		ChainOutput      *ledger.OutputWithChainID
		Timestamp        base.LedgerTime // takes ledger.TimeFromClockTime(time.Now()) if ledger.NilLedgerTime
		Lock             ledger.Lock
		Amount           uint64
		AdjustToMinimum  bool
		AddConstraints   [][]byte
		UnlockData       []*UnlockData
		Endorsements     []base.TransactionID
		ExplicitBaseline *base.TransactionID
		TagAlong         *TagAlongData
	}

	// MakeChainSuccTransactionParams contains parameters for building a chain transaction
	MakeChainSuccTransactionParams struct {
		// predecessor
		ChainInput *ledger.OutputWithChainID
		// timestamp of the target transaction
		Timestamp base.LedgerTime
		// some amount sent to the target lock. It can be a tag-along output. The remainder goes to the chain
		TagAlongSequencer base.ChainID
		TagAlongFee       uint64
		// chain controller
		PrivateKey ed25519.PrivateKey
		// enforce transaction is profitable
		EnforceProfitability bool
		// return input loader function
		ReturnInputLoader bool
	}

	TagAlongData struct {
		SeqID  base.ChainID
		Amount uint64
	}

	UnlockData struct {
		OutputIndex     byte
		ConstraintIndex byte
		Data            []byte
	}
)

func NewTransferData(senderKey ed25519.PrivateKey, sourceAccount ledger.Controller, ts base.LedgerTime) *TransferData {
	sourcePubKey := senderKey.Public().(ed25519.PublicKey)
	if util.IsNil(sourceAccount) {
		sourceAccount = ledger.SigLockFromED25519PublicKey(sourcePubKey)
	}
	return &TransferData{
		SenderPrivateKey: senderKey,
		SenderPublicKey:  sourcePubKey,
		SourceAccount:    sourceAccount,
		Timestamp:        ts,
		AddConstraints:   make([][]byte, 0),
		UnlockData:       make([]*UnlockData, 0),
		Endorsements:     make([]base.TransactionID, 0),
	}
}

func (t *TransferData) WithTargetLock(lock ledger.Lock) *TransferData {
	t.Lock = lock
	return t
}

func (t *TransferData) WithAmount(amount uint64, adjustToMinimum ...bool) *TransferData {
	t.Amount = amount
	t.AdjustToMinimum = len(adjustToMinimum) > 0 && adjustToMinimum[0]
	return t
}

func (t *TransferData) WithTagAlong(target base.ChainID, fee uint64) *TransferData {
	t.TagAlong = &TagAlongData{
		SeqID:  target,
		Amount: fee,
	}
	return t
}

func (t *TransferData) WithConstraintBinary(constr []byte, idx ...byte) *TransferData {
	if len(idx) == 0 {
		t.AddConstraints = append(t.AddConstraints, constr)
	} else {
		util.Assertf(idx[0] == 0xff || idx[0] < ledger.ConstraintIndexChain, "WithConstraintBinary: wrong constraint index")
		t.AddConstraints[idx[0]] = constr
	}
	return t
}

func (t *TransferData) WithConstraint(constr ledger.Constraint, idx ...byte) *TransferData {
	return t.WithConstraintBinary(constr.Bytes(), idx...)
}

func (t *TransferData) WithConstraintAtIndex(constr ledger.Constraint) *TransferData {
	return t.WithConstraintBinary(constr.Bytes())
}

func (t *TransferData) UseOutputsAsInputs(outs ...*ledger.OutputWithID) error {
	for _, o := range outs {
		// Output must be sig-locked by the same holder as t.SourceAccount,
		// or chain-locked by the same chain — i.e. matching ControllerID.
		lock := o.Output.Lock()
		if c, ok := lock.(ledger.Controller); !ok || !ledger.EqualControllers(t.SourceAccount, c) {
			return fmt.Errorf("UseOutputsAsInputs: output can't be consumed. Source account: %s, output: %s", t.SourceAccount.String(), o.Output.ToString())
		}
	}
	t.Inputs = outs
	return nil
}

func (t *TransferData) MustWithInputs(outs ...*ledger.OutputWithID) *TransferData {
	util.AssertNoError(t.UseOutputsAsInputs(outs...))
	return t
}

func (t *TransferData) WithChainOutput(out *ledger.OutputWithChainID) *TransferData {
	t.ChainOutput = out
	return t
}

func (t *TransferData) WithUnlockData(consumedOutputIndex, constraintIndex byte, data []byte) *TransferData {
	t.UnlockData = append(t.UnlockData, &UnlockData{
		OutputIndex:     consumedOutputIndex,
		ConstraintIndex: constraintIndex,
		Data:            data,
	})
	return t
}

func (t *TransferData) WithEndorsements(ids ...base.TransactionID) *TransferData {
	t.Endorsements = ids
	return t
}

// TotalAdjustedAmount adjust amount to minimum storage deposit requirements
func (t *TransferData) TotalAdjustedAmount() uint64 {
	if !t.AdjustToMinimum {
		// not adjust. Will render wrong transaction if not enough tokens
		return t.Amount
	}

	outTentative := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(math.MaxUint64 / 2)).WithLock(t.Lock)
		for _, c := range t.AddConstraints {
			o.MustPushConstraint(c)
		}
	})

	minimumDeposit := ledger.MinimumStorageDeposit(outTentative)
	if t.Amount < minimumDeposit {
		return minimumDeposit
	}
	if t.TagAlong == nil {
		return t.Amount
	}
	return t.Amount + t.TagAlong.Amount
}

// MakeTransferTransaction makes transaction
// disableEndorsementChecking is an option to disable endorsement timestamp checking, i.e. it can produce
// tx with invalid endorsements. Used only for testing
func MakeTransferTransaction(par *TransferData, disableEndorsementValidation ...bool) ([]byte, error) {
	if par.Amount == 0 || par.Lock == nil {
		return nil, fmt.Errorf("MakeTransferTransaction: wrong amount or lock")
	}

	var err error
	var ret []byte
	if par.ChainOutput == nil {
		ret, err = MakeSimpleTransferTransaction(par, disableEndorsementValidation...)
	} else {
		ret, err = MakeChainTransferTransaction(par, disableEndorsementValidation...)
	}
	return ret, err
}

func filterInputs(outs []*ledger.OutputWithID, amount uint64, ed25519Only ...bool) (uint64, []*ledger.OutputWithID, error) {
	ret := make([]*ledger.OutputWithID, 0, len(outs))
	availableTokens := uint64(0)

	filterNotED25519 := len(ed25519Only) > 0 && ed25519Only[0]

	for _, o := range outs {
		if filterNotED25519 && o.Output.Lock().Name() != ledger.SigLockName {
			continue
		}
		if len(ret) >= 256 {
			return 0, nil, fmt.Errorf("exceeded max number of consumed outputs 256")
		}
		ret = append(ret, o)
		availableTokens += o.Output.TokenBalance()
		if availableTokens >= amount {
			break
		}
	}
	return availableTokens, ret, nil
}

func MakeSimpleTransferTransaction(par *TransferData, disableEndorsementChecking ...bool) ([]byte, error) {
	txBytes, _, err := MakeSimpleTransferTransactionWithRemainder(par, disableEndorsementChecking...)
	return txBytes, err
}

func MakeSimpleTransferTransactionWithRemainder(par *TransferData, disableEndorsementChecking ...bool) ([]byte, *ledger.OutputWithID, error) {
	if !base.ValidTime(par.Timestamp) {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: wrong timestamp bytes 0x%s", par.Timestamp.Hex())
	}

	if par.ChainOutput != nil {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: ChainInput must be nil. Use MakeSimpleTransferTransaction instead")
	}
	if par.Lock == nil {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: target lock is not specified")
	}
	amount := par.TotalAdjustedAmount()
	availableTokens, consumedOuts, err := filterInputs(par.Inputs, amount)
	if err != nil {
		return nil, nil, err
	}

	if availableTokens < amount {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: not enough tokens in account %s: needed %d, got %d",
			par.SourceAccount.String(), par.Amount, availableTokens)
	}

	txb := New()
	checkTotal, inputTs, err := txb.ConsumeOutputsNoUnlock(consumedOuts...)
	if err != nil {
		return nil, nil, err
	}
	util.Assertf(availableTokens == checkTotal, "availableTokens == checkTotal")

	targetSlot := base.MaximumTime(inputTs, par.Timestamp).Slot
	adjustedTs := base.MaximumTime(inputTs, par.Timestamp).
		AddTicks(int(ledger.L(targetSlot).TransactionPace))

	util.Assertf(base.ValidTime(adjustedTs), "ledger.ValidTime(adjustedTs): ts bytes 0x%s", adjustedTs.Hex)

	for i := range par.Endorsements {
		if len(disableEndorsementChecking) == 0 || !disableEndorsementChecking[0] {
			if par.Endorsements[i].Slot() < adjustedTs.Slot {
				return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: can't endorse transaction from another time slot")
			}
		}
		if par.Endorsements[i].Slot() > adjustedTs.Slot {
			// adjust timestamp to the endorsed slot
			adjustedTs = base.T(par.Endorsements[i].Slot(), 0)
		}
	}

	mainOutput := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(par.Lock)
		if o.NumConstraints()+len(par.AddConstraints) >= 256 {
			err = fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: too many UTXO elements")
			return
		}
		for _, constr := range par.AddConstraints {
			o.MustPushConstraint(constr)
		}
	})
	if err != nil {
		return nil, nil, err
	}

	tagAlongFee := uint64(0)
	var tagAlongOut *ledger.Output
	if par.TagAlong != nil {
		tagAlongOut = ledger.NewTagAlongOutput(par.TagAlong.Amount, par.TagAlong.SeqID, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.SenderPrivateKey)))
		tagAlongFee = par.TagAlong.Amount
	}

	var remainderOut *ledger.Output
	var remainderIndex byte
	if availableTokens > amount+tagAlongFee {
		remainderOut = ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(availableTokens - amount - tagAlongFee)).
				WithLock(par.SourceAccount)
		})
	}
	if remainderOut != nil {
		if remainderIndex, err = txb.ProduceOutput(remainderOut); err != nil {
			return nil, nil, fmt.Errorf("making remainder output: %v", err)
		}
	}
	if _, err = txb.ProduceOutput(mainOutput); err != nil {
		return nil, nil, err
	}
	if tagAlongOut != nil {
		if _, err = txb.ProduceOutput(tagAlongOut); err != nil {
			return nil, nil, err
		}
	}

	for i := range consumedOuts {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			// always referencing the 0 output
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			util.AssertNoError(err)
		}
	}

	for _, un := range par.UnlockData {
		txb.PutUnlockParams(un.OutputIndex, un.ConstraintIndex, un.Data)
	}
	txb.TransactionData.Timestamp = adjustedTs
	txb.TransactionData.Endorsements = par.Endorsements
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.SenderPrivateKey)

	txBytes := txb.TransactionData.Bytes()
	var rem *ledger.OutputWithID
	if remainderOut != nil {
		if rem, err = transaction.OutputWithIDFromTransactionBytes(txBytes, remainderIndex); err != nil {
			return nil, nil, err
		}
	}
	return txBytes, rem, nil
}

// MakeChainSuccessorTransaction creates a transaction to continue a non-sequencer chain with inflation.
// Optionally withdraws some amount to the target lock, which can be used as a tag-along output
// Returns transaction and inflation amount
func MakeChainSuccessorTransaction(par *MakeChainSuccTransactionParams) ([]byte, uint64, func(i byte) (*ledger.Output, error), error) {
	var consumedOutputs []*ledger.Output
	if par.ReturnInputLoader {
		consumedOutputs = make([]*ledger.Output, 0)
	}
	errP := util.MakeErrFuncForPrefix("MakeChainSuccessorTransaction")

	if par.ChainInput.ID.IsSequencerTransaction() {
		// refuse to transition sequencer transactions
		return nil, 0, nil, errP("cannot extend sequencer output")
	}
	if par.Timestamp.IsSlotBoundary() {
		// refuse to produce transaction on the slot boundary
		return nil, 0, nil, errP("timestamp is on slot boundary")
	}

	// enforce validity time constraints taking into account transaction pace constraint
	lib := ledger.L(par.Timestamp.Slot)
	if tsIn := par.ChainInput.ID.Timestamp(); par.Timestamp.Before(par.ChainInput.ID.Timestamp().AddTicks(int(lib.TransactionPace))) {
		return nil, 0, nil, errP("timestamp %s is inconsistent with latest chain output timestamp %s", par.Timestamp.String(), tsIn.String())
	}

	// find chain constraint in the predecessor
	chainInConstraint := par.ChainInput.Output.ChainConstraint()
	if chainInConstraint == nil {
		return nil, 0, nil, errP("not a chain output: %s", par.ChainInput.ID.StringShort())
	}
	// calculate inflation amount and create inflation constraint
	inflationAmount := lib.ChainInflationOneSlot(
		par.ChainInput.Output.TokenBalance()+uint64(par.ChainInput.Output.FrozenCoverage(0)),
		par.ChainInput.Timestamp().Slot,
	)
	chainInAmount := par.ChainInput.Output.TokenBalance()
	if chainInAmount+inflationAmount <= par.TagAlongFee {
		// we do not handle complete withdrawal of funds from the chain
		return nil, 0, nil, errP("not enough tokens for tag-along fee %d", par.TagAlongFee)
	}

	chainOutAmount := chainInAmount + inflationAmount - par.TagAlongFee
	util.Assertf(chainOutAmount > 0, "chainOutAmount > 0")

	if par.EnforceProfitability {
		if chainOutAmount < chainInAmount {
			return nil, 0, nil, errP("chain transition is not profitable")
		}
	}

	txb := New()

	// consume predecessor
	chainPredIdx, err := txb.ConsumeOutput(par.ChainInput.Output, par.ChainInput.ID)
	if err != nil {
		return nil, 0, nil, errP(err)
	}
	if par.ReturnInputLoader {
		consumedOutputs = append(consumedOutputs, par.ChainInput.Output)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	chainID := chainInConstraint.ChainID
	if chainInConstraint.IsOrigin() {
		chainID = base.MakeOriginChainID(par.ChainInput.ID)
	}

	// make chain output
	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(int64(chainOutAmount), int64(inflationAmount))
		o.PutLock(par.ChainInput.Output.Lock())
		// put chain constraint at fixed index 2
		chainOutConstraint := ledger.NewChainConstraint(chainID, chainPredIdx, chainInConstraint.OriginSlot, chainInConstraint.CumulativeChainInflation+inflationAmount, chainInConstraint.CumulativeBranchBonus, chainInConstraint.TransitionCounter+1, chainInConstraint.BranchCounter)
		o.PutConstraint(chainOutConstraint.Bytes(), ledger.ConstraintIndexChain)
	})

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, 0, nil, errP(err)
	}
	// unlock chain input (chain constraint unlock + inflation (optionally)
	txb.PutUnlockParams(chainPredIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(chainOutIndex))

	if par.TagAlongFee > 0 {
		tagAlongOut := ledger.NewTagAlongOutput(par.TagAlongFee, par.TagAlongSequencer, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.PrivateKey)))
		if _, err = txb.ProduceOutput(tagAlongOut); err != nil {
			return nil, 0, nil, errP(err)
		}
	}

	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.PrivateKey)

	inputLoader := func(i byte) (*ledger.Output, error) {
		panic("MakeSequencerTransactionWithInputLoaderOld: par.ReturnInputLoader parameter must be set to true")
	}
	if par.ReturnInputLoader {
		inputLoader = func(i byte) (*ledger.Output, error) {
			return consumedOutputs[i], nil
		}
	}
	return txb.TransactionData.Bytes(), inflationAmount, inputLoader, nil
}

func MakeChainTransferTransaction(par *TransferData, disableEndorsementChecking ...bool) ([]byte, error) {
	if par.ChainOutput == nil {
		return nil, fmt.Errorf("ChainInput must be provided")
	}
	amount := par.TotalAdjustedAmount()
	// we are trying to consume non-chain outputs for the amount. Only if it is not enough, we are taking tokens from the chain
	availableTokens, consumedOuts, err := filterInputs(par.Inputs, amount)
	if err != nil {
		return nil, err
	}
	// count the chain output in
	availableTokens += par.ChainOutput.Output.TokenBalance()
	// some tokens must remain in the chain account
	if availableTokens <= amount {
		return nil, fmt.Errorf("not enough tokens in account %s: needed %d, got %d",
			par.SourceAccount.String(), par.Amount, availableTokens)
	}

	txb := New()

	chainInputIndex, err := txb.ConsumeOutput(par.ChainOutput.Output, par.ChainOutput.ID)
	util.Assertf(chainInputIndex == 0, "chainInputIndex == 0")
	if err != nil {
		return nil, err
	}
	checkAmount, inputTs, err := txb.ConsumeOutputsNoUnlock(consumedOuts...)
	if err != nil {
		return nil, err
	}
	util.Assertf(availableTokens == checkAmount+par.ChainOutput.Output.TokenBalance(), "availableTokens == checkAmount")
	targetSlot := base.MaximumTime(inputTs, par.ChainOutput.Timestamp()).Slot
	adjustedTs := base.MaximumTime(inputTs, par.ChainOutput.Timestamp()).
		AddTicks(int(ledger.L(targetSlot).TransactionPace))

	for i := range par.Endorsements {
		if len(disableEndorsementChecking) == 0 || !disableEndorsementChecking[0] {
			if par.Endorsements[i].Slot() < adjustedTs.Slot {
				return nil, fmt.Errorf("can't endorse transaction from another slot")
			}
		}
		if par.Endorsements[i].Slot() > adjustedTs.Slot {
			// adjust timestamp to the endorsed slot
			adjustedTs = base.T(par.Endorsements[i].Slot(), 0)
		}
	}

	chainConstr := ledger.NewChainConstraint(par.ChainOutput.ChainID, 0,
		par.ChainOutput.OriginSlot, par.ChainOutput.CumulativeChainInflation, par.ChainOutput.CumulativeBranchBonus, par.ChainOutput.TransitionCounter+1, par.ChainOutput.BranchCounter)
	util.Assertf(availableTokens > amount, "availableTokens > amount")

	chainSuccessorOutput := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(availableTokens - amount))
		o.WithLock(par.ChainOutput.Output.Lock())
		o.PutConstraint(chainConstr.Bytes(), ledger.ConstraintIndexChain)
	})
	outChainOutputIdx, err := txb.ProduceOutput(chainSuccessorOutput)
	if err != nil {
		return nil, err
	}

	mainOutput := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(par.Lock)
		if o.NumConstraints()+len(par.AddConstraints) >= 256 {
			err = fmt.Errorf("too many UTXO elements")
			return
		}
		for _, constr := range par.AddConstraints {
			o.MustPushConstraint(constr)
		}
	})
	if err != nil {
		return nil, err
	}

	if _, err = txb.ProduceOutput(mainOutput); err != nil {
		return nil, err
	}
	// unlock chain input
	txb.PutSignatureUnlock(outChainOutputIdx)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(outChainOutputIdx))

	// always reference chain input
	for i := range consumedOuts {
		chainUnlockRef := ledger.NewChainLockUnlockParams(0)
		txb.PutUnlockParams(byte(i+1), ledger.ConstraintIndexLock, chainUnlockRef)
		util.AssertNoError(err)
	}

	txb.TransactionData.Timestamp = adjustedTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.SenderPrivateKey)

	txBytes := txb.TransactionData.Bytes()
	return txBytes, nil
}

//---------------------------------------------------------

func (u *UnlockParams) Bytes() []byte {
	return u.array.Bytes()
}

func NewUnlockBlock() *UnlockParams {
	return &UnlockParams{
		array: tuples.EmptyTupleEditable(256),
	}
}

func GetChainAccount(chainID base.ChainID, srdr multistate.IndexedStateReader, desc ...bool) (*ledger.OutputWithChainID, []*ledger.OutputWithID, error) {
	chainOutData, err := srdr.GetUTXOForChainID(chainID)
	if err != nil {
		return nil, nil, err
	}
	chainData, err := ledger.ParseChainConstraintsFromData([]*ledger.OutputDataWithID{chainOutData})
	if err != nil {
		return nil, nil, err
	}
	if len(chainData) != 1 {
		return nil, nil, fmt.Errorf("error while parsing chain output")
	}
	retData, err := srdr.GetUTXOsForController(ledger.ChainLockFromChainID(chainID).ControllerID())
	if err != nil {
		return nil, nil, err
	}
	ret, err := ledger.ParseAndSortOutputData(retData, nil, desc...)
	if err != nil {
		return nil, nil, err
	}
	return chainData[0], ret, nil
}

type MakeDelegationInitTransactionParams struct {
	Timestamp              base.LedgerTime
	Amount                 uint64
	MasterID               base.HolderID
	Target                 base.ChainID
	MaxFrozenEpochs        byte
	RequiredInflationShare uint16
	MasterPrivateKey       ed25519.PrivateKey
	Inputs                 []*ledger.OutputWithID
	TagAlongSequencer      base.ChainID
	TagAlongFee            uint64
}

func MakeDelegationInitTransaction(par MakeDelegationInitTransactionParams) ([]byte, error) {
	if par.MasterID != base.HolderID(ledger.SigLockFromED25519PrivateKey(par.MasterPrivateKey)) {
		return nil, fmt.Errorf("MakeDelegationInitTransaction: private key does not match master address")
	}
	inputTotal, inps, err := filterInputs(par.Inputs, par.Amount+par.TagAlongFee, true)
	if err != nil {
		return nil, err
	}
	if inputTotal < par.Amount+par.TagAlongFee {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: not enough tokens")
	}
	txb := New()

	_, tsIn, err := txb.ConsumeOutputsUnlock(inps...)
	if err != nil {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: %w", err)
	}
	lib := ledger.L(par.Timestamp.Slot)
	if tsIn.AddTicks(int(lib.TransactionPace)).After(par.Timestamp) {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: transaction pace constraint violated")
	}

	delegateOutput := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:                 par.Amount,
		MasterID:               par.MasterID,
		Target:                 par.Target,
		MaxFrozenEpochs:        par.MaxFrozenEpochs,
		RequiredInflationShare: par.RequiredInflationShare,
		StartSlot:              par.Timestamp.Slot,
	})
	if _, err = txb.ProduceOutput(delegateOutput); err != nil {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: %w", err)
	}
	tagAlong := ledger.NewTagAlongOutput(par.TagAlongFee, par.TagAlongSequencer, par.MasterID)
	if _, err = txb.ProduceOutput(tagAlong); err != nil {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: %w", err)
	}
	if inputTotal > par.Amount+par.TagAlongFee {
		remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(inputTotal - par.Amount - par.TagAlongFee))
			o.WithLock(ledger.SigLock(par.MasterID))
		})
		if _, err = txb.ProduceOutput(remainder); err != nil {
			return nil, fmt.Errorf("MakeInitDelegationTransaction: %w", err)
		}
	}

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = par.Timestamp
	txb.SignED25519(par.MasterPrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()
	if err != nil {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: %w\n----- failing tx --------\n%s", err, txString)
	}
	//txBytes := txb.TransactionData.Bytes()
	//
	//if err = transaction.ValidateTxBytes(txBytes, txb.LoadInput); err != nil {
	//	return nil, err
	//}
	return txBytes, nil
}
