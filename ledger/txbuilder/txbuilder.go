package txbuilder

import (
	"crypto/ed25519"
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
)

// UnlockParams is re-exported from txbuildercore so existing tests / sequencer
// code referencing txbuilder.UnlockParams keep compiling.
type UnlockParams = txbuildercore.UnlockParams

// NewUnlockBlock re-exported for the same reason.
func NewUnlockBlock() *UnlockParams { return txbuildercore.NewUnlockBlock() }

// TxBuilder is a thin server-side sugar layer over *txbuildercore.TxBuilder.
// All wire-format state lives in the embedded txbuildercore.TxData; the two
// typed buffers below mirror the consumed / produced outputs in their
// *ledger.Output form so the typed APIs (validation, frozen-coverage,
// chain helpers, recipes) can inspect them without re-parsing bytes.
type TxBuilder struct {
	*txbuildercore.TxBuilder
	ConsumedOutputs []*ledger.Output
	ProducedOutputs []*ledger.Output
}

func New() *TxBuilder {
	return &TxBuilder{
		TxBuilder:       txbuildercore.New(0),
		ConsumedOutputs: make([]*ledger.Output, 0),
		ProducedOutputs: make([]*ledger.Output, 0),
	}
}

// SetTimestamp shadows the embedded txbuildercore.SetTimestamp to also set
// TxData.UpgradeIndex from the slot's library version. This preserves
// the deferred-derivation behaviour the old transactionData had in
// toRawTxData — callers don't need to know about the upgrade-index
// field.
func (txb *TxBuilder) SetTimestamp(ts base.LedgerTime) {
	txb.TxBuilder.SetTimestamp(ts)
	txb.TxData.UpgradeIndex = ledger.L(ts.Slot).UpgradeIndex()
}

// ReplaceProducedOutput overwrites the produced output at idx, syncing
// both the typed buffer and the wire-format byte slice. Used by
// callers that mutate a produced output after the initial Push (e.g.
// chain-output post-processing in recipes / tests).
func (txb *TxBuilder) ReplaceProducedOutput(idx byte, o *ledger.Output) {
	txb.ProducedOutputs[idx] = o
	txb.TxData.OutputBytes[idx] = o.Bytes()
}

// ConsumeOutput appends a typed consumed output and forwards its raw
// bytes to the embedded txbuildercore.TxBuilder.
func (txb *TxBuilder) ConsumeOutput(out *ledger.Output, oid base.OutputID) (byte, error) {
	if txb.NumInputs() >= 256 {
		return 0, fmt.Errorf("too many consumed outputs")
	}
	txb.ConsumedOutputs = append(txb.ConsumedOutputs, out)
	return txb.TxBuilder.ConsumeOutput(out.Bytes(), oid), nil
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
		if o.Output.TokenBalance() > math.MaxUint64-retTotal {
			return 0, base.NilLedgerTime, fmt.Errorf("arithmetic overflow when calculating total ")
		}
		retTotal += o.Output.TokenBalance()
		retTs = base.MaximumTime(retTs, o.Timestamp())
	}
	return retTotal, retTs, nil
}

// ProduceOutput adds produced output to the tx builder. Checks storage deposit.
func (txb *TxBuilder) ProduceOutput(o *ledger.Output) (byte, error) {
	if err := o.EnoughAmountForStorageDeposit(); err != nil {
		return 0, fmt.Errorf("TxBuilder:ProduceOutput: %v", err)
	}
	o.MustValidOutput()
	if txb.NumOutputs() >= 256 {
		return 0, fmt.Errorf("too many produced outputs")
	}
	txb.ProducedOutputs = append(txb.ProducedOutputs, o)
	return txb.TxBuilder.ProduceOutput(o.Bytes()), nil
}

func (txb *TxBuilder) ConsumedAmount() uint64 {
	ret := uint64(0)
	for _, o := range txb.ConsumedOutputs {
		ret += o.TokenBalance()
	}
	return ret
}

