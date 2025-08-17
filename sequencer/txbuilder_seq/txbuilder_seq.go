package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

type (
	SequencerTxBuilder struct {
		*txbuilder.TxBuilder
		*seqdata.SequencerData
		privateKey            ed25519.PrivateKey
		chainInput            *ledger.OutputWithChainID
		stemInput             *ledger.OutputWithID // it is branch tx if != nil
		doNotInflateMainChain bool                 // default is inflate
		producedAmounts       [15]int64
		vrfProof              []byte
	}
)

// New initializes sequencer tx builder and performs necessary validity check
func New(ts base.LedgerTime,
	predecessor *ledger.OutputWithChainID,
	stem *ledger.OutputWithID,
	privateKey ed25519.PrivateKey) (*SequencerTxBuilder, error) {

	ret := &SequencerTxBuilder{
		privateKey: privateKey,
		chainInput: predecessor,
		stemInput:  stem,
		TxBuilder:  txbuilder.New(),
	}

	var err error
	sd, err := ledger.ParseSeqMilestoneData(predecessor.Output)

	if err != nil {
		ret.SequencerData = seqdata.New()
	} else {
		ret.SequencerData = &sd
		ret.SequencerData.IncChainHeight()
		if stem != nil {
			ret.SequencerData.IncBranchHeight()
		}
	}

	diffTicksChain := base.DiffTicks(ts, predecessor.Timestamp())
	if diffTicksChain < int64(ledger.L().ID.TransactionPaceSequencer) ||
		diffTicksChain < int64(ret.SequencerData.Pace()) {
		return nil, fmt.Errorf("SequencerTxBuilder: pace constraint violated: %s", ts.String())
	}

	ret.TransactionData.Timestamp = ts

	if ret.IsSlotBoundary() {
		if stem == nil {
			return nil, fmt.Errorf("SequencerTxBuilder: wrong timestamp or stem for branch transaction: %s", ts.String())
		}
	} else {
		if !ledger.L().ID.IsPostBranchConsolidationTimestamp(ts) {
			return nil, fmt.Errorf("SequencerTxBuilder: timestamp violates post-branch timestamp constraint: %s", ts.String())
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
			ret.producedAmounts[ledger.AmountIndexInflation] = int64(ledger.L().BranchInflationBonusDirect(ret.vrfProof))
		} else {
			// for non-branch
			if ret.chainInput.Timestamp().Slot != ret.TransactionData.Timestamp.Slot {
				ret.producedAmounts[ledger.AmountIndexInflation] = int64(ledger.L().CalcChainInflationAmountOneSlot(ret.chainInput.Timestamp().Slot,
					ret.chainInput.Output.TokenBalance()+uint64(ret.chainInput.Output.FrozenCoverage(0))))
			}
		}
	}
	predAmounts := predecessor.Output.Amounts()
	ret.producedAmounts[ledger.AmountIndexTokenBalance] = int64(predAmounts.TokenBalance()) + ret.producedAmounts[ledger.AmountIndexInflation]

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
		ret.producedAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] = predecessorFrozenCoverageAdjusted(i)
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

func (txb *SequencerTxBuilder) IsSlotBoundary() bool {
	return txb.TransactionData.Timestamp.IsSlotBoundary()
}

func (txb *SequencerTxBuilder) SetInflateMainChain(inflate bool) {
	txb.doNotInflateMainChain = !inflate
}

func (txb *SequencerTxBuilder) AddEndorsement(txid base.TransactionID) error {
	txb.TransactionData.Endorsements = append(txb.TransactionData.Endorsements, txid)
	if len(txb.TransactionData.Endorsements) > int(ledger.L().ID.MaxNumberOfEndorsements) {
		return fmt.Errorf("SequencerTxBuilder: too many endorsements")
	}
	return nil
}

func (txb *SequencerTxBuilder) AddTagAlongInput(o *ledger.OutputWithID) (byte, error) {
	seqCmd := ParseCommandFromOutput(o.Output)
	expectedNumberOfProducedOutputs := len(txb.TransactionData.Outputs) + seqCmd.RequireAdditionalOutputs() + 1
	if txb.TransactionData.Timestamp.IsSlotBoundary() {
		expectedNumberOfProducedOutputs++
	}
	if expectedNumberOfProducedOutputs > 255 {
		return 0, fmt.Errorf("SequencerTxBuilder: too many produced outputs")
	}
	idx, err := txb.ConsumeTagAlongOutputUnlock(o.Output, o.ID, 0, txb.chainInput.ChainConstraintIndex)
	if err != nil {
		return 0, err
	}
	txb.producedAmounts[ledger.AmountIndexTokenBalance] += int64(o.Output.TokenBalance())
	if seqCmd.IsAuthenticated(txb) {
		seqCmd.Apply(txb)
	}
	return idx, nil
}

func (txb *SequencerTxBuilder) AddDelegationInput(out *ledger.DelegateOutput) error {
	panic("implement me")
}

func (txb *SequencerTxBuilder) buildSequencerAndStemOutputs() error {
	if txb.producedAmounts[ledger.AmountIndexTokenBalance] < int64(ledger.L().ID.MinimumAmountOnSequencer) {
		return fmt.Errorf("SequencerTxBuilder: amount %s on the produced chain output is below minimum %s required for the sequencer",
			util.Th(txb.producedAmounts[ledger.AmountIndexTokenBalance]),
			util.Th(ledger.L().ID.MinimumAmountOnSequencer))
	}
	// sequencer input
	txb.PutSignatureUnlock(0)

	// sequencer produced output
	var chainOutConstraintIdx byte
	chainOutIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(txb.producedAmounts[:]...)
		o.PutLock(txb.chainInput.Output.Lock())

		chainOutConstraint := ledger.NewChainConstraint(txb.chainInput.ChainID, 0, txb.chainInput.ChainConstraintIndex, txb.chainInput.OriginSlot, txb.chainInput.OriginAmount)
		chainOutConstraintIdx = o.MustPushConstraint(chainOutConstraint.Bytes())
		// put sequencer constraint
		sequencerConstraint := ledger.NewSequencerConstraint(chainOutConstraintIdx)
		o.MustPushConstraint(sequencerConstraint.Bytes())
		idxMsData := o.MustPushConstraint(easyfl.InlineDataBytecode(txb.SequencerData.Bytes()))
		util.Assertf(idxMsData == ledger.SeqMilestoneDataFixedIndex, "idxMsData == SeqMilestoneDataFixedIndex")

	}))
	if err != nil {
		return fmt.Errorf("SequencerTxBuilder: %w", err)
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
		return fmt.Errorf("SequencerTxBuilder: %w", err)
	}
	return nil
}

func (txb *SequencerTxBuilder) BytesWithValidation() ([]byte, base.TransactionID, string, error) {
	if err := txb.buildSequencerAndStemOutputs(); err != nil {
		return nil, [32]byte{}, "", fmt.Errorf("SequencerTxBuilder: %w", err)
	}
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(txb.privateKey)
	return txb.TxBuilder.BytesWithValidation()
}
