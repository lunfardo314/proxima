package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

type (
	SeqTxBuilder struct {
		*txbuilder.TxBuilder
		*ledger.Library       // cached library for this transaction's slot
		origSeqData           *seqdata.SequencerData
		rdr                   multistate.IndexedStateReader
		nextSeqData           *seqdata.SequencerData
		signatureType         byte
		privateKey            []byte
		publicKey             []byte
		chainInput            *ledger.OutputWithChainID
		stemInput             *ledger.OutputWithID // it is branch tx if != nil
		doNotInflateMainChain bool                 // default is inflate
		chainOutAmounts       [15]int64
		vrfProof              []byte
	}

	TxBuilderCommand interface {
		// Apply valid=false means it is permanently invalid, err is a reason why not possible to apply it
		Apply(txb *SeqTxBuilder) (valid bool, err error)
		Lines(prefix ...string) *lines.Lines
		// AttachmentCostDelta returns the total attachment cost contribution of this command,
		// including the base tag-along input (+1) plus any additional inputs/outputs the command creates.
		// This value is added to seqTxCost to predict the final sequencer transaction cost.
		AttachmentCostDelta() int
	}
	Params struct {
		Timestamp             base.LedgerTime
		Predecessor           *ledger.OutputWithChainID
		Stem                  *ledger.OutputWithID
		SignatureType         byte
		PrivateKey            []byte
		PublicKey             []byte
		StateReader           multistate.IndexedStateReader
		DoNotInflateMainChain bool
	}
)

// New initializes sequencer tx builder and performs necessary validity check
func New(par Params) (*SeqTxBuilder, error) {

	ret := &SeqTxBuilder{
		Library:               ledger.L(par.Timestamp.Slot), // cached library for this transaction's slot
		signatureType:         par.SignatureType,
		privateKey:            par.PrivateKey,
		publicKey:             par.PublicKey,
		chainInput:            par.Predecessor,
		stemInput:             par.Stem,
		TxBuilder:             txbuilder.New(),
		rdr:                   par.StateReader,
		doNotInflateMainChain: par.DoNotInflateMainChain,
	}

	var err error
	sd, err := ledger.ParseSequencerData(par.Predecessor.Output)

	if err != nil {
		ret.origSeqData = seqdata.New()
	} else {
		ret.origSeqData = &sd
		ret.origSeqData.IncChainHeight()
		if par.Stem != nil {
			ret.origSeqData.IncBranchHeight()
		}
	}
	ret.nextSeqData = ret.origSeqData.Clone()
	diffTicksChain := base.DiffTicks(par.Timestamp, par.Predecessor.Timestamp())
	if diffTicksChain < int64(ret.TransactionPaceSequencer) ||
		diffTicksChain < int64(ret.origSeqData.Pace()) {
		return nil, fmt.Errorf("SeqTxBuilder: pace constraint violated: %s", par.Timestamp.String())
	}

	ret.TransactionData.Timestamp = par.Timestamp

	if ret.IsSlotBoundary() {
		if par.Stem == nil {
			return nil, fmt.Errorf("SeqTxBuilder: wrong timestamp or stem for branch transaction: %s", par.Timestamp.String())
		}
	} else {
		if !ret.IsPostBranchConsolidationTimestamp(par.Timestamp) {
			return nil, fmt.Errorf("SeqTxBuilder: timestamp violates post-branch timestamp constraint: %s", par.Timestamp.String())
		}
	}

	if ret.stemInput != nil {
		// calculate VRF proof for the branch
		prevStem, ok := ret.stemInput.Output.StemLock()
		util.Assertf(ok, "SequencerTxBuilderinconsistency: cannot find previous stem")

		// sign concatenation of predecessor VRFProof with slot number and next VRF proof
		msg := common.Concat(prevStem.VRFProof, base.Slot2Bytes(ret.TransactionData.Timestamp.Slot))
		ret.vrfProof = common.Concat(base.SignatureTypeED25519, ed25519.Sign(ret.privateKey, msg))
	}

	// form initial amounts vector

	if !ret.doNotInflateMainChain {
		// calculate main chain inflation amount
		if ret.IsSlotBoundary() {
			// from VRF proof for branch
			util.Assertf(len(ret.vrfProof) > 0, "len(vrfProof)>0")
			ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ret.Library.BranchInflationBonus(ret.vrfProof))
		} else {
			// for non-branch
			if ret.chainInput.Timestamp().Slot != ret.TransactionData.Timestamp.Slot {
				ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ret.Library.ChainInflationOneSlot(
					ret.chainInput.Output.TokenBalance()+uint64(ret.chainInput.Output.FrozenCoverage(0)),
					ret.chainInput.Timestamp().Slot,
				))
			}
		}
	}
	predAmounts := par.Predecessor.Output.Amounts()
	ret.chainOutAmounts[ledger.AmountIndexTokenBalance] = int64(predAmounts.TokenBalance()) + ret.chainOutAmounts[ledger.AmountIndexInflation]

	// frozen coverage at the predecessor adjusted to the epoch of the successor
	diffEpochsInt := ret.DiffEpochs(par.Predecessor.ChainID, par.Timestamp, par.Predecessor.Timestamp())
	util.Assertf(diffEpochsInt >= 0, "diffEpochsInt>=0")
	diffEpochs := uint32(diffEpochsInt)

	maxFrozenEpochs := ret.MaxFrozenEpochs
	predecessorFrozenCoverageAdjusted := func(i uint32) (result int64) {
		if idx := i + diffEpochs; idx < maxFrozenEpochs {
			result = predAmounts.FrozenCoverageAt(byte(idx))
		}
		return
	}
	for i := uint32(0); i < ret.MaxFrozenEpochs; i++ {
		ret.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] = predecessorFrozenCoverageAdjusted(i)
	}

	// consume chain and stem (optionally) outputs but do not unlock it
	idx, err := ret.ConsumeOutput(ret.chainInput.Output, ret.chainInput.ID)
	util.AssertNoError(err)
	util.Assertf(idx == 0, "idx==0")

	if par.Stem != nil {
		idx, err = ret.ConsumeOutput(ret.stemInput.Output, ret.stemInput.ID)
		util.AssertNoError(err)
		util.Assertf(idx == 1, "idx==1")
	}
	return ret, nil
}

