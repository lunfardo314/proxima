package factory

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/unitrie/common"
)

const (
	// CommandCodeWithdrawAmount is a command to the sequencer to withdraw specified amount to the target lock
	CommandCodeWithdrawAmount = byte(0xff)

	MinimumAmountToRequestFromSequencer = 100_000
)

type CommandParser struct {
	ownerAddress ledger.AddressED25519
}

func NewCommandParser(ownerAddress ledger.AddressED25519) CommandParser {
	return CommandParser{ownerAddress}
}

func (p CommandParser) ParseSequencerCommandToOutput(input *ledger.OutputWithID) ([]*ledger.Output, error) {
	cmdCode, cmdParamArr := parseSenderCommand(p.ownerAddress, input)
	if cmdParamArr == nil {
		// command has not been found in the output. Ignore
		return nil, nil
	}

	outputs, err := makeOutputFromCommandData(cmdCode, cmdParamArr)
	if err != nil {
		return nil, fmt.Errorf("ParseSequencerCommandToOutput: error while parsing sequencer command input %s: %w", input.ID.StringShort(), err)
	}
	return outputs, nil
}

// parseSenderCommand analyzes the input and parses out raw sequencer command data, if any
// Command is recognized by:
//   - finding SenderED25519 constraint inn the output. If not found, output is not a command output
//   - taking constraint next after the SenderED25519. If it does not exist, parse fails
//   - evaluating the constraint (as EasyFL bytecode). It will not fail, because the whole output is valid. The value of
//     evaluation is non-empty byte slice D
//   - the element D[0] is interpreted as command code
//   - the remaining bytes D[1:] are parsed as lazy array of command parameters.
//   - if operations are successful, cmd code and array of parameters represents syntactically correct sequencer command
func parseSenderCommand(myAddr ledger.AddressED25519, input *ledger.OutputWithID) (byte, *lazybytes.Array) {
	senderAddr, senderConstraintIdx := input.Output.SenderED25519()
	if senderConstraintIdx == 0xff {
		return 0, nil
	}
	if !ledger.EqualConstraints(myAddr, senderAddr) {
		// if sender address is not equal to the controller address of the signature,
		// the command is ignored
		return 0, nil
	}
	// command data is expected in the constraint at the index next after the sender. The data itself is
	// evaluation of the constraint without context. It can't be nil because each input is a valid output
	commandDataIndex := senderConstraintIdx + 1
	if int(commandDataIndex) >= input.Output.NumConstraints() {
		// command data does not exist, ignore command
		return 0, nil
	}
	// evaluating constraint without context to get the real command data
	// Usually, data cmd is concat(....)
	cmdDataConstrBytecode := input.Output.ConstraintAt(commandDataIndex)
	cmdDataRaw, err := ledger.L().EvalFromBinary(nil, cmdDataConstrBytecode)
	if err != nil {
		// this means constraint cannot be evaluated without context of the transaction
		// It is valid because output is valid, however it cannot be used as command data
		return 0, nil
	}
	util.Assertf(len(cmdDataRaw) > 0, "sequencer command data cannot be nil")
	cmdParamsArr, err := lazybytes.ParseArrayFromBytesReadOnly(cmdDataRaw[1:])
	if err != nil {
		return 0, nil
	}
	util.AssertNotNil(cmdParamsArr)
	return cmdDataRaw[0], cmdParamsArr
}

