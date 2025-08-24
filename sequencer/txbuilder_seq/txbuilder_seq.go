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
		*seqdata.SequencerData
		rdr                   multistate.IndexedStateReader
		nextSeqData           *seqdata.SequencerData
		privateKey            ed25519.PrivateKey
		chainInput            *ledger.OutputWithChainID
		stemInput             *ledger.OutputWithID // it is branch tx if != nil
		doNotInflateMainChain bool                 // default is inflate
		chainOutAmounts       [15]int64
		vrfProof              []byte
	}

	SeqCommandMessage struct {
		base.SmallPersistentMap
		ledger.MessageWithED25519Sender
		CmdCode byte
	}

	TxBuilderCommand interface {
		Apply(txb *SeqTxBuilder) error
	}

	SeqCommandBase struct {
		o ledger.OutputWithID
	}

	cmdParser func(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqCommandMessage) (cmd TxBuilderCommand, isValid bool)

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
		ret.SequencerData = seqdata.New()
	} else {
		ret.SequencerData = &sd
		ret.SequencerData.IncChainHeight()
		if stem != nil {
			ret.SequencerData.IncBranchHeight()
		}
	}
	ret.nextSeqData = ret.SequencerData.Clone()
	diffTicksChain := base.DiffTicks(ts, predecessor.Timestamp())
	if diffTicksChain < int64(ledger.L().ID.TransactionPaceSequencer) ||
		diffTicksChain < int64(ret.SequencerData.Pace()) {
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
				ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ledger.L().CalcChainInflationAmountOneSlot(ret.chainInput.Timestamp().Slot,
					ret.chainInput.Output.TokenBalance()+uint64(ret.chainInput.Output.FrozenCoverage(0))))
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

func (txb *SeqTxBuilder) AddTagAlongInput(o ledger.OutputWithID) error {
	if cmd, ok := txb.TxBuilderCommandFromOutput(o); ok {
		return cmd.Apply(txb)
	}
	return fmt.Errorf("SeqTxBuilder: cannot use output as tag-along:\n%s", o.String())
}

func (txb *SeqTxBuilder) FreezeDelegation(delegationIn *ledger.DelegationOutput) error {
	if len(txb.ConsumedOutputs) > 255 {
		return fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many inputs")
	}
	if len(txb.TransactionData.Outputs) > 254 {
		return fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many produced outputs")
	}

	if delegationIn.Target.ChainID() != txb.chainInput.ChainID {
		return fmt.Errorf("SeqTxBuilder: cannot be unlocked by the sequencer at %s", txb.TransactionData.Timestamp.String())
	}

	lastEpochToFreeze := delegationIn.LatestPossibleEpochToFreeze(txb.TransactionData.Timestamp)
	predIdx := byte(len(txb.ConsumedOutputs))
	delegationOut, requiredAdvance, projectedContributionToInflation, err := delegationIn.MakeDelegationFreezeOutput(
		txb.TransactionData.Timestamp, lastEpochToFreeze, predIdx)
	if err != nil {
		return fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
	}

	if projectedContributionToInflation < requiredAdvance+txb.SequencerData.InflationMargin(projectedContributionToInflation) {
		// makes no economic sense for the sequencer
		return fmt.Errorf("SeqTxBuilder.FreezeDelegation:  advance required by the delegation output is goo big")
	}

	idx, err := txb.ConsumeOutput(delegationIn.Output, delegationIn.ID)
	if err != nil {
		return fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
	}
	util.Assertf(idx == predIdx, "idx == predIdx")

	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(requiredAdvance)

	succIdx, err := txb.ProduceOutput(delegationOut)
	if err != nil {
		return fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
	}
	// unlock lock
	txb.PutUnlockParams(idx, 1, ledger.NewChainLockUnlockParams(0, 2), 0)
	// unlock chain
	txb.PutUnlockParams(idx, 2, ledger.NewChainUnlockParams(succIdx, 2))

	// add frozen coverage to the sequencer output
	a := delegationOut.Amounts().FrozenCoverageVector()
	for i, c := range a {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] += c
	}

	return nil
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

func (txb *SeqTxBuilder) reservedInputs() (ret int) {
	ret = 1
	if txb.stemInput != nil {
		ret = 2
	}
	return
}
