package multistate

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/lunfardo314/unitrie/common"
)

var ErrNotFound = errors.New("object not found")

type SugaredStateReader struct {
	global.IndexedStateReader
}

func MakeSugared(s global.IndexedStateReader) SugaredStateReader {
	return SugaredStateReader{s}
}

func NewSugaredReadableState(store common.KVReader, root common.VCommitment, clearCacheAsSize ...int) (SugaredStateReader, error) {
	rdr, err := NewReadable(store, root, clearCacheAsSize...)
	if err != nil {
		return SugaredStateReader{}, err
	}
	return MakeSugared(rdr), nil
}

func MustNewSugaredReadableState(store common.KVReader, root common.VCommitment) SugaredStateReader {
	ret, err := NewSugaredReadableState(store, root)
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) Desugar() global.IndexedStateReader {
	return s.IndexedStateReader
}

func (s SugaredStateReader) GetOutputWithID(oid *core.OutputID) (*core.OutputWithID, error) {
	oData, found := s.IndexedStateReader.GetUTXO(oid)
	if !found {
		return nil, ErrNotFound
	}
	ret, err := core.OutputFromBytesReadOnly(oData)
	if err != nil {
		return nil, err
	}

	return &core.OutputWithID{
		ID:     *oid,
		Output: ret,
	}, nil
}

func (s SugaredStateReader) GetOutput(oid *core.OutputID) (*core.Output, error) {
	oData, found := s.IndexedStateReader.GetUTXO(oid)
	if !found {
		return nil, ErrNotFound
	}
	ret, err := core.OutputFromBytesReadOnly(oData)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) MustGetOutput(oid *core.OutputID) *core.OutputWithID {
	ret, err := s.GetOutputWithID(oid)
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetOutputsForAccount(addr core.AccountID) ([]*core.OutputWithID, error) {
	oDatas, err := s.GetUTXOsLockedInAccount(addr)
	if err != nil {
		return nil, err
	}
	return txutils.ParseAndSortOutputData(oDatas, nil)
}

func (s SugaredStateReader) GetStemOutput() *core.OutputWithID {
	oData, err := s.IndexedStateReader.GetUTXOsLockedInAccount(core.StemAccountID)
	util.AssertNoError(err)
	if len(oData) != 1 {
		fmt.Println()
	}
	util.Assertf(len(oData) == 1, "inconsistency: stem output must be unique in the state, found %d stem output records", len(oData))
	ret, err := oData[0].Parse()
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetChainOutput(chainID *core.ChainID) (*core.OutputWithID, error) {
	oData, err := s.IndexedStateReader.GetUTXOForChainID(chainID)
	if err != nil {
		return nil, err
	}
	ret, err := core.OutputFromBytesReadOnly(oData.OutputData)
	if err != nil {
		return nil, err
	}
	return &core.OutputWithID{
		ID:     oData.ID,
		Output: ret,
	}, nil
}

func (s SugaredStateReader) BalanceOf(addr core.AccountID) uint64 {
	outs, err := s.GetOutputsForAccount(addr)
	util.AssertNoError(err)
	ret := uint64(0)
	for _, o := range outs {
		ret += o.Output.Amount()
	}
	return ret
}

func (s SugaredStateReader) NumOutputs(addr core.AccountID) int {
	outs, err := s.GetOutputsForAccount(addr)
	util.AssertNoError(err)
	return len(outs)
}

func (s SugaredStateReader) BalanceOnChain(chainID *core.ChainID) uint64 {
	o, err := s.GetChainOutput(chainID)
	if err != nil {
		return 0
	}
	return o.Output.Amount()
}
