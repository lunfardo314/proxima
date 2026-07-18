package utxodb

// Transaction-building recipes for the in-memory test ledger and the
// integration tests that exercise it. These helpers used to live in
// ledger/txbuilder; they are kept here, on the side of the UTXODB
// test infrastructure, because production composition paths
// (sequencer, proxi wallet) have their own dedicated builders. The
// recipes are deliberately written against the typed-output wrapper
// in examples/exhelp — the same one the in-tree examples use — so
// the typed-buffer machinery has a single home.

import (
	"crypto/ed25519"
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

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
		Endorsements     []base.TransactionID
		ExplicitBaseline *base.TransactionID
		TagAlong         *TagAlongData
	}

	// MakeChainSuccTransactionParams contains parameters for building a chain transaction
	MakeChainSuccTransactionParams struct {
		ChainInput           *ledger.OutputWithChainID
		Timestamp            base.LedgerTime
		TagAlongSequencer    base.ChainID
		TagAlongFee          uint64
		PrivateKey           ed25519.PrivateKey
		EnforceProfitability bool
		ReturnInputLoader    bool
	}

	TagAlongData struct {
		SeqID  base.ChainID
		Amount uint64
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
		return t
	}
	if idx[0] == 0xff || idx[0] < ledger.ConstraintIndexChain {
		t.AddConstraints[idx[0]] = constr
		return t
	}
	util.Assertf(idx[0] > ledger.ConstraintIndexChain, "WithConstraintBinary: cannot overwrite the chain slot directly")
	target := int(idx[0] - ledger.ConstraintIndexChain)
	for len(t.AddConstraints) <= target {
		t.AddConstraints = append(t.AddConstraints, nil)
	}
	t.AddConstraints[target] = constr
	return t
}

func (t *TransferData) WithConstraint(constr ledger.Constraint, idx ...byte) *TransferData {
	return t.WithConstraintBinary(constr.Bytes(), idx...)
}

func (t *TransferData) UseOutputsAsInputs(outs ...*ledger.OutputWithID) error {
	for _, o := range outs {
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

// TotalAdjustedAmount adjusts amount to minimum storage deposit requirements
func (t *TransferData) TotalAdjustedAmount() uint64 {
	if !t.AdjustToMinimum {
		return t.Amount
	}

	outTentative := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(math.MaxUint64 / 2)).WithLock(t.Lock)
		for i, c := range t.AddConstraints {
			if c == nil {
				continue
			}
			o.PutConstraint(c, ledger.ConstraintIndexChain+byte(i))
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

// MakeTransferTransaction builds a transfer transaction. disableEndorsementChecking
// is a test-only flag to skip endorsement-slot validation (can produce tx with
// invalid endorsements).
func MakeTransferTransaction(par *TransferData, disableEndorsementValidation ...bool) ([]byte, error) {
	if par.Amount == 0 || par.Lock == nil {
		return nil, fmt.Errorf("MakeTransferTransaction: wrong amount or lock")
	}
	if par.ChainOutput == nil {
		return MakeSimpleTransferTransaction(par, disableEndorsementValidation...)
	}
	return MakeChainTransferTransaction(par, disableEndorsementValidation...)
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

	txb := exhelp.New()
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
			adjustedTs = base.T(par.Endorsements[i].Slot(), 0)
		}
	}

	mainOutput := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(par.Lock)
		if o.NumConstraints()+len(par.AddConstraints) >= 256 {
			err = fmt.Errorf("MakeSimpleTransferTransactionWithRemainder: too many UTXO elements")
			return
		}
		for i, constr := range par.AddConstraints {
			if constr == nil {
				continue
			}
			o.PutConstraint(constr, ledger.ConstraintIndexChain+byte(i))
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
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			util.AssertNoError(err)
		}
	}

	txb.SetTimestamp(adjustedTs)
	txb.TxData.Endorsements = par.Endorsements
	txb.ComputeInputCommitment()
	txb.SignED25519(par.SenderPrivateKey)

	txBytes := txb.Bytes()
	var rem *ledger.OutputWithID
	if remainderOut != nil {
		if rem, err = transaction.OutputWithIDFromTransactionBytes(txBytes, remainderIndex); err != nil {
			return nil, nil, err
		}
	}
	return txBytes, rem, nil
}

