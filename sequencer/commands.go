package sequencer

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
)

const (
	CommandCodeSendAmount = byte(0xff)
)

func parseSenderCommandData(myAddr core.AddressED25519, o *core.OutputWithID) []byte {
	senderAddr, senderConstraintIdx := o.Output.SenderED25519()
	if senderConstraintIdx == 0xff {
		return nil
	}
	if !core.EqualConstraints(myAddr, senderAddr) {
		// if sender address is not equal to the controller address of the signature,
		// the command is ignored
		return nil
	}
	// command data is expected in the constraint at the index next after the sender. The data itself is
	// evaluation of the constraint without context. It can't be nil because each input is a valid output
	commandDataIndex := senderConstraintIdx + 1
	if int(commandDataIndex) >= o.Output.NumConstraints() {
		// command data does not exist, ignore command
		return nil
	}
	// evaluating constraint without context to get the real command data
	// Usually, data cmd is concat(....)
	cmdDataConstrBytecode := o.Output.ConstraintAt(commandDataIndex)
	cmdData, err := easyfl.EvalFromBinary(nil, cmdDataConstrBytecode)
	if err != nil {
		// this means constraint cannot be evaluated without context of the transaction
		// It is valid because output is valid, however it cannot be used as command data
		return nil
	}
	util.Assertf(len(cmdData) > 0, "sequencer command data cannot be nil")
	return cmdData
}

// expected:
// 0 byte: command code
// [1:] bytes is lazy array of parameters
func makeOutputFromCommandData(cmdData []byte) (*core.Output, error) {
	util.Assertf(len(cmdData) > 0, "len(cmdData)>0")
	commandCode := cmdData[0]
	cmdParams, err := lazyslice.ParseArrayFromBytesReadOnly(cmdData[1:])
	if err != nil {
		return nil, err
	}

	switch commandCode {
	case CommandCodeSendAmount:
		// expected 2 parameters:
		// - 0 target lock bytecode
		// - 1 amount 8 bytes
		if cmdParams.NumElements() != 2 || len(cmdParams.At(1)) != 8 {
			return nil, fmt.Errorf("wrong params")
		}
		targetLock, err := core.LockFromBytes(cmdParams.At(0))
		if err != nil {
			return nil, err
		}
		amount := binary.BigEndian.Uint64(cmdParams.At(1))
		return core.NewOutput(func(o *core.Output) {
			o.WithAmount(amount).WithLock(targetLock)
		}), nil
	default:
		return nil, fmt.Errorf("command code %d not supported", commandCode)
	}
}
