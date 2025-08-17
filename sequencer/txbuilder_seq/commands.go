package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type (
	SequencerCommandBase struct {
		base.SmallPersistentMap
		ledger.MessageWithED25519Sender
		CmdCode byte
	}

	SequencerCommand interface {
		Apply(txb *SequencerTxBuilder) error
	}

	WithdrawCommand struct {
		SequencerCommandBase
		Amount uint64
		Target ledger.Lock
	}
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
func ParseCommandFromOutput(o *ledger.Output) (SequencerCommand, bool) {
	msg, idx := o.MessageWithED25519Sender()
	if idx == 0xff {
		return nil, false
	}
	m, err := base.SmallPersistentMapFromBytes(msg.Msg)
	if err != nil {
		return nil, false
	}
	cmd := m.Get(FieldCmdCode)
	if cmd == nil || len(cmd) != 1 {
		return nil, false
	}

	cmdBase := SequencerCommandBase{
		SmallPersistentMap:       m,
		MessageWithED25519Sender: *msg,
		CmdCode:                  cmd[0],
	}

	switch cmdBase.CmdCode {
	case WithdrawCmdCode:
		if ret, ok := parseWithdrawCommand(cmdBase); ok {
			return ret, true
		}
	}
	return nil, false
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

func (cmd *WithdrawCommand) Apply(txb *SequencerTxBuilder) error {
	if cmd.Amount < MinimumAmountToRequestFromSequencer {
		return fmt.Errorf("SequencerCommand: insufficient amount to withdraw. Minimum is %s, got %s",
			util.Th(MinimumAmountToRequestFromSequencer), util.Th(cmd.Amount))
	}
	onChainAmount := txb.producedAmounts[ledger.AmountIndexTokenBalance]
	if onChainAmount <= int64(cmd.Amount) || onChainAmount-int64(cmd.Amount) < int64(ledger.L().ID.MinimumAmountOnSequencer) {
		return fmt.Errorf("SequencerCommand: %s is too big amount to withdraw. Remaining on-chain balance is %s",
			util.Th(cmd.Amount), util.Th(onChainAmount))
	}
	_, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(cmd.Amount).WithLock(cmd.Target)
	}))
	if err != nil {
		return err
	}
	txb.producedAmounts[ledger.AmountIndexTokenBalance] -= int64(cmd.Amount)
	return nil
}
