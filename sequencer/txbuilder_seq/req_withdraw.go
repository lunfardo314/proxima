package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

type WithdrawRequest struct {
	o      ledger.OutputWithID
	amount uint64
	target ledger.Lock
}

const (
	MinimumAmountToRequestFromSequencer = 1_000_000

	WithdrawCmdCode     = byte(1)
	FieldWithdrawAmount = byte(1)
	FieldWithdrawTarget = byte(2)
)

func init() {
	registerSequencerCommand(WithdrawCmdCode, _parseWithdrawOutput)
}

func _parseWithdrawOutput(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqRequestMessage) (cmd TxBuilderCommand, isValid bool, err error) {
	if o.Output.NumConstraints() != 3 {
		// unexpected structure -> may be attack
		err = fmt.Errorf("WithdrawRequest: parse failed, unexpected structure of the output")
		return
	}
	// authenticate
	publicKey := txb.privateKey.Public().(ed25519.PublicKey)
	if msg.SenderHash != blake2b.Sum256(publicKey) {
		// wrong sender -> may be attack
		err = fmt.Errorf("WithdrawRequest: sender can't update sequncer data (failed authorisation)")
		return
	}
	ret := &WithdrawRequest{o: o}
	if ret.amount, err = easyfl_util.Uint64FromBytes(msg.Get(FieldWithdrawAmount)); err != nil {
		err = fmt.Errorf("WithdrawRequest: amount not specified")
		return
	}
	if ret.amount < MinimumAmountToRequestFromSequencer {
		// too small amount to withdraw
		err = fmt.Errorf("WithdrawRequest: requested amount %d is less than minimum alowed", ret.amount)
		return
	}
	if ret.target, err = ledger.LockFromBytes(msg.Get(FieldWithdrawTarget)); err != nil {
		err = fmt.Errorf("WithdrawRequest: failed to parse lock: %w", err)
		return
	}
	return ret, true, nil

}

func NewWithdrawRequestBytecode(privKey ed25519.PrivateKey, amount uint64, target ledger.Lock) *ledger.MessageWithED25519Sender {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{WithdrawCmdCode})
	body.Set(FieldWithdrawAmount, easyfl_util.TrimmedLeadingZeroUint64(amount))
	body.Set(FieldWithdrawTarget, target.Bytes())

	return ledger.NewMessageWithED25519SenderFromPrivateKey(privKey, body.Bytes())
}

func NewWithdrawCommandOutput(targetChain base.ChainID, privKey ed25519.PrivateKey, fee, amount uint64, target ledger.Lock) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee).WithLock(ledger.ChainLockFromChainID(targetChain))
		o.MustPushConstraint(NewWithdrawRequestBytecode(privKey, amount, target).Bytes())
	})
}

func (cmd *WithdrawRequest) Apply(txb *SeqTxBuilder) (bool, error) {
	if len(txb.ConsumedOutputs)+txb.reservedInputs() > 256 {
		return true, fmt.Errorf("WithdrawRequest: too many inputs")
	}
	onChainAmount := txb.chainOutAmounts[ledger.AmountIndexTokenBalance]
	if onChainAmount <= int64(cmd.amount) || onChainAmount-int64(cmd.amount) < int64(ledger.L().ID.MinimumAmountOnSequencer) {
		return false, fmt.Errorf("WithdrawRequest: insufficient balance on chain")
	}
	_, err := txb.ConsumeTagAlongOutputUnlock(cmd.o.Output, cmd.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	util.AssertNoError(err)
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(cmd.o.Output.TokenBalance())

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(cmd.amount).WithLock(cmd.target)
	}))
	if err != nil {
		return true, fmt.Errorf("WithdrawRequest: %w", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(cmd.amount)
	return true, nil
}
