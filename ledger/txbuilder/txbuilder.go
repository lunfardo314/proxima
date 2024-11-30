package txbuilder

import (
	"crypto"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

type (
	TransactionBuilder struct {
		ConsumedOutputs []*ledger.Output
		TransactionData *transactionData
	}

	transactionData struct {
		InputIDs             []*ledger.OutputID
		Outputs              []*ledger.Output
		UnlockBlocks         []*UnlockParams
		Signature            []byte
		SequencerOutputIndex byte
		StemOutputIndex      byte
		Timestamp            ledger.Time
		InputCommitment      [32]byte
		Endorsements         []*ledger.TransactionID
		LocalLibraries       [][]byte
	}

	UnlockParams struct {
		array *lazybytes.Array
	}
)

func NewTransactionBuilder() *TransactionBuilder {
	return &TransactionBuilder{
		ConsumedOutputs: make([]*ledger.Output, 0),
		TransactionData: &transactionData{
			InputIDs:             make([]*ledger.OutputID, 0),
			Outputs:              make([]*ledger.Output, 0),
			UnlockBlocks:         make([]*UnlockParams, 0),
			SequencerOutputIndex: 0xff,
			StemOutputIndex:      0xff,
			Timestamp:            ledger.NilLedgerTime,
			InputCommitment:      [32]byte{},
			Endorsements:         make([]*ledger.TransactionID, 0),
			LocalLibraries:       make([][]byte, 0),
		},
	}
}

func (txb *TransactionBuilder) NumInputs() int {
	ret := len(txb.ConsumedOutputs)
	util.Assertf(ret == len(txb.TransactionData.InputIDs), "ret==len(ctx.Transaction.InputIDs)")
	return ret
}

func (txb *TransactionBuilder) NumOutputs() int {
	return len(txb.TransactionData.Outputs)
}

func (txb *TransactionBuilder) ConsumeOutput(out *ledger.Output, oid ledger.OutputID) (byte, error) {
	if txb.NumInputs() >= 256 {
		return 0, fmt.Errorf("too many consumed outputs")
	}
	txb.ConsumedOutputs = append(txb.ConsumedOutputs, out)
	txb.TransactionData.InputIDs = append(txb.TransactionData.InputIDs, &oid)
	txb.TransactionData.UnlockBlocks = append(txb.TransactionData.UnlockBlocks, NewUnlockBlock())

	return byte(len(txb.ConsumedOutputs) - 1), nil
}

func (txb *TransactionBuilder) ConsumeOutputWithID(o *ledger.OutputWithID) (byte, error) {
	return txb.ConsumeOutput(o.Output, o.ID)
}

// ConsumeOutputs returns total sum and maximal timestamp
func (txb *TransactionBuilder) ConsumeOutputs(outs ...*ledger.OutputWithID) (uint64, ledger.Time, error) {
	retTotal := uint64(0)
	retTs := ledger.NilLedgerTime
	for _, o := range outs {
		if _, err := txb.ConsumeOutput(o.Output, o.ID); err != nil {
			return 0, ledger.NilLedgerTime, err
		}
		// safe arithmetics
		if o.Output.Amount() > math.MaxUint64-retTotal {
			return 0, ledger.NilLedgerTime, fmt.Errorf("arithmetic overflow when calculating total ")
		}
		retTotal += o.Output.Amount()
		retTs = ledger.MaximumTime(retTs, o.Timestamp())
	}
	return retTotal, retTs, nil
}

func (txb *TransactionBuilder) PutUnlockParams(inputIndex, constraintIndex byte, unlockParamData []byte) {
	txb.TransactionData.UnlockBlocks[inputIndex].array.PutAtIdxWithPadding(constraintIndex, unlockParamData)
}

// PutSignatureUnlock marker 0xff references signature of the transaction.
// It can be distinguished from any reference because it cannot be strictly less than any other reference
func (txb *TransactionBuilder) PutSignatureUnlock(inputIndex byte) {
	txb.PutUnlockParams(inputIndex, ledger.ConstraintIndexLock, []byte{0xff})
}

// PutUnlockReference references some preceding output
func (txb *TransactionBuilder) PutUnlockReference(inputIndex, constraintIndex, referencedInputIndex byte) error {
	if referencedInputIndex >= inputIndex {
		return fmt.Errorf("referenced input index must be strongly less than the unlocked output index")
	}
	txb.PutUnlockParams(inputIndex, constraintIndex, []byte{referencedInputIndex})
	return nil
}

func (txb *TransactionBuilder) PutStandardInputUnlocks(n int) error {
	util.Assertf(n > 0, "n > 0")
	txb.PutSignatureUnlock(0)
	for i := 1; i < n; i++ {
		if err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
			return err
		}
	}
	return nil
}

