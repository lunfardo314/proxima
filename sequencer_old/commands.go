package sequencer_old

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
)

const (
	// CommandCodeWithdrawAmount is a command to the sequencer to send specified amount to the target lock
	CommandCodeWithdrawAmount = byte(0xff)

	MinimumAmountToRequestFromSequencer = 100_000
)

// parseSenderCommandDataRaw analyzes the input and parses out raw sequencer command data, if any
func parseSenderCommandDataRaw(myAddr core.AddressED25519, input *core.OutputWithID) []byte {
	senderAddr, senderConstraintIdx := input.Output.SenderED25519()
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
	if int(commandDataIndex) >= input.Output.NumConstraints() {
		// command data does not exist, ignore command
		return nil
	}
	// evaluating constraint without context to get the real command data
	// Usually, data cmd is concat(....)
	cmdDataConstrBytecode := input.Output.ConstraintAt(commandDataIndex)
	cmdData, err := easyfl.EvalFromBinary(nil, cmdDataConstrBytecode)
	if err != nil {
		// this means constraint cannot be evaluated without context of the transaction
		// It is valid because output is valid, however it cannot be used as command data
		return nil
	}
	util.Assertf(len(cmdData) > 0, "sequencer command data cannot be nil")
	return cmdData
}

// makeOutputFromCommandData parses command data and makes output from it.
// The output will be produced by the transaction which consumes inouts with command
// Sequencer command raw data is parsed the following way
// expected:
// - byte 0: command code
// - bytes [1:] is lazy array of parameters, interpreted depending on the command code
func makeOutputFromCommandData(cmdDataRaw []byte) (*core.Output, error) {
	util.Assertf(len(cmdDataRaw) > 0, "len(cmdDataRaw)>0")
	commandCode := cmdDataRaw[0]
	cmdParams, err := lazybytes.ParseArrayFromBytesReadOnly(cmdDataRaw[1:])
	if err != nil {
		return nil, err
	}

	switch commandCode {
	case CommandCodeWithdrawAmount:
		// expected 2 parameters:
		// - #0 target lock bytecode
		// - #1 amount 8 bytes
		if cmdParams.NumElements() != 2 || len(cmdParams.At(1)) != 8 {
			return nil, fmt.Errorf("wrong params")
		}
		targetLock, err := core.LockFromBytes(cmdParams.At(0))
		if err != nil {
			return nil, err
		}
		amount := binary.BigEndian.Uint64(cmdParams.At(1))
		if amount < MinimumAmountToRequestFromSequencer {
			return nil, fmt.Errorf("the requested amount %d is less than minimum (%d). Command igored",
				amount, MinimumAmountToRequestFromSequencer)
		}
		return core.NewOutput(func(o *core.Output) {
			o.WithAmount(amount).WithLock(targetLock)
		}), nil
	default:
		return nil, fmt.Errorf("command code %d not supported", commandCode)
	}
}
