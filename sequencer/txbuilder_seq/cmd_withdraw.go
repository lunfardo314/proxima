package txbuilder_seq

import (
	"crypto/ed25519"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"golang.org/x/crypto/blake2b"
)

type WithdrawCommand struct {
	SequencerCommandBase
	Amount uint64
	Target ledger.Lock
}

const (
	MinimumAmountToRequestFromSequencer = 1_000_000

	WithdrawCmdCode     = byte(1)
	FieldWithdrawAmount = byte(1)
	FieldWithdrawTarget = byte(2)
)

func init() {
	registerSequencerCommand(WithdrawCmdCode, func(cmdBase SequencerCommandBase) (SequencerCommand, bool) {
		ret := &WithdrawCommand{
			SequencerCommandBase: cmdBase,
		}
		var err error
		if ret.Amount, err = easyfl_util.Uint64FromBytes(cmdBase.Get(FieldWithdrawAmount)); err != nil {
			return nil, false
		}
		if ret.Target, err = ledger.LockFromBytes(cmdBase.Get(FieldWithdrawTarget)); err != nil {
			return nil, false
		}
		return ret, true
	})
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

func (cmd *WithdrawCommand) CheckPreconditions(txb *SequencerTxBuilder) (isAuth bool, consume bool, producesOutputs int) {
	pubKey := txb.privateKey.Public().(ed25519.PublicKey)
	consume = true
	if isAuth = cmd.MessageWithED25519Sender.SenderHash == blake2b.Sum256(pubKey); isAuth {
		producesOutputs = 1
	}
	return
}

func (cmd *WithdrawCommand) Apply(txb *SequencerTxBuilder) {
	if cmd.Amount < MinimumAmountToRequestFromSequencer {
		// too small amount to withdraw
		return
	}
	onChainAmount := txb.chainOutAmounts[ledger.AmountIndexTokenBalance]
	if onChainAmount <= int64(cmd.Amount) || onChainAmount-int64(cmd.Amount) < int64(ledger.L().ID.MinimumAmountOnSequencer) {
		// insufficient balance on the chain
		return
	}

	_, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(cmd.Amount).WithLock(cmd.Target)
	}))
	if err != nil {
		return
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(cmd.Amount)
	return
}