func (txb *TransactionBuilder) PushEndorsements(txid ...*ledger.TransactionID) {
	txb.TransactionData.Endorsements = append(txb.TransactionData.Endorsements, txid...)
}

func (txb *TransactionBuilder) ProduceOutput(o *ledger.Output) (byte, error) {
	o.MustValidOutput()
	if txb.NumOutputs() >= 256 {
		return 0, fmt.Errorf("too many produced outputs")
	}
	txb.TransactionData.Outputs = append(txb.TransactionData.Outputs, o)
	return byte(len(txb.TransactionData.Outputs) - 1), nil
}

func (txb *TransactionBuilder) ProduceOutputs(outs ...*ledger.Output) (uint64, error) {
	total := uint64(0)
	for _, o := range outs {
		if _, err := txb.ProduceOutput(o); err != nil {
			return 0, err
		}
		total += o.Amount()
	}
	return total, nil
}

func (txb *TransactionBuilder) InputCommitment() [32]byte {
	arr := lazybytes.EmptyArray(256)
	for _, o := range txb.ConsumedOutputs {
		arr.Push(o.Bytes())
	}
	return blake2b.Sum256(arr.Bytes())
}

func (tx *transactionData) ToArray() *lazybytes.Array {
	unlockParams := lazybytes.EmptyArray(256)
	inputIDs := lazybytes.EmptyArray(256)
	outputs := lazybytes.EmptyArray(256)
	endorsements := lazybytes.EmptyArray(256)

	for _, b := range tx.UnlockBlocks {
		unlockParams.Push(b.Bytes())
	}
	for _, oid := range tx.InputIDs {
		inputIDs.Push(oid[:])
	}
	for _, o := range tx.Outputs {
		outputs.Push(o.Bytes())
	}
	for _, e := range tx.Endorsements {
		endorsements.Push(e.Bytes())
	}

	total := uint64(0)
	for _, o := range tx.Outputs {
		total += o.Amount()
	}
	var totalBin [8]byte
	binary.BigEndian.PutUint64(totalBin[:], total)

	elems := make([]any, ledger.TxTreeIndexMax)
	elems[ledger.TxUnlockData] = unlockParams
	elems[ledger.TxInputIDs] = inputIDs
	elems[ledger.TxOutputs] = outputs
	elems[ledger.TxSignature] = tx.Signature
	elems[ledger.TxSequencerAndStemOutputIndices] = []byte{tx.SequencerOutputIndex, tx.StemOutputIndex}
	elems[ledger.TxTimestamp] = tx.Timestamp.Bytes()
	elems[ledger.TxTotalProducedAmount] = totalBin[:]
	elems[ledger.TxInputCommitment] = tx.InputCommitment[:]
	elems[ledger.TxEndorsements] = endorsements
	elems[ledger.TxLocalLibraries] = lazybytes.MakeArrayFromDataReadOnly(tx.LocalLibraries...)
	return lazybytes.MakeArrayReadOnly(elems...)
}

func (tx *transactionData) Bytes() []byte {
	return tx.ToArray().Bytes()
}

