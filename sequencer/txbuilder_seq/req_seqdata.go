package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type SetSequencerDataTxBuilderCommand struct {
	ledger.TagAlongOutput
	seqdata.SequencerData
}

const (
	RequestCodeSetSequencerData = byte(2)

	FieldSetSequencerDataBinary = 'd'
)

func setSequencerDataOutputParser(txb *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, isValid bool, err error) {
	if o.Output.NumConstraints() != 3 {
		// unexpected structure -> may be an attack
		err = fmt.Errorf("exactly 3 constraints expected in the 'set sequencer data' request")
		return
	}
	util.Assertf(o.RequestParams != nil, "o.RequestParams != nil")

	// check authorisation
	if o.SenderID != base.SpenderIDFromPublicKey(txb.signatureType, txb.publicKey) {
		// wrong sender -> may be attack
		err = fmt.Errorf("sender hash does not match public key of the owner (authorisation failure)")
		return
	}
	sd, err := seqdata.FromBytes(o.RequestParams.Get(FieldSetSequencerDataBinary))
	if err != nil {
		err = fmt.Errorf("ParseSetSequencerDataRequest: failed parse: %v", err)
		return
	}
	return &SetSequencerDataTxBuilderCommand{
		TagAlongOutput: o.TagAlongOutput,
		SequencerData:  sd,
	}, true, nil
}

func (c *SetSequencerDataTxBuilderCommand) Apply(txb *SeqTxBuilder) (bool, error) {
	if len(txb.ConsumedOutputs)+txb.reservedInputs() > 256 {
		return true, fmt.Errorf("SetSequencerDataTxBuilderCommand: too many inputs")
	}
	idx, err := txb.ConsumeOutput(c.Output, c.ID)
	util.AssertNoError(err)

	txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(c.Output.TokenBalance())
	txb.nextSeqData = c.SequencerData.Clone()
	return true, nil
}

func (c *SetSequencerDataTxBuilderCommand) Lines(prefix ...string) *lines.Lines {
	return lines.New(prefix...).Add("SetSequencerDataTxBuilderCommand: seqData = " + c.SequencerData.Lines().Join(","))
}

func NewSeqDataCommandOutput(seqID base.ChainID, sender ledger.SigLock, fee uint64, newParams *seqdata.SequencerData) *ledger.Output {
	par := base.NewSmallPersistentMap()
	par.Set(FieldCmdCode, []byte{RequestCodeSetSequencerData})
	par.Set(FieldSetSequencerDataBinary, newParams.Bytes())
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&ledger.TagAlongLock{
			TargetSequencerID: seqID,
			SenderID:          base.SpenderID(sender),
		})
		o.MustPushConstraint(easyfl.InlineDataBytecode(par.Bytes()))
	})
}

func (c *SetSequencerDataTxBuilderCommand) AttachmentCostDelta() int {
	// +1 for the consumed tag-along input, no additional outputs
	return 1
}
