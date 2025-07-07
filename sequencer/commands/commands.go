package commands

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

const (
	// CommandCodeWithdrawAmount is a command to the sequencer to withdraw specified amount to the target lock
	CommandCodeWithdrawAmount = byte(0xff)

	MinimumAmountToRequestFromSequencer = 1_000_000
)

type CommandParser struct {
	ownerAddress ledger.AddressED25519
}

// sender message is treated the following way:
// byte 0 is command code
// bytes [1:] is lazy array of parameters

func NewWithdrawCommandData(amount uint64, targetLock ledger.Lock) ([]byte, error) {
	if amount < MinimumAmountToRequestFromSequencer {
		return nil, fmt.Errorf("withdraw amount must be at least %s, got: %s", util.Th(MinimumAmountToRequestFromSequencer), util.Th(amount))
	}
	arr := tuples.MakeTupleFromDataElements(easyfl_util.TrimmedLeadingZeroUint64(amount), targetLock.Bytes())
	return common.Concat(CommandCodeWithdrawAmount, arr.Bytes()), nil
}

func parseWithdrawCommandData(data []byte) (uint64, ledger.Lock, bool) {
	if len(data) == 0 || data[0] != CommandCodeWithdrawAmount {
		return 0, nil, false
	}
	arr, err := tuples.TupleFromBytes(data[1:])
	if err != nil {
		return 0, nil, false
	}
	if arr.NumElements() != 2 {
		return 0, nil, false
	}
	amount, err := easyfl_util.Uint64FromBytes(arr.MustAt(0))
	if err != nil {
		return 0, nil, false
	}
	if amount < MinimumAmountToRequestFromSequencer {
		// silently ignore
		return 0, nil, false
	}
	targetLock, err := ledger.LockFromBytes(arr.MustAt(1))
	if err != nil {
		return 0, nil, false
	}
	return amount, targetLock, true
}

func NewCommandParser(ownerAddress ledger.AddressED25519) CommandParser {
	return CommandParser{ownerAddress}
}

func (p CommandParser) ParseSequencerCommandToOutputs(input *ledger.OutputWithID) ([]*ledger.Output, error) {
	msg, idx := input.Output.MessageWithED25519Sender()
	if idx == 0xff || !bytes.Equal(p.ownerAddress, msg.SenderHash[:]) {
		// security critical: parser will not produce any outputs if sender is on equal to the owner
		return nil, nil
	}
	amount, targetLock, isWithdrawCmd := parseWithdrawCommandData(msg.Msg)
	if !isWithdrawCmd {
		// silently ignore if parsing error
		return nil, nil
	}
	// make withdrawal output
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amount).WithLock(targetLock)
	})
	return []*ledger.Output{o}, nil
}

type MakeSequencerWithdrawCmdOutputParams struct {
	SeqID          base.ChainID
	ControllerAddr ledger.AddressED25519
	TargetLock     ledger.Lock
	TagAlongFee    uint64
	Amount         uint64
}

func MakeSequencerWithdrawCmdOutput(par MakeSequencerWithdrawCmdOutputParams) (*ledger.Output, error) {
	cmdData, err := NewWithdrawCommandData(par.Amount, par.TargetLock)
	if err != nil {
		return nil, err
	}
	msg := ledger.NewMessageWithED25519SenderFromAddress(par.ControllerAddr, cmdData)

	ret := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(par.TagAlongFee)
		o.WithLock(ledger.ChainLockFromChainID(par.SeqID))
		o.MustPushConstraint(msg.Bytes())
	})
	// reverse checking
	cmdParserDummy := NewCommandParser(par.ControllerAddr)
	oWithIDDummy := &ledger.OutputWithID{Output: ret}
	out, err := cmdParserDummy.ParseSequencerCommandToOutputs(oWithIDDummy)
	util.AssertNoError(err)
	util.Assertf(len(out) == 1, "len(out)==1")
	util.Assertf(out[0].TokenBalance() == par.Amount, "out[0].TokenBalance()==par.TokenBalance")
	util.Assertf(ledger.EqualConstraints(par.TargetLock, out[0].Lock()), "ledger.EqualConstraints(par.TargetLock, out[0].Lock())")
	return ret, nil
}