func (tx *transactionData) EssenceBytes() []byte {
	return transaction.EssenceBytesFromTransactionDataTree(tx.ToArray().AsTree())
}

var rnd = rand.New(rand.NewSource(time.Now().UnixNano()))

func (txb *TransactionBuilder) SignED25519(privKey ed25519.PrivateKey) {
	sig, err := privKey.Sign(rnd, txb.TransactionData.EssenceBytes(), crypto.Hash(0))
	util.AssertNoError(err)
	pubKey := privKey.Public().(ed25519.PublicKey)
	txb.TransactionData.Signature = common.Concat(sig, []byte(pubKey))
}

type (
	TransferData struct {
		SenderPrivateKey  ed25519.PrivateKey
		SenderPublicKey   ed25519.PublicKey
		SourceAccount     ledger.Accountable
		Inputs            []*ledger.OutputWithID
		ChainOutput       *ledger.OutputWithChainID
		Timestamp         ledger.Time // takes ledger.TimeFromClockTime(time.Now()) if ledger.NilLedgerTime
		Lock              ledger.Lock
		Amount            uint64
		AdjustToMinimum   bool
		AddSender         bool
		AddConstraints    [][]byte
		MarkAsSequencerTx bool
		UnlockData        []*UnlockData
		Endorsements      []*ledger.TransactionID
		TagAlong          *TagAlongData
	}

	// MakeChainSuccTransactionParams contains parameters for building a chain transaction
	MakeChainSuccTransactionParams struct {
		// predecessor
		ChainInput *ledger.OutputWithChainID
		// timestamp of the target transaction
		Timestamp ledger.Time
		// some amount sent to the target lock. It can be a tag-along output. Remainder goes to the chain
		WithdrawTarget ledger.Lock
		WithdrawAmount uint64
		// chain controller
		PrivateKey ed25519.PrivateKey
		// enforce transaction is profitable
		EnforceProfitability bool
		// return input loader function
		ReturnInputLoader bool
	}

	TagAlongData struct {
		SeqID  ledger.ChainID
		Amount uint64
	}

	UnlockData struct {
		OutputIndex     byte
		ConstraintIndex byte
		Data            []byte
	}
)