// MakeChainSuccessorTransaction creates a transaction to continue a non-sequencer chain with inflation.
// Optionally withdraws some amount to the target lock, which can be used as a tag-along output.
// Returns transaction bytes, inflation amount, and (if par.ReturnInputLoader) a bytes-loader.
func MakeChainSuccessorTransaction(par *MakeChainSuccTransactionParams) ([]byte, uint64, func(i byte) ([]byte, error), error) {
	var consumedOutputs []*ledger.Output
	if par.ReturnInputLoader {
		consumedOutputs = make([]*ledger.Output, 0)
	}
	errP := util.MakeErrFuncForPrefix("MakeChainSuccessorTransaction")

	if par.ChainInput.ID.IsSequencerTransaction() {
		return nil, 0, nil, errP("cannot extend sequencer output")
	}
	if par.Timestamp.IsSlotBoundary() {
		return nil, 0, nil, errP("timestamp is on slot boundary")
	}

	lib := ledger.L(par.Timestamp.Slot)
	if tsIn := par.ChainInput.ID.Timestamp(); par.Timestamp.Before(par.ChainInput.ID.Timestamp().AddTicks(int(lib.TransactionPace))) {
		return nil, 0, nil, errP("timestamp %s is inconsistent with latest chain output timestamp %s", par.Timestamp.String(), tsIn.String())
	}

	chainInConstraint := par.ChainInput.Output.ChainConstraint()
	if chainInConstraint == nil {
		return nil, 0, nil, errP("not a chain output: %s", par.ChainInput.ID.StringShort())
	}
	inflationAmount := lib.ChainInflationOneSlot(
		par.ChainInput.Output.TokenBalance()+uint64(par.ChainInput.Output.FrozenCoverage(0)),
		par.ChainInput.Timestamp().Slot,
	)
	chainInAmount := par.ChainInput.Output.TokenBalance()
	if chainInAmount+inflationAmount <= par.TagAlongFee {
		return nil, 0, nil, errP("not enough tokens for tag-along fee %d", par.TagAlongFee)
	}

	chainOutAmount := chainInAmount + inflationAmount - par.TagAlongFee
	util.Assertf(chainOutAmount > 0, "chainOutAmount > 0")

	if par.EnforceProfitability {
		if chainOutAmount < chainInAmount {
			return nil, 0, nil, errP("chain transition is not profitable")
		}
	}

	txb := exhelp.New()

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

	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(int64(chainOutAmount), int64(inflationAmount))
		o.PutLock(par.ChainInput.Output.Lock())
		chainOutConstraint := ledger.NewChainConstraint(chainID, chainPredIdx, chainInConstraint.OriginSlot, chainInConstraint.CumulativeChainInflation+inflationAmount, chainInConstraint.CumulativeBranchBonus, chainInConstraint.TransitionCounter+1, chainInConstraint.BranchCounter)
		o.PutConstraint(chainOutConstraint.Bytes(), ledger.ConstraintIndexChain)
	})

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, 0, nil, errP(err)
	}
	txb.PutUnlockParams(chainPredIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(chainOutIndex))

	if par.TagAlongFee > 0 {
		tagAlongOut := ledger.NewTagAlongOutput(par.TagAlongFee, par.TagAlongSequencer, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.PrivateKey)))
		if _, err = txb.ProduceOutput(tagAlongOut); err != nil {
			return nil, 0, nil, errP(err)
		}
	}

	txb.SetTimestamp(par.Timestamp)
	txb.ComputeInputCommitment()
	txb.SignED25519(par.PrivateKey)

	inputLoader := func(i byte) ([]byte, error) {
		panic("MakeChainSuccessorTransaction: par.ReturnInputLoader was not set to true")
	}
	if par.ReturnInputLoader {
		inputLoader = func(i byte) ([]byte, error) {
			return consumedOutputs[i].Bytes(), nil
		}
	}
	return txb.Bytes(), inflationAmount, inputLoader, nil
}

func MakeChainTransferTransaction(par *TransferData, disableEndorsementChecking ...bool) ([]byte, error) {
	if par.ChainOutput == nil {
		return nil, fmt.Errorf("ChainInput must be provided")
	}
	amount := par.TotalAdjustedAmount()
	availableTokens, consumedOuts, err := filterInputs(par.Inputs, amount)
	if err != nil {
		return nil, err
	}
	availableTokens += par.ChainOutput.Output.TokenBalance()
	if availableTokens <= amount {
		return nil, fmt.Errorf("not enough tokens in account %s: needed %d, got %d",
			par.SourceAccount.String(), par.Amount, availableTokens)
	}

	txb := exhelp.New()

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
		for i, constr := range par.AddConstraints {
			if constr == nil {
				continue
			}
			o.PutConstraint(constr, ledger.ConstraintIndexChain+byte(i))
		}
	})
	if err != nil {
		return nil, err
	}

	if _, err = txb.ProduceOutput(mainOutput); err != nil {
		return nil, err
	}
	txb.PutSignatureUnlock(outChainOutputIdx)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(outChainOutputIdx))

	for i := range consumedOuts {
		chainUnlockRef := ledger.NewChainLockUnlockParams(0)
		txb.PutUnlockParams(byte(i+1), ledger.ConstraintIndexLock, chainUnlockRef)
		util.AssertNoError(err)
	}

	txb.SetTimestamp(adjustedTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(par.SenderPrivateKey)

	return txb.Bytes(), nil
}

