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
		seqdata.SequencerData
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
	if ret.SequencerData, err = ledger.ParseSeqMilestoneData(predecessor.Output); err != nil {
		ret.SequencerData = seqdata.New()
	} else {
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

func (txb *SequencerTxBuilder) AddTagAlongInput(out *ledger.OutputWithID) error {
	panic("implement me")
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
	chainPredIdx, err := txb.ConsumeOutput(txb.chainInput.Output, txb.chainInput.ID)
	if err != nil {
		return fmt.Errorf("SequencerTxBuilder: %w", err)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	// sequencer produced output
	var chainOutConstraintIdx byte
	chainOutIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(txb.producedAmounts[:]...)
		o.PutLock(txb.chainInput.Output.Lock())

		chainOutConstraint := ledger.NewChainConstraint(txb.chainInput.ChainID, chainPredIdx, txb.chainInput.ChainConstraintIndex, txb.chainInput.OriginSlot, txb.chainInput.OriginAmount)
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
	txb.PutUnlockParams(chainPredIdx, txb.chainInput.ChainConstraintIndex, ledger.NewChainUnlockParams(chainOutIdx, chainOutConstraintIdx))
	txb.TransactionData.SequencerOutputIndex = chainOutIdx

	if txb.stemInput == nil {
		return nil
	}
	// handle stem
	_, err = txb.ConsumeOutput(txb.stemInput.Output, txb.stemInput.ID)
	if err != nil {
		return fmt.Errorf("SequencerTxBuilder: %w", err)
	}
	util.Assertf(len(txb.vrfProof) > 0, "len(txb.vrfProof)>0")

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
	txb.SignED25519(txb.privateKey)
	return txb.TxBuilder.BytesWithValidation()
}