func NewTransferData(senderKey ed25519.PrivateKey, sourceAccount ledger.Accountable, ts ledger.Time) *TransferData {
	sourcePubKey := senderKey.Public().(ed25519.PublicKey)
	if util.IsNil(sourceAccount) {
		sourceAccount = ledger.AddressED25519FromPublicKey(sourcePubKey)
	}
	return &TransferData{
		SenderPrivateKey: senderKey,
		SenderPublicKey:  sourcePubKey,
		SourceAccount:    sourceAccount,
		Timestamp:        ts,
		AddConstraints:   make([][]byte, 0),
		UnlockData:       make([]*UnlockData, 0),
		Endorsements:     make([]*ledger.TransactionID, 0),
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

func (t *TransferData) WithConstraintBinary(constr []byte, idx ...byte) *TransferData {
	if len(idx) == 0 {
		t.AddConstraints = append(t.AddConstraints, constr)
	} else {
		util.Assertf(idx[0] == 0xff || idx[0] < ledger.ConstraintIndexFirstOptionalConstraint, "WithConstraintBinary: wrong constraint index")
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
		if !ledger.EqualConstraints(t.SourceAccount, o.Output.Lock()) {
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

func (t *TransferData) WithSender() *TransferData {
	t.AddSender = true
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

func (t *TransferData) WithEndorsements(ids ...*ledger.TransactionID) *TransferData {
	t.Endorsements = ids
	return t
}

func (t *TransferData) WithTagAlong(seqID ledger.ChainID, amount uint64) *TransferData {
	t.TagAlong = &TagAlongData{
		SeqID:  seqID,
		Amount: amount,
	}
	return t
}

// TotalAdjustedAmount adjust amount to minimum storage deposit requirements
func (t *TransferData) TotalAdjustedAmount() uint64 {
	if !t.AdjustToMinimum {
		// not adjust. Will render wrong transaction if not enough tokens
		return t.Amount
	}

	outTentative := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(t.Amount).WithLock(t.Lock)
		for _, c := range t.AddConstraints {
			_, err := o.PushConstraint(c)
			util.AssertNoError(err)
		}
	})

	minimumDeposit := ledger.MinimumStorageDeposit(outTentative, 0)
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

func outputsToConsumeSimple(par *TransferData, amount uint64) (uint64, []*ledger.OutputWithID, error) {
	consumedOuts := par.Inputs[:0]
	availableTokens := uint64(0)
	numConsumedOutputs := 0

	for _, o := range par.Inputs {
		if numConsumedOutputs >= 256 {
			return 0, nil, fmt.Errorf("exceeded max number of consumed outputs 256")
		}
		consumedOuts = append(consumedOuts, o)
		numConsumedOutputs++
		availableTokens += o.Output.Amount()
		if availableTokens >= amount {
			break
		}
	}
	return availableTokens, consumedOuts, nil
}

func MakeSimpleTransferTransaction(par *TransferData, disableEndorsementChecking ...bool) ([]byte, error) {
	txBytes, _, err := MakeSimpleTransferTransactionWithRemainder(par, disableEndorsementChecking...)
	return txBytes, err
}

func MakeSimpleTransferTransactionWithRemainder(par *TransferData, disableEndorsementChecking ...bool) ([]byte, *ledger.OutputWithID, error) {
	if !ledger.ValidTime(par.Timestamp) {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: wrong timestamp bytes 0x%s", hex.EncodeToString(par.Timestamp[:]))
	}

	if par.ChainOutput != nil {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: ChainInput must be nil. Use MakeSimpleTransferTransaction instead")
	}
	if par.Lock == nil {
		return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: target lock is not specified")
	}
	amount := par.TotalAdjustedAmount()
	availableTokens, consumedOuts, err := outputsToConsumeSimple(par, amount)
	if err != nil {
		return nil, nil, err
	}

	if availableTokens < amount {
		if availableTokens < amount {
			return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: not enough tokens in account %s: needed %d, got %d",
				par.SourceAccount.String(), par.Amount, availableTokens)
		}
	}

	txb := NewTransactionBuilder()
	checkTotal, inputTs, err := txb.ConsumeOutputs(consumedOuts...)
	if err != nil {
		return nil, nil, err
	}
	util.Assertf(availableTokens == checkTotal, "availableTokens == checkTotal")

	adjustedTs := ledger.MaximumTime(inputTs, par.Timestamp).
		AddTicks(ledger.TransactionPace())

	util.Assertf(ledger.ValidTime(adjustedTs), "ledger.ValidTime(adjustedTs): ts bytes 0x%s", hex.EncodeToString(adjustedTs[:]))

	for i := range par.Endorsements {
		if len(disableEndorsementChecking) == 0 || !disableEndorsementChecking[0] {
			if par.Endorsements[i].Slot() < adjustedTs.Slot() {
				return nil, nil, fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: can't endorse transaction from another time slot")
			}
		}
		if par.Endorsements[i].Slot() > adjustedTs.Slot() {
			// adjust timestamp to the endorsed slot
			adjustedTs = ledger.NewLedgerTime(par.Endorsements[i].Slot(), 0)
		}
	}

	mainOutput := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(amount).WithLock(par.Lock)
		if par.AddSender {
			senderAddr := ledger.AddressED25519FromPublicKey(par.SenderPublicKey)
			if _, err = o.PushConstraint(ledger.NewSenderED25519(senderAddr).Bytes()); err != nil {
				return
			}
		}
		for _, constr := range par.AddConstraints {
			if _, err = o.PushConstraint(constr); err != nil {
				return
			}
		}
	})
	if err != nil {
		return nil, nil, err
	}

	tagAlongFee := uint64(0)
	var tagAlongOut *ledger.Output
	if par.TagAlong != nil {
		tagAlongOut = ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.TagAlong.Amount).
				WithLock(ledger.ChainLockFromChainID(par.TagAlong.SeqID))
		})
		tagAlongFee = par.TagAlong.Amount
	}

	var remainderOut *ledger.Output
	var remainderIndex byte
	if availableTokens > amount+tagAlongFee {
		remainderOut = ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(availableTokens - amount - tagAlongFee).WithLock(par.SourceAccount.AsLock())
		})
	}
	if remainderOut != nil {
		if remainderIndex, err = txb.ProduceOutput(remainderOut); err != nil {
			return nil, nil, err
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
	txb.TransactionData.InputCommitment = txb.InputCommitment()
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
// Returns transaction adn inflation amount
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
	if tsIn := par.ChainInput.ID.Timestamp(); par.Timestamp.Before(par.ChainInput.ID.Timestamp().AddTicks(ledger.TransactionPace())) {
		return nil, 0, nil, errP("timestamp %s is inconsistent with latest chain output timestamp %s", par.Timestamp.String(), tsIn.String())
	}

	// find chain constraint in the predecessor
	chainInConstraint, chainInConstraintIdx := par.ChainInput.Output.ChainConstraint()
	if chainInConstraintIdx == 0xff {
		return nil, 0, nil, errP("not a chain output: %s", par.ChainInput.ID.StringShort())
	}
	// calculate inflation amount and create inflation constraint
	inflationAmount, _ := calcChainInflationAmount(par.ChainInput, par.Timestamp)
	chainInAmount := par.ChainInput.Output.Amount()
	if chainInAmount+inflationAmount <= par.WithdrawAmount {
		// we do not handle complete withdrawal of funds from the chain
		return nil, 0, nil, errP("not enough tokens to withdraw specified amount %d", par.WithdrawAmount)
	}
	var inflationConstraint *ledger.InflationConstraint
	if inflationAmount > 0 {
		inflationConstraint = &ledger.InflationConstraint{
			ChainInflation:       inflationAmount,
			ChainConstraintIndex: chainInConstraintIdx,
		}
	}

	chainOutAmount := chainInAmount + inflationAmount - par.WithdrawAmount
	util.Assertf(chainOutAmount > 0, "chainOutAmount > 0")

	if par.EnforceProfitability {
		if chainOutAmount < chainInAmount {
			return nil, 0, nil, errP("chain transition is not profitable")
		}
	}

	txb := NewTransactionBuilder()

	// consume predecessor
	chainPredIdx, err := txb.ConsumeOutput(par.ChainInput.Output, par.ChainInput.ID)
	if err != nil {
		return nil, 0, nil, errP(err)
	}
	if par.ReturnInputLoader {
		consumedOutputs = append(consumedOutputs, par.ChainInput.Output)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	chainID := chainInConstraint.ID
	if chainInConstraint.IsOrigin() {
		chainID = ledger.MakeOriginChainID(&par.ChainInput.ID)
	}

	// make chain output
	var chainOutConstraintIdx byte
	chainOut := ledger.NewOutput(func(o *ledger.Output) {
		o.PutAmount(chainOutAmount)
		o.PutLock(par.ChainInput.Output.Lock())
		// put chain constraint
		chainOutConstraint := ledger.NewChainConstraint(chainID, chainPredIdx, chainInConstraintIdx, 0)
		chainOutConstraintIdx, _ = o.PushConstraint(chainOutConstraint.Bytes())

		if inflationConstraint != nil {
			inflationConstraint.ChainConstraintIndex = chainOutConstraintIdx
			_, _ = o.PushConstraint(inflationConstraint.Bytes())
		}
	})

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, 0, nil, errP(err)
	}
	// unlock chain input (chain constraint unlock + inflation (optionally)
	txb.PutUnlockParams(chainPredIdx, chainInConstraintIdx, ledger.NewChainUnlockParams(chainOutIndex, chainOutConstraintIdx, 0))

	if par.WithdrawAmount > 0 {
		withdrawOut := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.WithdrawAmount).
				WithLock(par.WithdrawTarget)
		})
		if _, err = txb.ProduceOutput(withdrawOut); err != nil {
			return nil, 0, nil, errP(err)
		}
	}

	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.PrivateKey)

	inputLoader := func(i byte) (*ledger.Output, error) {
		panic("MakeSequencerTransactionWithInputLoader: par.ReturnInputLoader parameter must be set to true")
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
	availableTokens, consumedOuts, err := outputsToConsumeSimple(par, amount)
	if err != nil {
		return nil, err
	}
	// count the chain output in
	availableTokens += par.ChainOutput.Output.Amount()
	// some tokens must remain in the chain account
	if availableTokens <= amount {
		return nil, fmt.Errorf("not enough tokens in account %s: needed %d, got %d",
			par.SourceAccount.String(), par.Amount, availableTokens)
	}

	txb := NewTransactionBuilder()

	chainInputIndex, err := txb.ConsumeOutput(par.ChainOutput.Output, par.ChainOutput.ID)
	util.Assertf(chainInputIndex == 0, "chainInputIndex == 0")
	if err != nil {
		return nil, err
	}
	if par.MarkAsSequencerTx {
		txb.TransactionData.SequencerOutputIndex = 0
	}
	checkAmount, inputTs, err := txb.ConsumeOutputs(consumedOuts...)
	if err != nil {
		return nil, err
	}
	util.Assertf(availableTokens == checkAmount+par.ChainOutput.Output.Amount(), "availableTokens == checkAmount")
	adjustedTs := ledger.MaximumTime(inputTs, par.ChainOutput.Timestamp()).
		AddTicks(ledger.TransactionPace())

	for i := range par.Endorsements {
		if len(disableEndorsementChecking) == 0 || !disableEndorsementChecking[0] {
			if par.Endorsements[i].Slot() < adjustedTs.Slot() {
				return nil, fmt.Errorf("can't endorse transaction from another slot")
			}
		}
		if par.Endorsements[i].Slot() > adjustedTs.Slot() {
			// adjust timestamp to the endorsed slot
			adjustedTs = ledger.NewLedgerTime(par.Endorsements[i].Slot(), 0)
		}
	}

	chainConstr := ledger.NewChainConstraint(par.ChainOutput.ChainID, 0, par.ChainOutput.PredecessorConstraintIndex, 0)
	util.Assertf(availableTokens > amount, "availableTokens > amount")
	chainSuccessorOutput := par.ChainOutput.Output.Clone(func(o *ledger.Output) {
		o.WithAmount(availableTokens-amount).
			PutConstraint(chainConstr.Bytes(), par.ChainOutput.PredecessorConstraintIndex)
	})
	if _, err = txb.ProduceOutput(chainSuccessorOutput); err != nil {
		return nil, err
	}

	mainOutput := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(amount).WithLock(par.Lock)
		if par.AddSender {
			senderAddr := ledger.AddressED25519FromPublicKey(par.SenderPublicKey)
			if _, err = o.PushConstraint(ledger.NewSenderED25519(senderAddr).Bytes()); err != nil {
				return
			}
		}
		for _, constr := range par.AddConstraints {
			if _, err = o.PushConstraint(constr); err != nil {
				return
			}
		}
	})
	if err != nil {
		return nil, err
	}

	if _, err = txb.ProduceOutput(mainOutput); err != nil {
		return nil, err
	}
	// unlock chain input
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, par.ChainOutput.PredecessorConstraintIndex, []byte{0, par.ChainOutput.PredecessorConstraintIndex, 0})

	// always reference chain input
	for i := range consumedOuts {
		chainUnlockRef := ledger.NewChainLockUnlockParams(0, par.ChainOutput.PredecessorConstraintIndex)
		txb.PutUnlockParams(byte(i+1), ledger.ConstraintIndexLock, chainUnlockRef)
		util.AssertNoError(err)
	}

	txb.TransactionData.Timestamp = adjustedTs
	txb.TransactionData.InputCommitment = txb.InputCommitment()
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
		array: lazybytes.EmptyArray(256),
	}
}