// makeOutputFromCommandData parses command data and makes output from it.
// The output will be produced by the transaction which consumes inouts with command
// Sequencer command raw data is parsed the following way
// expected:
// - byte 0: command code
// - bytes [1:] is lazy array of parameters, interpreted depending on the command code
func makeOutputFromCommandData(cmdCode byte, cmdParams *lazybytes.Array) ([]*ledger.Output, error) {
	switch cmdCode {
	case CommandCodeWithdrawAmount:
		// expected 2 parameters:
		// - #0 target lock bytecode
		// - #1 amount 8 bytes
		if cmdParams.NumElements() != 2 || len(cmdParams.At(1)) != 8 {
			return nil, fmt.Errorf("wrong 'withdraw' command params")
		}
		targetLock, err := ledger.LockFromBytes(cmdParams.At(0))
		if err != nil {
			return nil, fmt.Errorf("wrong target lock in 'withdraw' command: %w", err)
		}
		amount := binary.BigEndian.Uint64(cmdParams.At(1))
		if amount < MinimumAmountToRequestFromSequencer {
			return nil, fmt.Errorf("the requested amount %d is less than minimum (%d) in the 'withdraw' command",
				amount, MinimumAmountToRequestFromSequencer)
		}
		o := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(amount).WithLock(targetLock)
		})
		return []*ledger.Output{o}, nil
	default:
		return nil, fmt.Errorf("command code %d is not supported", cmdCode)
	}
}

type MakeSequencerWithdrawCmdOutputParams struct {
	SeqID          ledger.ChainID
	ControllerAddr ledger.AddressED25519
	TargetLock     ledger.Lock
	TagAlongFee    uint64
	Amount         uint64
}

func MakeSequencerWithdrawCmdOutput(par MakeSequencerWithdrawCmdOutputParams) (*ledger.Output, error) {
	if par.Amount < MinimumAmountToRequestFromSequencer {
		return nil, fmt.Errorf("the reqested amount (%s) is less than required minimum (%s) if the sequencer command",
			util.GoTh(par.Amount), util.GoTh(MinimumAmountToRequestFromSequencer))
	}
	ret := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(par.TagAlongFee).WithLock(ledger.ChainLockFromChainID(par.SeqID))
		idx, err := o.PushConstraint(ledger.NewSenderED25519(par.ControllerAddr).Bytes())
		util.AssertNoError(err)
		util.Assertf(idx == 2, "idx==2")
		var amountBin [8]byte
		binary.BigEndian.PutUint64(amountBin[:], par.Amount)
		cmdParamsArr := lazybytes.MakeArrayFromDataReadOnly(par.TargetLock.Bytes(), amountBin[:])

		rawData := common.ConcatBytes([]byte{CommandCodeWithdrawAmount}, cmdParamsArr.Bytes())
		src := fmt.Sprintf("concat(0x%s)", hex.EncodeToString(rawData))

		cmdConstraint, _, err := ledger.L().ExpressionSourceToBinary(src)
		util.AssertNoError(err)
		idx, err = o.PushConstraint(cmdConstraint)
		util.AssertNoError(err)
		util.Assertf(idx == 3, "idx==3")
	})
	// reverse checking
	cmdParserDummy := NewCommandParser(par.ControllerAddr)
	oWithIDDummy := &ledger.OutputWithID{Output: ret}
	out, err := cmdParserDummy.ParseSequencerCommandToOutput(oWithIDDummy)
	util.AssertNoError(err)
	util.Assertf(len(out) == 1, "len(out)==1")
	util.Assertf(out[0].Amount() == par.Amount, "out[0].Amount()==par.Amount")
	util.Assertf(ledger.EqualConstraints(par.TargetLock, out[0].Lock()), "ledger.EqualConstraints(par.TargetLock, out[0].Lock())")
	return ret, nil
}

const minimumWithdrawAmountFromSequencer = 1_000_000

func MakeSequencerWithdrawCommand(amount uint64, targetLock ledger.Lock) (ledger.GeneralScript, error) {
	if amount < minimumWithdrawAmountFromSequencer {
		return nil, fmt.Errorf("withdraw from sequencer amount must be ar least %s", util.GoTh(minimumWithdrawAmountFromSequencer))
	}
	var amountBin [8]byte
	binary.BigEndian.PutUint64(amountBin[:], amount)
	cmdParArr := lazybytes.MakeArrayFromDataReadOnly(targetLock.Bytes(), amountBin[:])
	cmdData := common.Concat(CommandCodeWithdrawAmount, cmdParArr)
	constrSource := fmt.Sprintf("concat(0x%s)", hex.EncodeToString(cmdData))
	cmdConstr, err := ledger.NewGeneralScriptFromSource(constrSource)
	glb.AssertNoError(err)

	return cmdConstr, nil
}