func (txb *TxBuilder) ProducedAmount() (uint64, uint64) {
	retTotal := uint64(0)
	retInflation := uint64(0)
	for _, o := range txb.ProducedOutputs {
		retTotal += o.TokenBalance()
		retInflation += o.Inflation()
	}
	return retTotal, retInflation
}

// LoadInputBytes returns the raw bytes of the i-th consumed output.
// This is the loader shape expected by transaction.SetFullContext /
// transaction.ParseAndValidate.
func (txb *TxBuilder) LoadInputBytes(i byte) ([]byte, error) {
	if int(i) >= len(txb.ConsumedOutputs) {
		return nil, fmt.Errorf("can't load input #%d", i)
	}
	return txb.ConsumedOutputs[i].Bytes(), nil
}

// CalcFrozenCoverageDelta sums up frozen coverage vectors of all delegation outputs.
// The result is sized at the maximum TargetMaxFrozenEpochs observed across
// all produced delegation outputs in this tx (Phase 4 of
// delegation_epoch_params). All delegations in a sequencer's freeze tx
// target the same chain, so this is effectively the chain's
// maxFrozenEpochs.
func (txb *TxBuilder) CalcFrozenCoverageDelta() ([]int64, error) {
	maxLen := 0
	for _, o := range txb.ProducedOutputs {
		if o.Lock().Name() != ledger.DelegateLockName {
			continue
		}
		n := o.Amounts().NumElements()
		if n > maxLen {
			maxLen = n
		}
	}
	if maxLen < int(ledger.AmountIndexFrozenCoverage) {
		// no delegation outputs (or all have no FC cells)
		return nil, nil
	}
	sum := make([]int64, maxLen)
	for _, o := range txb.ProducedOutputs {
		if o.Lock().Name() == ledger.DelegateLockName {
			if overflow := o.Amounts().AddToVector(sum); overflow {
				return nil, fmt.Errorf("CalcFrozenCoverageDelta: arithmetic overflow")
			}
		}
	}
	return sum[ledger.AmountIndexFrozenCoverage:], nil
}

// MustPutFrozenCoverage adjusts the produced chain output's amounts
// vector to carry forward the predecessor's frozen coverage (shifted
// by the inter-tx epoch difference) plus the per-epoch deltas from
// produced delegation outputs. Phase 4 of delegation_epoch_params:
// epochSlots and maxFrozenEpochs come from this chain's own
// delegationParams (at ConstraintIndexDelegationParams on the produced
// chain output).
func (txb *TxBuilder) MustPutFrozenCoverage(producedOutputIdx byte, frozenCoverageDeltaVector []int64, targetTs base.LedgerTime) {
	o := txb.ProducedOutputs[producedOutputIdx]

	lib := ledger.L(targetTs.Slot)

	// Read this chain's delegationParams. Required: a chain receiving
	// frozen coverage must opt in.
	dpBytes, dpErr := o.At(int(ledger.ConstraintIndexDelegationParams))
	util.Assertf(dpErr == nil && len(dpBytes) > 0,
		"MustPutFrozenCoverage: produced chain output must carry delegationParams to receive frozen coverage")
	dp, err := ledger.DelegationParamsFromBytesWithLib(dpBytes, lib)
	util.AssertNoError(err)

	a := make([]int64, int(ledger.AmountIndexFrozenCoverage)+int(dp.MaxFrozenEpochs))
	a[ledger.AmountIndexTokenBalance] = int64(o.TokenBalance())
	a[ledger.AmountIndexInflation] = int64(o.Inflation())
	copy(a[ledger.AmountIndexFrozenCoverage:], frozenCoverageDeltaVector)

	// find the predecessor and adjust its vector
	cc := o.ChainConstraint()
	util.Assertf(cc != nil, "MustPutFrozenCoverage: inconsistency 1")
	oPred := txb.ConsumedOutputs[cc.PredecessorInputIndex]
	predVector := oPred.Amounts().FrozenCoverageVector(dp.MaxFrozenEpochs)
	predTs := txb.TxData.InputIDs[cc.PredecessorInputIndex].Timestamp()
	predVectorAdjusted := lib.AdjustFrozenCoverageVector(cc.ChainID, predVector, predTs, targetTs,
		dp.EpochSlots, dp.MaxFrozenEpochs)
	for i := range frozenCoverageDeltaVector {
		a[int(ledger.AmountIndexFrozenCoverage)+i] += predVectorAdjusted[i]
	}

	txb.ReplaceProducedOutput(producedOutputIdx, o.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(ledger.NewAmounts(a[:]...).Bytes(), ledger.ConstraintIndexAmounts)
	}))
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
	// idx[0] == 0xff means "append"; idx[0] < ConstraintIndexChain (3)
	// overwrites a mandatory pre-chain slot; idx[0] > ConstraintIndexChain
	// places at a specific extras position (pads with empty placeholders
	// if needed so the absolute output index is honoured by the
	// downstream tuple builder).
	if idx[0] == 0xff || idx[0] < ledger.ConstraintIndexChain {
		t.AddConstraints[idx[0]] = constr
		return t
	}
	util.Assertf(idx[0] > ledger.ConstraintIndexChain, "WithConstraintBinary: cannot overwrite the chain slot directly")
	// `AddConstraints` is appended in order starting at ConstraintIndexChain;
	// so the absolute output index of position `j` in the slice is
	// `ConstraintIndexChain + j`. Pad up to the target position with
	// nil entries that the output builder will skip.
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

