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

type WithdrawCommand struct {
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

func _parseWithdrawOutput(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqCommandMessage) (cmd TxBuilderCommand, isValid bool) {
	if o.Output.NumConstraints() != 3 {
		// unexpected structure -> may be attack
		return nil, false
	}
	// authenticate
	publicKey := txb.privateKey.Public().(ed25519.PublicKey)
	if msg.SenderHash != blake2b.Sum256(publicKey) {
		// wrong sender -> may be attack
		return
	}
	ret := &WithdrawCommand{o: o}
	var err error
	if ret.amount, err = easyfl_util.Uint64FromBytes(msg.Get(FieldWithdrawAmount)); err != nil {
		return
	}
	if ret.amount < MinimumAmountToRequestFromSequencer {
		// too small amount to withdraw
		return
	}
	if ret.target, err = ledger.LockFromBytes(msg.Get(FieldWithdrawTarget)); err != nil {
		return
	}
	return ret, true

}

func NewWithdrawCommandBytecode(privKey ed25519.PrivateKey, amount uint64, target ledger.Lock) []byte {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{WithdrawCmdCode})
	body.Set(FieldWithdrawAmount, easyfl_util.TrimmedLeadingZeroUint64(amount))
	body.Set(FieldWithdrawTarget, target.Bytes())

	msg := ledger.NewMessageWithED25519SenderFromPrivateKey(privKey, body.Bytes())
	return msg.Bytes()
}

func NewWithdrawCommandOutput(targetChain base.ChainID, privKey ed25519.PrivateKey, fee, amount uint64, target ledger.Lock) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee).WithLock(ledger.ChainLockFromChainID(targetChain))
		o.MustPushConstraint(NewWithdrawCommandBytecode(privKey, amount, target))
	})
}

func (cmd *WithdrawCommand) Apply(txb *SeqTxBuilder) error {
	if len(txb.ConsumedOutputs)+txb.reservedInputs() > 256 {
		return fmt.Errorf("WithdrawCommand: too many inputs")
	}
	onChainAmount := txb.chainOutAmounts[ledger.AmountIndexTokenBalance]
	if onChainAmount <= int64(cmd.amount) || onChainAmount-int64(cmd.amount) < int64(ledger.L().ID.MinimumAmountOnSequencer) {
		return fmt.Errorf("WithdrawCommand: insufficient balance on chain")
	}
	_, err := txb.ConsumeTagAlongOutputUnlock(cmd.o.Output, cmd.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	util.AssertNoError(err)
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(cmd.o.Output.TokenBalance())

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(cmd.amount).WithLock(cmd.target)
	}))
	if err != nil {
		return fmt.Errorf("WithdrawCommand: %w", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(cmd.amount)
	return nil
}
