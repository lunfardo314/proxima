package txbuilder_seq

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const FieldCmdCode = byte(0)

var _cmdParsers = map[byte]cmdParser{}

func registerSequencerCommand(cmdCode byte, parser cmdParser) {
	util.Assertf(cmdCode != 0, "sequencer command code can't be 0")
	_, alreadyExists := _cmdParsers[cmdCode]
	util.Assertf(!alreadyExists, "sequencer command code already reistered: %d", cmdCode)
	_cmdParsers[cmdCode] = parser
}

func (txb *SeqTxBuilder) TxBuilderCommandFromOutput(o ledger.OutputWithID) (cmd TxBuilderCommand, isValid bool) {
	msg, ok := parseSeqCommandMessage(o.Output)
	if !ok {
		return nil, false
	}
	if msg == nil {
		return &SimpleTagAlongOutput{SeqCommandBase{o}}, true
	}
	if parser, found := _cmdParsers[msg.CmdCode]; found {
		return parser(txb, o, msg)
	}
	return nil, false
}

// parseSeqCommandMessage parses out secure message constraint
func parseSeqCommandMessage(o *ledger.Output) (msg *SeqCommandMessage, isValid bool) {
	if o.NumConstraints() > 4 {
		return nil, false
	}
	_msg, idx := o.MessageWithED25519Sender()
	if idx == 0xff {
		return nil, o.NumConstraints() == 2
	}
	m, err := base.SmallPersistentMapFromBytes(_msg.Msg)
	if err != nil {
		return nil, false
	}
	cmdCode := m.Get(FieldCmdCode)
	if cmdCode == nil || len(cmdCode) != 1 {
		return nil, false
	}
	return &SeqCommandMessage{
		SmallPersistentMap:       m,
		MessageWithED25519Sender: *_msg,
		CmdCode:                  cmdCode[0],
	}, true
}

func (cmd *SimpleTagAlongOutput) Apply(txb *SeqTxBuilder) error {
	_, err := txb.ConsumeTagAlongOutputUnlock(cmd.o.Output, cmd.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	if err != nil {
		return err
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(cmd.o.Output.TokenBalance())
	return nil
}