func NewWithSequencerID(ts base.LedgerTime,
	seqID base.ChainID,
	privateKey ed25519.PrivateKey,
	rdr multistate.SugaredStateReader) (*SeqTxBuilder, error) {

	seqIn, err := rdr.GetChainOutputWithChainID(seqID)
	if err != nil {
		return nil, fmt.Errorf("error while retrieving chain origin for %s: %w", seqID.String(), err)
	}
	var stemIn *ledger.OutputWithID
	if ts.IsSlotBoundary() {
		stemIn = rdr.GetStemOutput()
	}
	return New(Params{
		Timestamp:     ts,
		Predecessor:   &seqIn,
		Stem:          stemIn,
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    privateKey,
		PublicKey:     privateKey.Public().(ed25519.PublicKey),
		StateReader:   rdr,
	})
}

func (txb *SeqTxBuilder) ChainInput() *ledger.OutputWithChainID {
	return txb.chainInput
}

func (txb *SeqTxBuilder) IsSlotBoundary() bool {
	return txb.TransactionData.Timestamp.IsSlotBoundary()
}

func (txb *SeqTxBuilder) SetInflateMainChain(inflate bool) {
	txb.doNotInflateMainChain = !inflate
}

func (txb *SeqTxBuilder) AddEndorsement(txid base.TransactionID) error {
	txb.TransactionData.Endorsements = append(txb.TransactionData.Endorsements, txid)
	if len(txb.TransactionData.Endorsements) > int(txb.MaxNumberOfEndorsements) {
		return fmt.Errorf("SeqTxBuilder: too many endorsements")
	}
	return nil
}

