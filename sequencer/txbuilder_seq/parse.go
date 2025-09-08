package txbuilder_seq

import (
	"fmt"

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

func (txb *SeqTxBuilder) TxBuilderCommandFromOutput(o ledger.OutputWithID) (cmd TxBuilderCommand, isValid bool, err error) {
	msg, err := preParseSeqRequest(o.Output)
	if err != nil {
		err = fmt.Errorf("TxBuilderCommandFromOutput: %v", err)
		return
	}
	if msg == nil {
		cmd = &SimpleTagAlongOutput{SeqCommandBase{o}}
		isValid = true
		return
	}
	if parser, found := _cmdParsers[msg.CmdCode]; found {
		return parser(txb, o, msg)
	}
	err = fmt.Errorf("TxBuilderCommandFromOutput: unknown command: %d", msg.CmdCode)
	return
}

// preParseSeqRequest parses out secure message
//   - expects no more than 4 constraints: 2 mandatory + msgED25519 + optional 'result guarantor' constraint.
//     Validation of the latter is request-specific
//   - if secure message constraint not found, expects exactly 2 constraints (ordinary tag-along)
func preParseSeqRequest(o *ledger.Output) (msg *SeqRequestMessage, err error) {
	if o.NumConstraints() > 4 {
		err = fmt.Errorf("can't contain more than 4 constraints, got %d", o.NumConstraints())
		return
	}
	_msg, idx := o.MessageWithED25519Sender()
	if idx == 0xff {
		if o.NumConstraints() != 2 {
			err = fmt.Errorf("tag-along output without command message must contain exactly 2 constraints")
		}
		return
	}
	m, err := base.SmallPersistentMapFromBytes(_msg.Msg)
	if err != nil {
		return
	}
	cmdCode := m.Get(FieldCmdCode)
	if cmdCode == nil || len(cmdCode) != 1 {
		err = fmt.Errorf("wrong command code field")
		return
	}
	msg = &SeqRequestMessage{
		SmallPersistentMap:       m,
		MessageWithED25519Sender: *_msg,
		CmdCode:                  cmdCode[0],
	}
	return msg, nil
}

func (cmd *SimpleTagAlongOutput) Apply(txb *SeqTxBuilder) (bool, error) {
	_, err := txb.ConsumeTagAlongOutputUnlock(cmd.o.Output, cmd.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	if err != nil {
		return true, err
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(cmd.o.Output.TokenBalance())
	return true, nil
}
