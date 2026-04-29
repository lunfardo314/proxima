package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type WithdrawFromChainTxBuilderCommand struct {
	ledger.TagAlongOutput
	amount uint64
	target ledger.Lock
}

const (
	MinimumAmountToRequestFromSequencer = 1_000_000

	RequestCodeWithdrawFromSeq = byte(1)
	FieldWithdrawAmount        = 'a'
	FieldWithdrawTarget        = 't'
)

func withdrawFromSeqRequestParser(txb *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, isValid bool, err error) {
	if o.Output.NumConstraints() != 3 {
		// unexpected structure -> may be attack
		err = fmt.Errorf("WithdrawFromChainTxBuilderCommand: parse failed, unexpected structure of the output")
		return
	}
	// check authorisation
	ownSenderID := base.HolderIDFromPublicKey(txb.signatureType, txb.publicKey)
	if o.SenderID != ownSenderID {
		// wrong sender -> may be an attack
		err = fmt.Errorf("WithdrawFromChainTxBuilderCommand: sender can't withdraw funds from the sequencer (authorisation failure)")
		return
	}
	ret := &WithdrawFromChainTxBuilderCommand{TagAlongOutput: o.TagAlongOutput}
	if ret.amount, err = easyfl_util.Uint64FromBytes(o.RequestParams.Get(FieldWithdrawAmount)); err != nil {
		err = fmt.Errorf("WithdrawFromChainTxBuilderCommand: amount not specified")
		return
	}
	if ret.amount < MinimumAmountToRequestFromSequencer {
		// too small amount to withdraw
		err = fmt.Errorf("WithdrawFromChainTxBuilderCommand: requested amount %d is less than minimum alowed", ret.amount)
		return
	}
	// Uses latest library version - upgrade code must maintain backward-compatible parsing
	if ret.target, err = ledger.LockFromBytesWithLib(o.RequestParams.Get(FieldWithdrawTarget), ledger.L(base.MaxSlot)); err != nil {
		err = fmt.Errorf("WithdrawFromChainTxBuilderCommand: failed to parse lock: %w", err)
		return
	}
	return ret, true, nil
}

func NewWithdrawRequestOutput(withdrawFromChain base.ChainID, sender ledger.SigLock, fee, amount uint64, target ledger.Lock) *ledger.Output {
	par := base.NewSmallPersistentMap()
	par.Set(FieldCmdCode, []byte{RequestCodeWithdrawFromSeq})
	par.Set(FieldWithdrawAmount, easyfl_util.TrimmedLeadingZeroUint64(amount))
	par.Set(FieldWithdrawTarget, target.Bytes())
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&ledger.TagAlongLock{
			TargetSequencerID: withdrawFromChain,
			SenderID:          base.HolderID(sender),
		})
		o.MustPushConstraint(easyfl.InlineDataBytecode(par.Bytes()))
	})
}

func (c *WithdrawFromChainTxBuilderCommand) Apply(txb *SeqTxBuilder) (bool, error) {
	if len(txb.ConsumedOutputs)+txb.reservedInputs() > 256 {
		return true, fmt.Errorf("WithdrawFromChainTxBuilderCommand: too many inputs")
	}
	onChainAmount := txb.chainOutAmounts[ledger.AmountIndexTokenBalance]
	if onChainAmount <= int64(c.amount) {
		return false, fmt.Errorf("WithdrawFromChainTxBuilderCommand: insufficient balance on chain")
	}
	idx, err := txb.ConsumeOutput(c.Output, c.ID)
	util.AssertNoError(err)

	txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(c.Output.TokenBalance())

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(c.amount).WithLock(c.target)
	}))
	if err != nil {
		return true, fmt.Errorf("WithdrawFromChainTxBuilderCommand: %w", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(c.amount)
	return true, nil
}

func (c *WithdrawFromChainTxBuilderCommand) Lines(prefix ...string) *lines.Lines {
	return lines.New(prefix...).Add("WithdrawFromChainTxBuilderCommand: amount = %s, target = %s", util.Th(c.amount), c.target.String())
}

func (c *WithdrawFromChainTxBuilderCommand) AttachmentCostDelta() int {
	// +1 for the consumed tag-along input, +1 for the withdrawal output
	return 2
}
