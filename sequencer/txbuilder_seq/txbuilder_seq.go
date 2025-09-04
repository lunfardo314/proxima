package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

type (
	SeqTxBuilder struct {
		*txbuilder.TxBuilder
		origSeqData           *seqdata.SequencerData
		rdr                   multistate.IndexedStateReader
		nextSeqData           *seqdata.SequencerData
		privateKey            ed25519.PrivateKey
		chainInput            *ledger.OutputWithChainID
		stemInput             *ledger.OutputWithID // it is branch tx if != nil
		doNotInflateMainChain bool                 // default is inflate
		chainOutAmounts       [15]int64
		vrfProof              []byte
	}

	SeqRequestMessage struct {
		base.SmallPersistentMap
		ledger.MessageWithED25519Sender
		CmdCode byte
	}

	TxBuilderCommand interface {
		// Apply valid=false means it is permanently invalid, err is a reason why not possibe to apply it
		Apply(txb *SeqTxBuilder) (valid bool, err error)
	}

	SeqCommandBase struct {
		o ledger.OutputWithID
	}

	cmdParser func(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqRequestMessage) (cmd TxBuilderCommand, valid bool, err error)

	SimpleTagAlongOutput struct {
		SeqCommandBase
	}
)

// New initializes sequencer tx builder and performs necessary validity check
func New(ts base.LedgerTime,
	predecessor *ledger.OutputWithChainID,
	stem *ledger.OutputWithID,
	privateKey ed25519.PrivateKey,
	rdr multistate.IndexedStateReader) (*SeqTxBuilder, error) {

	ret := &SeqTxBuilder{
		privateKey: privateKey,
		chainInput: predecessor,
		stemInput:  stem,
		TxBuilder:  txbuilder.New(),
		rdr:        rdr,
	}

	var err error
	sd, err := ledger.ParseSequencerData(predecessor.Output)

	if err != nil {
		ret.origSeqData = seqdata.New()
	} else {
		ret.origSeqData = &sd
		ret.origSeqData.IncChainHeight()
		if stem != nil {
			ret.origSeqData.IncBranchHeight()
		}
	}
	ret.nextSeqData = ret.origSeqData.Clone()
	diffTicksChain := base.DiffTicks(ts, predecessor.Timestamp())
	if diffTicksChain < int64(ledger.L().ID.TransactionPaceSequencer) ||
		diffTicksChain < int64(ret.origSeqData.Pace()) {
		return nil, fmt.Errorf("SeqTxBuilder: pace constraint violated: %s", ts.String())
	}

	ret.TransactionData.Timestamp = ts

	if ret.IsSlotBoundary() {
		if stem == nil {
			return nil, fmt.Errorf("SeqTxBuilder: wrong timestamp or stem for branch transaction: %s", ts.String())
		}
	} else {
		if !ledger.L().ID.IsPostBranchConsolidationTimestamp(ts) {
			return nil, fmt.Errorf("SeqTxBuilder: timestamp violates post-branch timestamp constraint: %s", ts.String())
		}
	}

	if ret.stemInput != nil {
		// calculate VRF proof for the branch
		prevStem, ok := ret.stemInput.Output.StemLock()
		util.Assertf(ok, "SequencerTxBuilderinconsistency: cannot find previous stem")

		// sign concatenation of predecessor VRFProof with slot number and next VRF proof
		msg := common.Concat(prevStem.VRFProof, ret.TransactionData.Timestamp.Slot.Bytes())
		ret.vrfProof = ed25519.Sign(ret.privateKey, msg)
	}

	// form initial amounts vector

	if !ret.doNotInflateMainChain {
		// calculate main chain inflation amount
		if ret.IsSlotBoundary() {
			// from VRF proof for branch
			util.Assertf(len(ret.vrfProof) > 0, "len(vrfProof)>0")
			ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ledger.L().BranchInflationBonusDirect(ret.vrfProof))
		} else {
			// for non-branch
			if ret.chainInput.Timestamp().Slot != ret.TransactionData.Timestamp.Slot {
				ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ledger.L().ChainInflationOneSlot(
					ret.chainInput.Output.TokenBalance()+uint64(ret.chainInput.Output.FrozenCoverage(0)),
					uint32(ret.chainInput.Timestamp().Slot),
				))
			}
		}
	}
	predAmounts := predecessor.Output.Amounts()
	ret.chainOutAmounts[ledger.AmountIndexTokenBalance] = int64(predAmounts.TokenBalance()) + ret.chainOutAmounts[ledger.AmountIndexInflation]

	// frozen coverage at the predecessor adjusted to the epoch of the successor
	dconst := ledger.DelegationConst()
	diffEpochsInt := dconst.DiffEpochs(predecessor.ChainID, ts, predecessor.Timestamp())
	util.Assertf(diffEpochsInt >= 0, "diffEpochsInt>=0")
	diffEpochs := uint32(diffEpochsInt)

	predecessorFrozenCoverageAdjusted := func(i uint32) (ret int64) {
		if idx := i + diffEpochs; idx < dconst.MaxFrozenEpochs {
			ret = predAmounts.FrozenCoverageAt(byte(idx))
		}
		return
	}
	for i := uint32(0); i < dconst.MaxFrozenEpochs; i++ {
		ret.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] = predecessorFrozenCoverageAdjusted(i)
	}

	// consume chain and stem (optionally) outputs but do not unlock it
	idx, err := ret.ConsumeOutput(ret.chainInput.Output, ret.chainInput.ID)
	util.AssertNoError(err)
	util.Assertf(idx == 0, "idx==0")

	if stem != nil {
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
	return New(ts, &seqIn, stemIn, privateKey, rdr)
}