// GetChainAccount returns the chain output of chainID plus all
// chainLock-controlled outputs for the controller, sorted (optionally
// descending) by amount.
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
	RequiredInflationCut uint16
	MasterPrivateKey       ed25519.PrivateKey
	Inputs                 []*ledger.OutputWithID
	TagAlongSequencer      base.ChainID
	TagAlongFee            uint64
	TargetEpochSlots       uint32
	TargetMaxFrozenEpochs  byte
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
	txb := exhelp.New()

	_, tsIn, err := txb.ConsumeOutputsUnlock(inps...)
	if err != nil {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: %w", err)
	}
	lib := ledger.L(par.Timestamp.Slot)
	if tsIn.AddTicks(int(lib.TransactionPace)).After(par.Timestamp) {
		return nil, fmt.Errorf("MakeInitDelegationTransaction: transaction pace constraint violated")
	}

	targetEpochSlots := par.TargetEpochSlots
	if targetEpochSlots == 0 {
		targetEpochSlots = lib.DelegationEpochSlots
	}
	targetMaxFrozenEpochs := par.TargetMaxFrozenEpochs
	if targetMaxFrozenEpochs == 0 {
		targetMaxFrozenEpochs = byte(lib.DelegationMaxFrozenEpochsMax)
	}
	delegateOutput := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:                 par.Amount,
		MasterID:               par.MasterID,
		Target:                 par.Target,
		MaxFrozenEpochs:        par.MaxFrozenEpochs,
		RequiredInflationCut: par.RequiredInflationCut,
		StartSlot:              par.Timestamp.Slot,
		EpochSlots:             targetEpochSlots,
		TargetMaxFrozenEpochs:  targetMaxFrozenEpochs,
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

	txb.ComputeInputCommitment()
	txb.SetTimestamp(par.Timestamp)
	txb.SignED25519(par.MasterPrivateKey)

	txBytes := txb.Bytes()
	tx, err := transaction.ParseAndValidate(txBytes, txb.LoadInputBytes)
	if err != nil {
		txString := ""
		if tx != nil {
			txString = tx.String()
		}
		return nil, fmt.Errorf("MakeInitDelegationTransaction: %w\n----- failing tx --------\n%s", err, txString)
	}
	return txBytes, nil
}

// EndChainParams collects inputs for MakeEndChainTransaction.
type EndChainParams struct {
	Timestamp     base.LedgerTime
	ChainIn       *ledger.OutputWithChainID
	PrivateKey    ed25519.PrivateKey
	TagAlongSeqID base.ChainID
	TagAlongFee   uint64 // 0 means no fee output will be produced
}

// MakeEndChainTransaction consumes the chain output and produces a
// plain sigLock UTXO to terminate the chain. Optionally adds a
// tag-along output to a sequencer.
func MakeEndChainTransaction(par EndChainParams) (*transaction.Transaction, error) {
	txb := exhelp.New()

	consumedIndex, err := txb.ConsumeOutput(par.ChainIn.Output, par.ChainIn.ID)
	util.AssertNoError(err)

	feeAmount := par.TagAlongFee

	outNonChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(par.ChainIn.Output.TokenBalance() - feeAmount)).
			WithLock(ledger.SigLockFromED25519PrivateKey(par.PrivateKey))
	})
	_, err = txb.ProduceOutput(outNonChain)
	util.AssertNoError(err)

	if feeAmount > 0 {
		tagAlongFeeOut := ledger.NewTagAlongOutput(feeAmount, par.TagAlongSeqID, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.PrivateKey)))
		if _, err = txb.ProduceOutput(tagAlongFeeOut); err != nil {
			return nil, err
		}
	}

	txb.PutSignatureUnlock(consumedIndex, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(consumedIndex, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	txb.SetTimestamp(par.Timestamp)
	txb.ComputeInputCommitment()
	txb.SignED25519(par.PrivateKey)

	tx, err := transaction.ParseAndValidate(txb.Bytes(), txb.LoadInputBytes)
	if err != nil {
		txString := ""
		if tx != nil {
			txString = tx.String()
		}
		return nil, fmt.Errorf("MakeEndChainTransaction: %w\n==== failing transaction ====\n%s", err, txString)
	}
	return tx, nil
}
