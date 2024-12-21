package multistate

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
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

func MustNewSugaredReadableState(store common.KVReader, root common.VCommitment, clearCacheAsSize ...int) SugaredStateReader {
	ret, err := NewSugaredReadableState(store, root, clearCacheAsSize...)
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetOutputWithID(oid *ledger.OutputID) (*ledger.OutputWithID, error) {
	oData, found := s.IndexedStateReader.GetUTXO(oid)
	if !found {
		return nil, ErrNotFound
	}
	ret, err := ledger.OutputFromBytesReadOnly(oData)
	if err != nil {
		return nil, err
	}

	return &ledger.OutputWithID{
		ID:     *oid,
		Output: ret,
	}, nil
}

func (s SugaredStateReader) GetOutputErr(oid *ledger.OutputID) (*ledger.Output, error) {
	oData, found := s.IndexedStateReader.GetUTXO(oid)
	if !found {
		return nil, ErrNotFound
	}
	ret, err := ledger.OutputFromBytesReadOnly(oData)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) GetOutput(oid *ledger.OutputID) *ledger.Output {
	ret, err := s.GetOutputErr(oid)
	if err == nil {
		return ret
	}
	util.Assertf(errors.Is(err, ErrNotFound), "%w", err)
	return nil
}

func (s SugaredStateReader) MustGetOutputWithID(oid *ledger.OutputID) *ledger.OutputWithID {
	ret, err := s.GetOutputWithID(oid)
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetOutputsForAccount(addr ledger.AccountID) ([]*ledger.OutputWithID, error) {
	oDatas, err := s.GetUTXOsLockedInAccount(addr)
	if err != nil {
		return nil, err
	}
	return txutils.ParseAndSortOutputData(oDatas, nil)
}

func (s SugaredStateReader) IterateOutputsForAccount(addr ledger.Accountable, fun func(oid ledger.OutputID, o *ledger.Output) bool) (err error) {
	var o *ledger.Output
	var err1 error
	return s.IterateUTXOsLockedInAccount(addr.AccountID(), func(oid ledger.OutputID, odata []byte) bool {
		o, err1 = ledger.OutputFromBytesReadOnly(odata)
		if err1 != nil {
			return true
		}
		return fun(oid, o)
	})
}

func (s SugaredStateReader) GetStemOutput() *ledger.OutputWithID {
	oData, err := s.IndexedStateReader.GetUTXOsLockedInAccount(ledger.StemAccountID)
	util.AssertNoError(err)
	if len(oData) != 1 {
		fmt.Println()
	}
	util.Assertf(len(oData) == 1, "inconsistency: expected exactly 1 stem output record in the state, found %d", len(oData))
	ret, err := oData[0].Parse()
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetChainOutput(chainID *ledger.ChainID) (*ledger.OutputWithID, error) {
	oData, err := s.IndexedStateReader.GetUTXOForChainID(chainID)
	if err != nil {
		return nil, err
	}
	ret, err := ledger.OutputFromBytesReadOnly(oData.OutputData)
	if err != nil {
		return nil, err
	}
	return &ledger.OutputWithID{
		ID:     oData.ID,
		Output: ret,
	}, nil
}

// GetChainTips return chain output and, if relevant, stem output for the chain ID.
// The stem output is nil if sequencer output is not in the branch
func (s SugaredStateReader) GetChainTips(chainID *ledger.ChainID) (*ledger.OutputWithID, *ledger.OutputWithID, error) {
	oData, err := s.IndexedStateReader.GetUTXOForChainID(chainID)
	if err != nil {
		return nil, nil, err
	}
	outSeq, err := ledger.OutputFromBytesReadOnly(oData.OutputData)
	if err != nil {
		return nil, nil, err
	}
	retSeq := &ledger.OutputWithID{
		ID:     oData.ID,
		Output: outSeq,
	}
	if !retSeq.ID.IsBranchTransaction() {
		// no stem on branch
		return retSeq, nil, nil
	}
	// sequencer output is on the branch
	stemOut := s.GetStemOutput()
	if retSeq.ID.TransactionID() != stemOut.ID.TransactionID() {
		// if sequencer output is on the branch, stem must be on the same transaction
		// Here stem and sequencer transactions are from different branches (yet on the same chain of branches)
		return retSeq, nil, nil
	}
	// stem and sequencer outputs are from the same transaction
	return retSeq, stemOut, nil
}

func (s SugaredStateReader) BalanceOf(addr ledger.AccountID) uint64 {
	outs, err := s.GetOutputsForAccount(addr)
	util.AssertNoError(err)
	ret := uint64(0)
	for _, o := range outs {
		ret += o.Output.Amount()
	}
	return ret
}

func (s SugaredStateReader) NumOutputs(addr ledger.AccountID) int {
	outs, err := s.GetOutputsForAccount(addr)
	util.AssertNoError(err)
	return len(outs)
}

func (s SugaredStateReader) BalanceOnChain(chainID *ledger.ChainID) uint64 {
	o, err := s.GetChainOutput(chainID)
	if err != nil {
		return 0
	}
	return o.Output.Amount()
}

func (s SugaredStateReader) GetOutputsDelegatedToAccount(addr ledger.Accountable) ([]*ledger.OutputWithID, error) {
	ret := make([]*ledger.OutputWithID, 0)
	err := s.IterateOutputsForAccount(addr, func(oid ledger.OutputID, o *ledger.Output) bool {
		lock := o.DelegationLock()
		if lock != nil && ledger.EqualAccountables(lock.TargetLock, addr) {
			ret = append(ret, &ledger.OutputWithID{
				ID:     oid,
				Output: o,
			})
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}
