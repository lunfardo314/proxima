package core

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
)

const sequencerRequestSource = `
// $0 is request code
// $1 is hax form of the serialized parameters as lazyarray 
func sequencerRequest: concat($0,$1)
`

const (
	SequencerRequestName     = "sequencerRequest"
	sequencerRequestTemplate = SequencerRequestName + "(%d, 0x%s)"
)

type SequencerRequest struct {
	OpCode byte
	Params *lazybytes.Array
}

func NewSequencerRequest(opCode byte, params ...[]byte) *SequencerRequest {
	return &SequencerRequest{
		OpCode: opCode,
		Params: lazybytes.MakeArrayFromDataReadOnly(params...),
	}
}

func (r *SequencerRequest) Name() string {
	return timelockName
}

func (r *SequencerRequest) Bytes() []byte {
	return mustBinFromSource(r.source())
}

func (r *SequencerRequest) String() string {
	parStr := make([]string, r.Params.NumElements())
	r.Params.ForEach(func(i int, data []byte) bool {
		parStr[i] = easyfl.Fmt(data)
		return true
	})
	return fmt.Sprintf("%s(%d,%s)", SequencerRequestName, r.OpCode, strings.Join(parStr, ","))
}

func (r *SequencerRequest) source() string {
	return fmt.Sprintf(sequencerRequestTemplate, r.OpCode, hex.EncodeToString(r.Params.Bytes()))
}

func SequencerRequestFromBytes(data []byte) (*SequencerRequest, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != SequencerRequestName {
		return nil, fmt.Errorf("not a sequencerRequest constraint")
	}
	opCodeBin := easyfl.StripDataPrefix(args[0])
	if len(opCodeBin) != 1 {
		return nil, fmt.Errorf("wrong opcode parameter")
	}
	opCode := opCodeBin[0]
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(args[1])
	if err != nil {
		return nil, err
	}
	return &SequencerRequest{
		OpCode: opCode,
		Params: arr,
	}, nil
}

func initSequencerRequestConstraint() {
	easyfl.MustExtendMany(sequencerRequestSource)

	example := NewSequencerRequest(1)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 2)
	util.Assertf(sym == SequencerRequestName, "sym == SequencerRequestName")
	util.AssertNoError(err)

	opCodeBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(opCodeBin) == 1, "len(opCodeBin)==1")
	util.Assertf(opCodeBin[0] == 1, "opCodeBin[0]==1")
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(easyfl.StripDataPrefix(args[1]))
	util.AssertNoError(err)
	util.Assertf(arr.NumElements() == 0, "arr.NumElements() == 0")
	registerConstraint(SequencerRequestName, prefix, func(data []byte) (Constraint, error) {
		return SequencerRequestFromBytes(data)
	})
}