// AddSimpleInput output must have 2 constraints and lock must be address25519 or chainLock
func (txb *SeqTxBuilder) AddSimpleInput(o ledger.OutputWithID) error {
	idx, err := txb.TxBuilder.ConsumeOutput(o.Output, o.ID)
	if err != nil {
		return fmt.Errorf("AddSimpleInput: %v", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(o.Output.TokenBalance())
	switch o.Output.Lock().Name() {
	case ledger.SigLockName:
		if err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0); err != nil {
			return fmt.Errorf("AddSimpleInput: %v", err)
		}
	case ledger.ChainLockName:
		txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
	default:
		return fmt.Errorf("AddSimpleInput: wrong ock type")
	}
	return nil
}

// AddTagAlongInput returns:
//
//	-- false, error if output is permanently invalid. If err != nil, it is a reason why
//	-- true, error it is temporary cannot be applied
func (txb *SeqTxBuilder) AddTagAlongInput(o ledger.OutputWithID) (cmd TxBuilderCommand, valid bool, err error) {
	if cmd, valid, err = txb.TxBuilderCommandFromOutput(o); err == nil {
		valid, err = cmd.Apply(txb)
	}
	if err != nil {
		err = fmt.Errorf("AddTagAlongInput: %w", err)
	}
	return
}

func (txb *SeqTxBuilder) calcAdvance(delegationIn *ledger.DelegationOutput, frozenEpochs byte) (uint64, error) {
	delegatorRequirement := delegationIn.RequiredInflationShare
	seqTolerance := 1000 - txb.origSeqData.InflationProfitMarginPromille()
	if seqTolerance < delegatorRequirement {
		return 0, fmt.Errorf("SeqTxBuilder.FreezeDelegation: advance required by delegator is loss-making for the sequencer")
	}
	frozenSlots := txb.FrozenSlotsFromFrozenEpochs(delegationIn.Target.ChainID(), txb.TransactionData.Timestamp.Slot, frozenEpochs)
	projectedInflation := txb.Library.ChainInflationMultiStep(delegationIn.Output.TokenBalance(), txb.TransactionData.Timestamp.Slot, frozenSlots)

	if txb.origSeqData.IsGreedy() {
		return (projectedInflation * uint64(delegatorRequirement)) / 1000, nil
	}
	return (projectedInflation * uint64(seqTolerance)) / 1000, nil
}

func (txb *SeqTxBuilder) FreezeDelegation(delegationIn *ledger.DelegationOutput, freezeUntilEpoch ...uint32) (successorIdx byte, valid bool, err error) {
	if !delegationIn.IsUnlockableByTargetForFreezing(txb.TransactionData.Timestamp.Slot) {
		valid = true
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: output cannot be unlocked by the target for freezing:\n%s", delegationIn.LinesHRFull("   ").String())
		return
	}
	if len(txb.ConsumedOutputs) > 255 {
		valid = true
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many inputs")
		return
	}
	if len(txb.TransactionData.Outputs) > 254 {
		valid = true
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many produced outputs")
		return
	}
	if delegationIn.Target.ChainID() != txb.chainInput.ChainID {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: cannot be unlocked by the sequencer at %s", txb.TransactionData.Timestamp.String())
		return
	}
	txEpoch := txb.EpochFromSlotDirect(delegationIn.Target.ChainID(), txb.TransactionData.Timestamp.Slot)

	freezeMaxEpoch := delegationIn.FreezeUntilMax(txb.TransactionData.Timestamp)
	var lastEpochToFreeze uint32
	if len(freezeUntilEpoch) > 0 && freezeUntilEpoch[0] <= freezeMaxEpoch && freezeUntilEpoch[0] >= txEpoch {
		lastEpochToFreeze = freezeUntilEpoch[0]
	} else {
		lastEpochToFreeze = freezeMaxEpoch
	}
	util.Assertf(lastEpochToFreeze >= txEpoch, "lastEpochToFreeze>=txEpoch")

	frozenEpochs := lastEpochToFreeze - txEpoch + 1
	var advance uint64
	if advance, err = txb.calcAdvance(delegationIn, byte(frozenEpochs)); err != nil {
		return
	}
	predIdx := byte(len(txb.ConsumedOutputs))
	delegationOut, err := delegationIn.MakeDelegationFreezeOutput(
		txb.TransactionData.Timestamp, lastEpochToFreeze, predIdx, advance)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}

	idx, err := txb.ConsumeOutput(delegationIn.Output, delegationIn.ID)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}
	util.Assertf(idx == predIdx, "idx == predIdx")

	successorIdx, err = txb.ProduceOutput(delegationOut)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(advance)
	// unlock delegation lock as target. First byte is chain lock unlock, 2nd byte indicates it is target unlock
	txb.PutUnlockParams(idx, 1, ledger.NewChainLockUnlockParams(0), ledger.DelegationUnlockedByTarget)
	// unlock chain
	txb.PutUnlockParams(idx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(successorIdx))

	// add frozen coverage to the sequencer output
	a := delegationOut.Amounts().FrozenCoverageVector(byte(txb.Library.MaxFrozenEpochs))
	for i, c := range a {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] += c
	}
	valid = true
	return
}

