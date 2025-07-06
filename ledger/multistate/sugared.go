package multistate

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

var ErrNotFound = errors.New("object not found")

type SugaredStateReader struct {
	IndexedStateReader
}

func MakeSugared(s IndexedStateReader) SugaredStateReader {
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

func (s SugaredStateReader) GetOutputWithID(oid base.OutputID) (*ledger.OutputWithID, error) {
	oData, found := s.IndexedStateReader.GetUTXO(oid)
	if !found {
		return nil, ErrNotFound
	}
	ret, err := ledger.OutputFromBytes(oData)
	if err != nil {
		return nil, err
	}

	return &ledger.OutputWithID{
		ID:     oid,
		Output: ret,
	}, nil
}

func (s SugaredStateReader) GetOutputErr(oid base.OutputID) (*ledger.Output, error) {
	oData, found := s.IndexedStateReader.GetUTXO(oid)
	if !found {
		return nil, ErrNotFound
	}
	ret, err := ledger.OutputFromBytes(oData)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// GetOutput retrieves and parses output.
// Warning: do not use in iteration bodies because of mutex lock
func (s SugaredStateReader) GetOutput(oid base.OutputID) *ledger.Output {
	ret, err := s.GetOutputErr(oid)
	if err == nil {
		return ret
	}
	util.Assertf(errors.Is(err, ErrNotFound), "%w", err)
	return nil
}

func (s SugaredStateReader) MustGetOutputWithID(oid base.OutputID) *ledger.OutputWithID {
	ret, err := s.GetOutputWithID(oid)
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetOutputsForAccount(addr ledger.AccountID) ([]*ledger.OutputWithID, error) {
	oDatas, err := s.GetUTXOsInAccount(addr)
	if err != nil {
		return nil, err
	}
	return ledger.ParseAndSortOutputData(oDatas, nil)
}

func (s SugaredStateReader) IterateOutputsForAccount(addr ledger.Accountable, fun func(oid base.OutputID, o *ledger.Output) bool) (err error) {
	var o *ledger.Output
	var err1 error
	return s.IterateUTXOsInAccount(addr.AccountID(), func(oid base.OutputID, odata []byte) bool {
		o, err1 = ledger.OutputFromBytes(odata)
		if err1 != nil {
			return true
		}
		return fun(oid, o)
	})
}

func (s SugaredStateReader) GetStemOutput() *ledger.OutputWithID {
	oData, err := s.IndexedStateReader.GetUTXOsInAccount(ledger.StemAccountID)
	util.AssertNoError(err)
	var stateID base.TransactionID
	if len(oData) >= 0 {
		stateID = oData[0].ID.TransactionID()
	}
	util.Assertf(len(oData) == 1, "inconsistency: expected exactly 1 stem output record in the state, found %d (id[0] = %s, hex = %s)",
		len(oData), stateID.String, stateID.StringHex)
	ret, err := oData[0].Parse()
	util.AssertNoError(err)
	return ret
}

func (s SugaredStateReader) GetChainOutputWithID(chainID base.ChainID) (*ledger.OutputWithID, error) {
	oData, err := s.IndexedStateReader.GetUTXOForChainID(chainID)
	if err != nil {
		return nil, err
	}
	ret, err := ledger.OutputFromBytes(oData.Data)
	if err != nil {
		return nil, err
	}
	return &ledger.OutputWithID{
		ID:     oData.ID,
		Output: ret,
	}, nil
}

func (s SugaredStateReader) GetChainOutputWithChainID(chainID base.ChainID) (ledger.OutputWithChainID, error) {
	o, err := s.GetChainOutputWithID(chainID)
	if err != nil {
		return ledger.OutputWithChainID{}, err
	}
	ret, ok := ledger.AsOutputWithChainID(o.Output, o.ID)
	util.Assertf(ok, "GetChainOutputWithChainID: inconsistency")
	return ret, nil
}

func (s SugaredStateReader) GetDelegatedOutput(delegationID base.ChainID) (ret ledger.Delegate2Output, err error) {
	var o ledger.OutputWithChainID
	o, err = s.GetChainOutputWithChainID(delegationID)
	if err != nil {
		return
	}
	ret, err = ledger.AsDelegate2Output(&o)
	return
}

// GetChainTips return chain output and, if relevant, stem output for the chain id.
// The stem output is nil if the sequencer output is not in the branch
func (s SugaredStateReader) GetChainTips(chainID base.ChainID) (*ledger.OutputWithID, *ledger.OutputWithID, error) {
	oData, err := s.IndexedStateReader.GetUTXOForChainID(chainID)
	if err != nil {
		return nil, nil, err
	}
	outSeq, err := ledger.OutputFromBytes(oData.Data)
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
		ret += o.Output.TokenBalance()
	}
	return ret
}

func (s SugaredStateReader) NumOutputs(addr ledger.AccountID) int {
	outs, err := s.GetOutputsForAccount(addr)
	util.AssertNoError(err)
	return len(outs)
}

func (s SugaredStateReader) BalanceOnChain(chainID base.ChainID) uint64 {
	o, err := s.GetChainOutputWithID(chainID)
	if err != nil {
		return 0
	}
	return o.Output.TokenBalance()
}

func (s SugaredStateReader) GetOutputsDelegatedToAccount(addr ledger.Accountable) ([]*ledger.OutputWithChainID, error) {
	ret := make([]*ledger.OutputWithChainID, 0)
	err := s.IterateOutputsForAccount(addr, func(oid base.OutputID, o *ledger.Output) bool {
		lock := o.DelegationLock()
		if lock != nil && ledger.EqualAccountables(lock.TargetLock, addr) {
			cc, idx := o.ChainConstraint()
			chainID := cc.ID
			if cc.IsOrigin() {
				chainID = base.MakeOriginChainID(oid)
			}
			util.Assertf(idx != 0xff, "inconsistency: chain constraint expected")
			ret = append(ret, &ledger.OutputWithChainID{
				OutputWithID: ledger.OutputWithID{
					ID:     oid,
					Output: o,
				},
				ChainConstraintData: ledger.ChainConstraintData{
					ChainID:                    chainID,
					PredecessorConstraintIndex: cc.PredecessorConstraintIndex,
					OriginSlot:                 cc.OriginSlot,
					OriginAmount:               cc.OriginAmount,
					ChainConstraintIndex:       idx,
				},
			})
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) GetOutputsDelegatedToAccount2(addr ledger.Accountable) ([]*ledger.OutputWithChainID, error) {
	ret := make([]*ledger.OutputWithChainID, 0)
	err := s.IterateOutputsForAccount(addr, func(oid base.OutputID, o *ledger.Output) bool {
		lock := o.DelegationLock2()
		if lock != nil && ledger.EqualAccountables(lock.Target, addr) {
			cc, idx := o.ChainConstraint()
			chainID := cc.ID
			if cc.IsOrigin() {
				chainID = base.MakeOriginChainID(oid)
			}
			util.Assertf(idx != 0xff, "inconsistency: chain constraint expected")
			ret = append(ret, &ledger.OutputWithChainID{
				OutputWithID: ledger.OutputWithID{
					ID:     oid,
					Output: o,
				},
				ChainConstraintData: ledger.ChainConstraintData{
					ChainID:                    chainID,
					PredecessorConstraintIndex: cc.PredecessorConstraintIndex,
					OriginSlot:                 cc.OriginSlot,
					OriginAmount:               cc.OriginAmount,
					ChainConstraintIndex:       idx,
				},
			})
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) IterateDelegatedOutputs(delegationTarget ledger.Accountable, fun func(oid base.OutputID, o *ledger.Output, dLock *ledger.DelegationLock) bool) {
	var dLock *ledger.DelegationLock
	err := s.IterateOutputsForAccount(delegationTarget, func(oid base.OutputID, o *ledger.Output) bool {
		if dLock = o.DelegationLock(); dLock != nil {
			if ledger.EqualAccountables(delegationTarget, dLock.TargetLock) {
				return fun(oid, o, dLock)
			}
		}
		return true
	})
	util.AssertNoError(err)
}

// GetOutputsLockedInAddressED25519ForAmount returns outputs locked in simple address. Skip delegated and other
func (s SugaredStateReader) GetOutputsLockedInAddressED25519ForAmount(addr ledger.AddressED25519, targetAmount uint64) ([]*ledger.OutputWithID, uint64) {
	ret := make([]*ledger.OutputWithID, 0)
	retAmount := uint64(0)
	err := s.IterateOutputsForAccount(addr, func(oid base.OutputID, o *ledger.Output) bool {
		if ledger.EqualConstraints(addr, o.Lock()) {
			ret = append(ret, &ledger.OutputWithID{
				ID:     oid,
				Output: o,
			})
			retAmount += o.TokenBalance()
		}
		return retAmount < targetAmount
	})
	util.AssertNoError(err)
	return ret, retAmount
}

func (s SugaredStateReader) IterateChainsInAccount(addr ledger.Accountable, fun func(oid base.OutputID, o *ledger.Output, chainID base.ChainID) bool) error {
	return s.IterateOutputsForAccount(addr, func(oid base.OutputID, o *ledger.Output) bool {
		if cc, idx := o.ChainConstraint(); idx != 0xff {
			if cc.IsOrigin() {
				return fun(oid, o, base.MakeOriginChainID(oid))
			}
			return fun(oid, o, cc.ID)
		}
		return true
	})
}

func (s SugaredStateReader) GetAllChainsOld() (map[base.ChainID]ChainRecordInfo, error) {
	var err error

	ids := make(map[base.ChainID]base.OutputID)
	err = s.IterateChainTips(func(chainID base.ChainID, oid base.OutputID) bool {
		ids[chainID] = oid
		return true
	})
	if err != nil {
		return nil, err
	}

	ret := make(map[base.ChainID]ChainRecordInfo)
	for chainID, oid := range ids {
		o := s.GetOutput(oid)
		if o == nil {
			return nil, fmt.Errorf("inconsistency: cannot get chainID: %s, oid: %s", chainID.String(), oid.String())
		}
		ret[chainID] = ChainRecordInfo{
			Balance: o.TokenBalance(),
			Output: &ledger.OutputDataWithID{
				ID:   oid,
				Data: o.Bytes(),
			},
		}
	}
	return ret, nil
}

// IterateChainedOutputs iterates chained outputs and parses them
func (s SugaredStateReader) IterateChainedOutputs(fun func(out ledger.OutputWithChainID) bool) error {
	type _chainOutputIDPair struct {
		chainID base.ChainID
		oid     base.OutputID
	}
	// first collect all chain tips to avoid deadlock
	// TODO loading all chains into memory is suboptimal. Trick is only needed to avoid deadlock with GetOutput

	chainTips := make([]_chainOutputIDPair, 0)
	err := s.IterateChainTips(func(chainID base.ChainID, oid base.OutputID) bool {
		chainTips = append(chainTips, _chainOutputIDPair{
			chainID: chainID,
			oid:     oid,
		})
		return true
	})
	if err != nil {
		return err
	}
	var exit bool
	for _, tip := range chainTips {
		o := s.GetOutput(tip.oid) // locks the reader each time
		if o == nil {
			return fmt.Errorf("IterateChainedOutputs: inconsistency: cannot get chain output: %s, oid: %s",
				tip.chainID.String(), tip.oid.String())
		}
		cc, idx := o.ChainConstraint()
		util.Assertf(idx != 0xff, "inconsistency: chain constraint expected")
		exit = !fun(ledger.OutputWithChainID{
			OutputWithID: ledger.OutputWithID{
				ID:     tip.oid,
				Output: o,
			},
			ChainConstraintData: ledger.ChainConstraintData{
				ChainID:                    tip.chainID,
				PredecessorConstraintIndex: cc.PredecessorConstraintIndex,
				OriginSlot:                 cc.OriginSlot,
				OriginAmount:               cc.OriginAmount,
				ChainConstraintIndex:       idx,
			},
		})
		if exit {
			return nil
		}
	}
	return nil
}

type DelegationsOnSequencer struct {
	SequencerOutput ledger.OutputWithID
	Delegations     map[base.ChainID]ledger.OutputWithID
}

func (s SugaredStateReader) GetDelegationsBySequencer() (map[base.ChainID]DelegationsOnSequencer, error) {
	allOuts := make([]ledger.OutputWithChainID, 0)
	err := s.IterateChainedOutputs(func(out ledger.OutputWithChainID) bool {
		allOuts = append(allOuts, out)
		return true
	})
	if err != nil {
		return nil, err
	}
	ret := make(map[base.ChainID]DelegationsOnSequencer)
	nonSeq := make([]*ledger.OutputWithChainID, 0)
	// collect all sequencers
	for i := range allOuts {
		if allOuts[i].OutputWithID.Output.IsSequencerOutput() {
			ret[allOuts[i].ChainID] = DelegationsOnSequencer{
				SequencerOutput: allOuts[i].OutputWithID,
			}
		} else {
			nonSeq = append(nonSeq, &allOuts[i])
		}
	}

	for _, delegation := range nonSeq {
		dl := delegation.OutputWithID.Output.DelegationLock()
		if dl == nil {
			// chain but not delegation
			continue
		}
		if dl.TargetLock.Name() == ledger.ChainLockName {
			cl := dl.TargetLock.(ledger.ChainLock)
			seq, ok := ret[cl.ChainID()]
			if !ok {
				// delegated to nonexistent sequencer
				continue
			}
			if len(seq.Delegations) == 0 {
				seq.Delegations = make(map[base.ChainID]ledger.OutputWithID)
			}
			seq.Delegations[delegation.ChainID] = delegation.OutputWithID
			ret[cl.ChainID()] = seq
		}
	}
	return ret, nil
}