func (txb *SeqTxBuilder) IsSlotBoundary() bool {
	return txb.TransactionData.Timestamp.IsSlotBoundary()
}

func (txb *SeqTxBuilder) SetInflateMainChain(inflate bool) {
	txb.doNotInflateMainChain = !inflate
}

func (txb *SeqTxBuilder) AddEndorsement(txid base.TransactionID) error {
	txb.TransactionData.Endorsements = append(txb.TransactionData.Endorsements, txid)
	if len(txb.TransactionData.Endorsements) > int(ledger.L().ID.MaxNumberOfEndorsements) {
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
	case ledger.AddressED25519Name:
		if err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0); err != nil {
			return fmt.Errorf("AddSimpleInput: %v", err)
		}
	case ledger.ChainLockName:
		txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainUnlockParams(0, 2))
	default:
		return fmt.Errorf("AddSimpleInput: wrong ock type")
	}
	return nil
}

// AddTagAlongInput returns:
//
//	-- false, error if output is permanently invalid. If err != nil, it is a reason why
//	-- true, error it is temporary cannot be applied
func (txb *SeqTxBuilder) AddTagAlongInput(o ledger.OutputWithID) (valid bool, err error) {
	cmd, valid, err := txb.TxBuilderCommandFromOutput(o)
	if !valid || err != nil {
		return valid, fmt.Errorf("AddTagAlongInput: %w", err)
	}
	return cmd.Apply(txb)
}

func (txb *SeqTxBuilder) calcAdvance(delegationIn *ledger.DelegationOutput, frozenEpochs byte) (uint64, error) {
	delegatorRequirement := delegationIn.RequiredInflationShare
	seqTolerance := 1000 - txb.origSeqData.InflationProfitMarginPromille()
	if seqTolerance < delegatorRequirement {
		return 0, fmt.Errorf("SeqTxBuilder.FreezeDelegation: advance required by delegator is loss-making for the sequencer")
	}
	dconst := ledger.DelegationConst()
	frozenSlots := dconst.FrozenSlotsFromFrozenEpochs(delegationIn.Target.ChainID(), uint32(txb.TransactionData.Timestamp.Slot), frozenEpochs)
	projectedInflation := ledger.L().ChainInflation(delegationIn.Output.TokenBalance(), uint32(txb.TransactionData.Timestamp.Slot), frozenSlots)

	if txb.origSeqData.IsGreedy() {
		return (projectedInflation * uint64(delegatorRequirement)) / 1000, nil
	}
	return (projectedInflation * uint64(seqTolerance)) / 1000, nil
}