func (txb *SeqTxBuilder) AddWithdrawOutput(o *ledger.Output) error {
	if o.Inflation() != 0 || !o.Amounts().IsFrozenCoverageZero(byte(txb.Library.MaxFrozenEpochs)) {
		return fmt.Errorf("AddWithdrawOutput: only token balance can be non-zero")
	}
	amount := o.TokenBalance()
	if txb.chainOutAmounts[ledger.AmountIndexTokenBalance] < int64(amount) {
		return fmt.Errorf("AddWithdrawOutput: not enough token balance")
	}
	if _, err := txb.ProduceOutput(o); err != nil {
		return fmt.Errorf("AddWithdrawOutput: %w", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(o.TokenBalance())
	return nil
}

func (txb *SeqTxBuilder) buildSequencerAndStemOutputs() error {
	// sequencer input
	txb.PutSignatureUnlock(0)

	// sequencer produced output
	chainOutIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(txb.chainOutAmounts[:]...)
		o.PutLock(txb.chainInput.Output.Lock())

		// chain constraint at fixed index 2
		chainOutConstraint := ledger.NewChainConstraint(txb.chainInput.ChainID, 0, txb.chainInput.OriginSlot, txb.chainInput.OriginAmount)
		o.PutConstraint(chainOutConstraint.Bytes(), ledger.ConstraintIndexChain)
		// sequencer constraint (no parameters)
		sequencerConstraint := ledger.NewSequencerConstraint()
		o.MustPushConstraint(sequencerConstraint.Bytes())
		idxMsData := o.MustPushConstraint(easyfl.InlineDataBytecode(txb.nextSeqData.Bytes()))
		util.Assertf(idxMsData == ledger.SeqMilestoneDataFixedIndex, "idxMsData == SeqMilestoneDataFixedIndex")

	}))
	if err != nil {
		return fmt.Errorf("SeqTxBuilder: %w", err)
	}

	// unlock sequencer chain constraint
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(chainOutIdx))
	txb.TransactionData.SequencerOutputIndex = chainOutIdx

	if txb.stemInput == nil {
		return nil
	}
	// handle stem
	stemOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(txb.stemInput.Output.TokenBalance()))
		o.WithLock(&ledger.StemLock{
			PredecessorOutputID: txb.stemInput.ID,
			VRFProof:            txb.vrfProof,
		})
	})
	txb.TransactionData.StemOutputIndex, err = txb.ProduceOutput(stemOut)
	if err != nil {
		return fmt.Errorf("SeqTxBuilder: %w", err)
	}
	return nil
}

func (txb *SeqTxBuilder) BuildTransactionWithValidation() (*transaction.Transaction, error) {
	if err := txb.buildSequencerAndStemOutputs(); err != nil {
		return nil, fmt.Errorf("SeqTxBuilder: %w", err)
	}
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(txb.privateKey)
	return txb.TxBuilder.BuildTransactionWithValidation()
}

func (txb *SeqTxBuilder) BytesWithValidation() ([]byte, base.TransactionID, string, error) {
	if err := txb.buildSequencerAndStemOutputs(); err != nil {
		return nil, [32]byte{}, "", fmt.Errorf("SeqTxBuilder: %w", err)
	}
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(txb.privateKey)
	return txb.TxBuilder.BytesWithValidation()
}

func (txb *SeqTxBuilder) BytesWithInputLoader() ([]byte, func(i byte) (*ledger.Output, error), error) {
	if err := txb.buildSequencerAndStemOutputs(); err != nil {
		return nil, nil, fmt.Errorf("SeqTxBuilder: %w", err)
	}
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(txb.privateKey)

	return txb.TxBuilder.TransactionData.Bytes(), txb.TxBuilder.LoadInput, nil
}

func (txb *SeqTxBuilder) reservedInputs() (ret int) {
	ret = 1
	if txb.stemInput != nil {
		ret = 2
	}
	return
}

