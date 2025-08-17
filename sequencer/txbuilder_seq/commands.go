package txbuilder_seq

import (
	"crypto/ed25519"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

type (
	SequencerCommandBase struct {
		base.SmallPersistentMap
		ledger.MessageWithED25519Sender
		CmdCode byte
	}

	SequencerCommand interface {
		IsAuthenticated(txb *SequencerTxBuilder) bool
		Apply(txb *SequencerTxBuilder)
		RequireAdditionalOutputs() int
	}

	WithdrawCommand struct {
		SequencerCommandBase
		Amount uint64
		Target ledger.Lock
	}

	NoopCommand struct{}
)

const (
	FieldCmdCode = byte(0)

	// withdraw command fields

	MinimumAmountToRequestFromSequencer = 1_000_000

	WithdrawCmdCode     = byte(0x01)
	FieldWithdrawAmount = byte(1)
	FieldWithdrawTarget = byte(2)
)

// ParseCommandFromOutput ok flag is false if command does not exist or si malformed
func ParseCommandFromOutput(o *ledger.Output) SequencerCommand {
	msg, idx := o.MessageWithED25519Sender()
	if idx == 0xff {
		return NoopCommand{}
	}
	m, err := base.SmallPersistentMapFromBytes(msg.Msg)
	if err != nil {
		return NoopCommand{}
	}
	cmd := m.Get(FieldCmdCode)
	if cmd == nil || len(cmd) != 1 {
		return NoopCommand{}
	}

	cmdBase := SequencerCommandBase{
		SmallPersistentMap:       m,
		MessageWithED25519Sender: *msg,
		CmdCode:                  cmd[0],
	}

	switch cmdBase.CmdCode {
	case WithdrawCmdCode:
		if ret, ok := parseWithdrawCommand(cmdBase); ok {
			return ret
		}
	}
	return NoopCommand{}
}

func parseWithdrawCommand(cmdBase SequencerCommandBase) (*WithdrawCommand, bool) {
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
}

func NewWithdrawCommandBytecode(privKey ed25519.PrivateKey, amount uint64, target ledger.Lock) []byte {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{WithdrawCmdCode})
	body.Set(FieldWithdrawAmount, easyfl_util.TrimmedLeadingZeroUint64(amount))
	body.Set(FieldWithdrawTarget, target.Bytes())

	msg := ledger.NewMessageWithED25519SenderFromPublicKey(privKey.Public().(ed25519.PublicKey), body.Bytes())
	return msg.Bytes()
}

func (cmd *WithdrawCommand) IsAuthenticated(txb *SequencerTxBuilder) bool {
	pubKey := txb.privateKey.Public().(ed25519.PublicKey)
	return cmd.MessageWithED25519Sender.SenderHash == blake2b.Sum256(pubKey)
}

func (cmd *WithdrawCommand) Apply(txb *SequencerTxBuilder) {
	util.Assertf(cmd.IsAuthenticated(txb), "sequencer command is not authenticated")
	if cmd.Amount < MinimumAmountToRequestFromSequencer {
		// too small amount to withdraw
		return
	}
	onChainAmount := txb.producedAmounts[ledger.AmountIndexTokenBalance]
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
	txb.producedAmounts[ledger.AmountIndexTokenBalance] -= int64(cmd.Amount)
	return
}

func (cmd *WithdrawCommand) RequireAdditionalOutputs() int {
	return 1
}

func (cmd NoopCommand) IsAuthenticated(_ *SequencerTxBuilder) bool {
	return false
}

func (cmd NoopCommand) Apply(_ *SequencerTxBuilder) {
	return
}

func (cmd NoopCommand) RequireAdditionalOutputs() int {
	return 0
}