// TotalAdjustedAmount adjust amount to minimum storage deposit requirements
func (t *TransferData) TotalAdjustedAmount() uint64 {
	if !t.AdjustToMinimum {
		// not adjust. Will render wrong transaction if not enough tokens
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
		// AddConstraints is indexed relative to ConstraintIndexChain (3):
		// entry 0 lands at output index 3, entry 1 at 4, etc. Use
		// PutConstraint with an explicit absolute index so nil padding
		// slots remain empty rather than being pushed as zero-length
		// elements.
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
			// always referencing the 0 output
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

	txb.SetTimestamp(par.Timestamp)
	txb.ComputeInputCommitment()
	txb.SignED25519(par.PrivateKey)

	inputLoader := func(i byte) (*ledger.Output, error) {
		panic("MakeSequencerTransactionWithInputLoaderOld: par.ReturnInputLoader parameter must be set to true")
	}
	if par.ReturnInputLoader {
		inputLoader = func(i byte) (*ledger.Output, error) {
			return consumedOutputs[i], nil
		}
	}
	return txb.Bytes(), inflationAmount, inputLoader, nil
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
		// Same indexing convention as MakeSimpleTransferTransactionWithRemainder.
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
	// unlock chain input
	txb.PutSignatureUnlock(outChainOutputIdx)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(outChainOutputIdx))

	// always reference chain input
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

//---------------------------------------------------------

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
	// TargetEpochSlots and TargetMaxFrozenEpochs are copies of the target
	// chain's delegationParams (Phase 5 of
	// claude/delegation_epoch_params.md). When both are zero, the
	// builder falls back to the library defaults — keeps older tests
	// that don't fetch per-target values working.
	TargetEpochSlots      uint32
	TargetMaxFrozenEpochs byte
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

	// Phase 5 of delegation_epoch_params: caller supplies the target's
	// inline params. Fall back to library defaults when not supplied
	// (legacy callers / tests using chains created with the defaults).
	targetEpochSlots := par.TargetEpochSlots
	if targetEpochSlots == 0 {
		targetEpochSlots = lib.DelegationEpochSlots
	}
	targetMaxFrozenEpochs := par.TargetMaxFrozenEpochs
	if targetMaxFrozenEpochs == 0 {
		targetMaxFrozenEpochs = byte(lib.MaxFrozenEpochs)
	}
	delegateOutput := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:                 par.Amount,
		MasterID:               par.MasterID,
		Target:                 par.Target,
		MaxFrozenEpochs:        par.MaxFrozenEpochs,
		RequiredInflationShare: par.RequiredInflationShare,
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
