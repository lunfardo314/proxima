package core

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// Immutable constraint forces the specified DataBlock to be repeated on the successor of the specified chain

type StateIndex struct {
	ChainBlockIndex byte
	StateIndex      uint32
}

const (
	StateIndexName     = "stateIndex"
	stateIndexTemplate = StateIndexName + "(u32/%d, %d)"
)

func NewStateIndex(chainBlockIndex byte, stateIndex uint32) *StateIndex {
	return &StateIndex{
		ChainBlockIndex: chainBlockIndex,
		StateIndex:      stateIndex,
	}
}

func StateIndexFromBytes(data []byte) (*StateIndex, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != StateIndexName {
		return nil, fmt.Errorf("not a StateIndex")
	}
	stateIdx := easyfl.StripDataPrefix(args[0])
	if len(stateIdx) != 4 {
		return nil, fmt.Errorf("can't parse StateIndex")
	}

	chainIdx := easyfl.StripDataPrefix(args[1])
	if len(chainIdx) != 1 {
		return nil, fmt.Errorf("can't parse StateIndex")
	}

	idx := binary.BigEndian.Uint32(stateIdx)

	return NewStateIndex(chainIdx[0], idx), nil
}

func (s *StateIndex) source() string {
	return fmt.Sprintf(stateIndexTemplate, s.StateIndex, s.ChainBlockIndex)
}

func (s *StateIndex) Bytes() []byte {
	return mustBinFromSource(s.source())
}

func (_ *StateIndex) Name() string {
	return StateIndexName
}

func (s *StateIndex) String() string {
	return s.source()
}

func initStateIndexConstraint() {
	easyfl.MustExtendMany(StateIndexSource)

	example := NewStateIndex(5, 314)
	stateIndexBack, err := StateIndexFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(stateIndexBack.StateIndex == 314, "inconsistency "+StateIndexName)
	util.Assertf(stateIndexBack.ChainBlockIndex == 5, "inconsistency "+StateIndexName)

	prefix, err := easyfl.ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)

	registerConstraint(StateIndexName, prefix, func(data []byte) (Constraint, error) {
		return StateIndexFromBytes(data)
	})
}

// TODO

const StateIndexSource = `
func stateIndex : concat($0, $1, !!!implement_me_StateIndexConstraint)
`