func (txb *SeqTxBuilder) FreezeDelegation(delegationIn *ledger.DelegationOutput) (successorIdx byte, err error) {
	if !delegationIn.IsUnlockableByTargetForFreezing(uint32(txb.TransactionData.Timestamp.Slot)) {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: output cannot be unlocked by the target for freezing:\n%s", delegationIn.LinesHR("   ").String())
		return
	}
	if len(txb.ConsumedOutputs) > 255 {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many inputs")
		return
	}
	if len(txb.TransactionData.Outputs) > 254 {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many produced outputs")
		return
	}
	if delegationIn.Target.ChainID() != txb.chainInput.ChainID {
		err = fmt.Errorf("SeqTxBuilder: cannot be unlocked by the sequencer at %s", txb.TransactionData.Timestamp.String())
		return
	}
	lastEpochToFreeze := delegationIn.FreezeUntilMax(txb.TransactionData.Timestamp)
	dconst := ledger.DelegationConst()
	txEpoch := dconst.EpochFromSlotDirect(delegationIn.Target.ChainID(), uint32(txb.TransactionData.Timestamp.Slot))
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

	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(advance)

	successorIdx, err = txb.ProduceOutput(delegationOut)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}
	// unlock delegation lock as target. First 2 bytes is chain unlock parameters, 3rd byte indicates it is target unlock
	txb.PutUnlockParams(idx, 1, ledger.NewChainLockUnlockParams(0, 2), ledger.DelegationUnlockedByTarget)
	// unlock chain
	txb.PutUnlockParams(idx, 2, ledger.NewChainUnlockParams(successorIdx, 2))

	// add frozen coverage to the sequencer output
	a := delegationOut.Amounts().FrozenCoverageVector()
	for i, c := range a {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] += c
	}

	return
}

func (txb *SeqTxBuilder) buildSequencerAndStemOutputs() error {
	if txb.chainOutAmounts[ledger.AmountIndexTokenBalance] < int64(ledger.L().ID.MinimumAmountOnSequencer) {
		return fmt.Errorf("SeqTxBuilder: amount %s on the produced chain output is below minimum %s required for the sequencer",
			util.Th(txb.chainOutAmounts[ledger.AmountIndexTokenBalance]),
			util.Th(ledger.L().ID.MinimumAmountOnSequencer))
	}
	// sequencer input
	txb.PutSignatureUnlock(0)

	// sequencer produced output
	var chainOutConstraintIdx byte
	chainOutIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(txb.chainOutAmounts[:]...)
		o.PutLock(txb.chainInput.Output.Lock())

		chainOutConstraint := ledger.NewChainConstraint(txb.chainInput.ChainID, 0, txb.chainInput.ChainConstraintIndex, txb.chainInput.OriginSlot, txb.chainInput.OriginAmount)
		chainOutConstraintIdx = o.MustPushConstraint(chainOutConstraint.Bytes())
		// put sequencer constraint
		sequencerConstraint := ledger.NewSequencerConstraint(chainOutConstraintIdx)
		o.MustPushConstraint(sequencerConstraint.Bytes())
		idxMsData := o.MustPushConstraint(easyfl.InlineDataBytecode(txb.nextSeqData.Bytes()))
		util.Assertf(idxMsData == ledger.SeqMilestoneDataFixedIndex, "idxMsData == SeqMilestoneDataFixedIndex")

	}))
	if err != nil {
		return fmt.Errorf("SeqTxBuilder: %w", err)
	}

	// unlock sequencer chain constraint
	txb.PutUnlockParams(0, txb.chainInput.ChainConstraintIndex, ledger.NewChainUnlockParams(chainOutIdx, chainOutConstraintIdx))
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

func (txb *SeqTxBuilder) Timestamp() base.LedgerTime {
	return txb.TransactionData.Timestamp
}

func (txb *SeqTxBuilder) Slot() uint32 {
	return uint32(txb.TransactionData.Timestamp.Slot)
}

type MakeSimpleSequencerTransactionParams struct {
	// sequencer name (set only if != ""
	Name string
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
	// Endorsements
	Endorsements []base.TransactionID
	// ExplicitBaseline or nil if none
	ExplicitBaseline *base.TransactionID
	// chain controller
	PrivateKey ed25519.PrivateKey
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
	txb, err := New(par.Timestamp, par.ChainInput, par.StemInput, par.PrivateKey, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
	}
	if par.Name != "" {
		txb.nextSeqData.SetName(par.Name)
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
	return txb.BytesWithInputLoader()
}

func MakeSimpleSequencerTransaction(par MakeSimpleSequencerTransactionParams) ([]byte, error) {
	txBytes, _, err := MakeSimpleSequencerTransactionWithInputLoader(par)
	return txBytes, err
}