func (txb *SeqTxBuilder) StateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(txb.rdr)
}

func (txb *SeqTxBuilder) InputsAreFull() bool {
	return txb.NumInputs()+txb.reservedInputs() >= 256
}

// AttachmentCost returns the predicted final attachment cost of the sequencer transaction.
// This is the sum of inputs and outputs, including chain output and stem output (if branch)
// that will be added at finalization.
func (txb *SeqTxBuilder) AttachmentCost() int {
	// Current inputs + current outputs + chain output (always 1)
	cost := txb.NumInputs() + txb.NumOutputs() + 1
	if txb.stemInput != nil {
		// Stem output will be added for branch transactions
		cost++
	}
	return cost
}

func (txb *SeqTxBuilder) Timestamp() base.LedgerTime {
	return txb.TransactionData.Timestamp
}

func (txb *SeqTxBuilder) Slot() uint32 {
	return txb.TransactionData.Timestamp.Slot
}

func (txb *SeqTxBuilder) SetName(name string) {
	txb.nextSeqData.SetName(name)
}

type MakeSimpleSequencerTransactionParams struct {
	// sequencer name (set only if != ""
	SeqName string
	// transaction ts
	Timestamp base.LedgerTime
	// predecessor
	ChainInput *ledger.OutputWithChainID
	//
	StemInput *ledger.OutputWithID // it is branch tx if != nil
	// timestamp of the transaction
	// additional inputs to consume. Must be unlockable by chain
	// can contain sender commands to the sequencer
	AdditionalInputs []*ledger.OutputWithID
	// withdraw outputs
	WithdrawOutputs []*ledger.Output
	// Endorsements
	Endorsements []base.TransactionID
	// ExplicitBaseline or nil if none
	ExplicitBaseline *base.TransactionID
	// private key type
	SignatureType byte
	// chain controller
	PrivateKey []byte
	//
	PublicKey []byte
	//
	DoNotInflateMainChain bool
	//
	AttachmentBudget uint16
}

// MakeSimpleSequencerTransactionWithInputLoader usually used in tests
func MakeSimpleSequencerTransactionWithInputLoader(par MakeSimpleSequencerTransactionParams) ([]byte, func(i byte) (*ledger.Output, error), error) {
	if !ledger.ValidSequencerPace(par.ChainInput.Timestamp(), par.Timestamp) {
		return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: sequencer pace constraint violated with chain input")
	}
	if par.StemInput != nil {
		if !ledger.ValidSequencerPace(par.StemInput.Timestamp(), par.Timestamp) {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: sequencer pace constraint violated with stem input")
		}
	}
	for _, o := range par.AdditionalInputs {
		if !ledger.ValidSequencerPace(o.Timestamp(), par.Timestamp) {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: sequencer pace constraint violated with additional input")
		}
	}
	txb, err := New(Params{
		Timestamp:             par.Timestamp,
		Predecessor:           par.ChainInput,
		Stem:                  par.StemInput,
		SignatureType:         par.SignatureType,
		PrivateKey:            par.PrivateKey,
		PublicKey:             par.PublicKey,
		DoNotInflateMainChain: par.DoNotInflateMainChain,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
	}
	if par.SeqName != "" {
		txb.SetName(par.SeqName)
	}
	for _, endorsement := range par.Endorsements {
		if err = txb.AddEndorsement(endorsement); err != nil {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
		}
	}
	if par.ExplicitBaseline != nil {
		if !par.ExplicitBaseline.IsBranchTransaction() {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: explicit baseline must be a branch transaction ID, got %s", par.ExplicitBaseline.StringShort())
		}
		txb.PutExplicitBaseline(par.ExplicitBaseline)
	}
	for _, o := range par.AdditionalInputs {
		if err = txb.AddSimpleInput(*o); err != nil {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
		}
	}
	for _, o := range par.WithdrawOutputs {
		if err = txb.AddWithdrawOutput(o); err != nil {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
		}
	}
	return txb.BytesWithInputLoader()
}

func MakeSimpleSequencerTransaction(par MakeSimpleSequencerTransactionParams) ([]byte, error) {
	txBytes, _, err := MakeSimpleSequencerTransactionWithInputLoader(par)
	return txBytes, err
}
