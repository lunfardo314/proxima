package txbuilder_seq

import (
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
		CheckPreconditions(txb *SequencerTxBuilder) (isAuth bool, consume bool, producesOutputs int) //
		Apply(txb *SequencerTxBuilder)
	}

	NoopCommand struct{}

	cmdParser func(cmdBase SequencerCommandBase) (SequencerCommand, bool)
)

const FieldCmdCode = byte(0)

var _cmdParsers = map[byte]cmdParser{}

func registerSequencerCommand(cmdCode byte, parser cmdParser) {
	util.Assertf(cmdCode != 0, "sequencer command code can't be 0")
	_, alreadyExists := _cmdParsers[cmdCode]
	util.Assertf(!alreadyExists, "sequencer command code already reistered: %d", cmdCode)
	_cmdParsers[cmdCode] = parser
}

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

	if parser, found := _cmdParsers[cmdBase.CmdCode]; found {
		if ret, ok := parser(cmdBase); ok {
			return ret
		}
	}
	return NoopCommand{}
}

func (cmd NoopCommand) CheckPreconditions(_ *SequencerTxBuilder) (bool, bool, int) {
	return false, false, 0
}

func (cmd NoopCommand) Apply(_ *SequencerTxBuilder) {
	return
}
