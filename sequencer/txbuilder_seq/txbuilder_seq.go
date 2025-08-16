package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

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
		onChainAmount         uint64
		onChainInflation      uint64
		vrfProof              []byte
	}
)

func New(ts base.LedgerTime,
	predecessor *ledger.OutputWithChainID,
	stem *ledger.OutputWithID,
	privateKey ed25519.PrivateKey,
	explicitBaseline ...base.TransactionID) (*SequencerTxBuilder, error) {

	ret := &SequencerTxBuilder{
		privateKey: privateKey,
		chainInput: predecessor,
		stemInput:  stem,
		TxBuilder:  txbuilder.New(),
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

	var err error
	if ret.SequencerData, err = ledger.ParseSeqMilestoneData(predecessor.Output); err != nil {
		ret.SequencerData = seqdata.New()
	} else {
		ret.SequencerData.IncChainHeight()
		if stem != nil {
			ret.SequencerData.IncBranchHeight()
		}
	}
	if len(explicitBaseline) > 0 {
		ret.PutExplicitBaseline(util.Ref(explicitBaseline[0]))
	}
	if ret.stemInput != nil {
		// calculate VRF proof for the branch
		prevStem, ok := ret.stemInput.Output.StemLock()
		util.Assertf(ok, "SequencerTxBuilderinconsistency: cannot find previous stem")

		// sign concatenation of predecessor VRFProof with slot number and next VRF proof
		msg := common.Concat(prevStem.VRFProof, ret.TransactionData.Timestamp.Slot.Bytes())
		ret.vrfProof = ed25519.Sign(ret.privateKey, msg)
	}
	if !ret.doNotInflateMainChain {
		// calculate main chain onChainInflation amount
		if ret.IsSlotBoundary() {
			// from VRF proof for branch
			util.Assertf(len(ret.vrfProof) > 0, "len(vrfProof)>0")
			ret.onChainInflation = ledger.L().BranchInflationBonusDirect(ret.vrfProof)
		} else {
			// for non-branch
			if ret.chainInput.Timestamp().Slot != ret.TransactionData.Timestamp.Slot {
				ret.onChainInflation = ledger.L().CalcChainInflationAmountOneSlot(ret.chainInput.Timestamp().Slot,
					ret.chainInput.Output.TokenBalance()+uint64(ret.chainInput.Output.FrozenCoverage(0)))
			}
		}
	}
	ret.onChainAmount = ret.chainInput.Output.TokenBalance() + ret.onChainInflation
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

func (txb *SequencerTxBuilder) BuildSequencerAndStemOutputs() error {
	if txb.onChainAmount < ledger.L().ID.MinimumAmountOnSequencer {
		return fmt.Errorf("SequencerTxBuilder: amount %s on the produced chain output is below minimum %s required for the sequencer",
			util.Th(txb.onChainAmount),
			util.Th(ledger.L().ID.MinimumAmountOnSequencer))
	}
	chainPredIdx, err := txb.ConsumeOutput(txb.chainInput.Output, txb.chainInput.ID)
	if err != nil {
		return fmt.Errorf("SequencerTxBuilder: %w", err)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	amounts := txb.chainInput.Output.Amounts()
	// TODO WIP
	txb.ProduceOutput(txb.chainInput.Output.Clone(func(o *ledger.OutputBuilder) {
		_ = amounts
	}))
	panic("implement me")

}

func (txb *SequencerTxBuilder) BytesWithValidation() ([]byte, base.TransactionID, string, error) {
	txb.SignED25519(txb.privateKey)
	return txb.TxBuilder.BytesWithValidation()
}