func GetChainAccount(chainID ledger.ChainID, srdr global.IndexedStateReader, desc ...bool) (*ledger.OutputWithChainID, []*ledger.OutputWithID, error) {
	chainOutData, err := srdr.GetUTXOForChainID(&chainID)
	if err != nil {
		return nil, nil, err
	}
	chainData, err := txutils.ParseChainConstraintsFromData([]*ledger.OutputDataWithID{chainOutData})
	if err != nil {
		return nil, nil, err
	}
	if len(chainData) != 1 {
		return nil, nil, fmt.Errorf("error while parsing chain output")
	}
	retData, err := srdr.GetUTXOsLockedInAccount(chainID.AsAccountID())
	if err != nil {
		return nil, nil, err
	}
	ret, err := txutils.ParseAndSortOutputData(retData, nil, desc...)
	if err != nil {
		return nil, nil, err
	}
	return chainData[0], ret, nil
}

// InsertSimpleChainTransition inserts a simple chain transition (surprise, surprise). Takes output with chain constraint from parameters,
// Produces identical output, only modifies timestamp. Unlocks chain-input lock with signature reference
func (txb *TransactionBuilder) InsertSimpleChainTransition(inChainData *ledger.OutputDataWithChainID, ts ledger.Time) error {
	chainIN, err := ledger.OutputFromBytesReadOnly(inChainData.OutputData)
	if err != nil {
		return err
	}
	_, predecessorConstraintIndex := chainIN.ChainConstraint()
	if predecessorConstraintIndex == 0xff {
		return fmt.Errorf("can't find chain constrain in the output")
	}
	predecessorOutputIndex, err := txb.ConsumeOutput(chainIN, inChainData.ID)
	if err != nil {
		return err
	}
	successor := ledger.NewChainConstraint(inChainData.ChainID, predecessorOutputIndex, predecessorConstraintIndex, 0)
	chainOut := chainIN.Clone(func(out *ledger.Output) {
		out.PutConstraint(successor.Bytes(), predecessorConstraintIndex)
	})
	successorOutputIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return err
	}
	txb.PutUnlockParams(predecessorOutputIndex, predecessorConstraintIndex, []byte{successorOutputIndex, predecessorConstraintIndex, 0})
	txb.PutSignatureUnlock(successorOutputIndex)

	return nil
}

func (txb *TransactionBuilder) String() string {
	ret := []string{"TransactionBuilder:"}
	ret = append(ret, fmt.Sprintf("Consumed outputs (%d):", len(txb.ConsumedOutputs)))
	util.Assertf(len(txb.ConsumedOutputs) == len(txb.TransactionData.InputIDs), "len(txb.ConsumedOutputs) == len(txb.Transaction.InputIDs)")
	for i := range txb.ConsumedOutputs {
		ret = append(ret, fmt.Sprintf("%d : %s\n", i, txb.TransactionData.InputIDs[i].StringShort()))
		ret = append(ret, txb.ConsumedOutputs[i].ToString("     "))
	}
	ret = append(ret, fmt.Sprintf("Produced outputs (%d):", len(txb.TransactionData.Outputs)))
	for i, o := range txb.TransactionData.Outputs {
		ret = append(ret, fmt.Sprintf("%d :%s", i, o.ToString("    ")))
	}
	ret = append(ret, fmt.Sprintf("Endorsements (%d):", len(txb.TransactionData.Endorsements)))
	for i, txid := range txb.TransactionData.Endorsements {
		ret = append(ret, fmt.Sprintf("%d : %s", i, txid.StringShort()))
	}
	return strings.Join(ret, "\n")
}
