package seqdata

import (
	"encoding/json"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type SequencerData struct {
	Name                    string `json:"name"`
	MinimumFee              uint64 `json:"minimum_fee"`
	InflationMarginPromille uint16 `json:"inflation_margin_promille"`
	ChainHeight             uint32 `json:"chain_height"`
	BranchHeight            uint32 `json:"branch_height"`
	Pace                    byte   `json:"pace"`
}

const NumParameters = 5

const (
	KeyName = byte(iota)
	KeyMinimumFee
	KeyInflationMarginPromille
	KeyChainHeight
	KeyBranchHeight
	KeyPace
)

func (sd *SequencerData) Bytes() []byte {
	m := base.NewSmallPersistentMap()
	m.Set(KeyName, []byte(sd.Name))
	m.Set(KeyMinimumFee, easyfl_util.TrimmedLeadingZeroUint64(sd.MinimumFee))
	m.Set(KeyInflationMarginPromille, easyfl_util.TrimmedLeadingZeroUint16(sd.InflationMarginPromille))
	m.Set(KeyChainHeight, easyfl_util.TrimmedLeadingZeroUint32(sd.ChainHeight))
	m.Set(KeyBranchHeight, easyfl_util.TrimmedLeadingZeroUint32(sd.BranchHeight))
	m.Set(KeyPace, []byte{sd.Pace})
	return m.Bytes()
}

func (sd *SequencerData) JSON() []byte {
	ret, err := json.MarshalIndent(sd, "", " ")
	util.AssertNoError(err)
	return ret
}

func SequencerDataFromBytes(data []byte) (*SequencerData, error) {
	m, err := base.SmallPersistentMapFromBytes(data)
	if err != nil {
		return nil, err
	}
	if m.Len() > NumParameters {
		return nil, fmt.Errorf("wrong number of parameters in sequencer data")
	}
	ret := &SequencerData{}
	ret.Name = string(m.Get(KeyName))
	ret.MinimumFee, err = easyfl_util.Uint64FromBytes(m.Get(KeyMinimumFee))
	if err != nil {
		return nil, err
	}
	ret.InflationMarginPromille, err = easyfl_util.Uint16FromBytes(m.Get(KeyInflationMarginPromille))
	if err != nil {
		return nil, err
	}
	ch, err := easyfl_util.Uint64FromBytes(m.Get(KeyChainHeight))
	if err != nil {
		return nil, err
	}
	ret.ChainHeight = uint32(ch)
	bh, err := easyfl_util.Uint64FromBytes(m.Get(KeyBranchHeight))
	if err != nil {
		return nil, err
	}
	ret.BranchHeight = uint32(bh)
	pace := m.Get(KeyPace)
	if len(pace) > 0 && len(pace) != 1 {
		return nil, fmt.Errorf("wrong 'pace' parameter")
	}
	ret.Pace = pace[0]
	return ret, nil
}

func SequencerDataFromJSON(data []byte) (*SequencerData, error) {
	ret := &SequencerData{}
	if err := json.Unmarshal(data, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}
