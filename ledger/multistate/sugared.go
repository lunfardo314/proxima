package multistate

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

var ErrNotFound = errors.New("object not found")

const TraceTag = "sugaredStateReader"

type SugaredStateReader struct {
	IndexedStateReader
	global.Logging
}

func MakeSugared(s IndexedStateReader, logger ...global.Logging) SugaredStateReader {
	ret := SugaredStateReader{IndexedStateReader: s}
	if len(logger) > 0 {
		ret.Logging = logger[0]
	}
	return ret
}

func (s SugaredStateReader) Trace(format string, args ...interface{}) {
	if s.Logging != nil {
		s.Tracef(TraceTag, format, args...)
	}
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

// ScanInactive scans the UTXO set to find outputs that weren't moved since specified slot
func (s SugaredStateReader) ScanInactive(slotNow, inactiveSinceSlot uint32, maxReturn ...int) ([]ledger.OutputWithID, error) {
	s.Trace(">>>>>> ScanInactive IN now: %d, since: %d", slotNow, inactiveSinceSlot)

	if slotNow <= inactiveSinceSlot {
		return nil, nil
	}
	s.Trace(">>>>>> ScanInactive 1")
	ret := make([]ledger.OutputWithID, 0)
	err := s.IterateUTXOs(func(o ledger.OutputWithID) bool {
		if o.ID.Slot() > inactiveSinceSlot {
			return true
		}
		if dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID); ok && dOut.IsInFrozenSlot(slotNow) {
			return true
		}
		ret = append(ret, o)
		if len(maxReturn) > 0 && len(ret) >= maxReturn[0] {
			return false
		}
		return true
	})
	return ret, err
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

func (s SugaredStateReader) GetDelegatedOutput(delegationID base.ChainID) (ret ledger.DelegationOutput, err error) {
	var o ledger.OutputWithChainID
	o, err = s.GetChainOutputWithChainID(delegationID)
	if err != nil {
		return
	}
	var ok bool
	if ret, ok = ledger.DelegationOutputFromOutputWithChainID(&o); !ok {
		err = fmt.Errorf("GetDelegatedOutput: not a DelegationOutput")
	}
	return
}

func (s SugaredStateReader) OutputIsConsumed(oid base.OutputID) bool {
	return s.KnowsCommittedTransaction(oid.TransactionID()) && !s.HasUTXO(oid)
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

func (s SugaredStateReader) GetOutputsDelegatedToAccount2(addr ledger.Accountable) ([]*ledger.OutputWithChainID, error) {
	ret := make([]*ledger.OutputWithChainID, 0)
	err := s.IterateOutputsForAccount(addr, func(oid base.OutputID, o *ledger.Output) bool {
		lock := o.DelegationLock()
		if lock != nil && ledger.EqualAccountables(lock.Target, addr) {
			cc, idx := o.ChainConstraint()
			chainID := cc.ChainID
			if cc.IsOrigin() {
				chainID = base.MakeOriginChainID(oid)
			}
			util.Assertf(idx != 0xff, "inconsistency: chain constraint expected")
			out := &ledger.OutputWithChainID{
				OutputWithID: ledger.OutputWithID{
					ID:     oid,
					Output: o,
				},
				ChainConstraintData: ledger.ChainConstraintData{
					ChainConstraint:      *cc,
					ChainConstraintIndex: idx,
				},
			}
			out.ChainID = chainID
			ret = append(ret, out)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) IterateDelegatedOutputs(delegationTarget base.ChainID, fun func(o *ledger.DelegationOutput) bool) {
	target := ledger.ChainLockFromChainID(delegationTarget)
	err := s.IterateOutputsForAccount(target, func(oid base.OutputID, o *ledger.Output) bool {
		out, ok := ledger.AsDelegationOutput(o, oid)
		if ok && ledger.EqualAccountables(target, out.Target) {
			return fun(&out)
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
			return fun(oid, o, cc.ChainID)
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
		out := ledger.OutputWithChainID{
			OutputWithID: ledger.OutputWithID{
				ID:     tip.oid,
				Output: o,
			},
			ChainConstraintData: ledger.ChainConstraintData{
				ChainConstraint:      *cc,
				ChainConstraintIndex: idx,
			},
		}
		out.ChainID = tip.chainID
		exit = !fun(out)
		if exit {
			return nil
		}
	}
	return nil
}

type DelegationsOnSequencer struct {
	SequencerOutput *ledger.OutputWithID // nil means sequencer output wasn't found
	Delegations     map[base.ChainID]ledger.DelegationOutput
}

// GetSequencersWithDelegations scans all chains. For sequencers collects all delegations to it
// Non-sequencer and non-delegations chained outputs are ignored
func (s SugaredStateReader) GetSequencersWithDelegations() (map[base.ChainID]DelegationsOnSequencer, error) {
	allOuts := make([]ledger.OutputWithChainID, 0)
	err := s.IterateChainedOutputs(func(out ledger.OutputWithChainID) bool {
		allOuts = append(allOuts, out)
		return true
	})
	if err != nil {
		return nil, err
	}
	ret := make(map[base.ChainID]DelegationsOnSequencer)
	// collect all sequencers
	for _, o := range allOuts {
		if o.Output.IsSequencerOutput() {
			seqEntry, seqEntryExists := ret[o.ChainID]
			if !seqEntryExists {
				seqEntry = DelegationsOnSequencer{
					Delegations: make(map[base.ChainID]ledger.DelegationOutput),
				}
			}
			seqEntry.SequencerOutput = util.Ref(o.OutputWithID)
			ret[o.ChainID] = seqEntry
		} else {
			if dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID); ok {
				seqEntry, seqEntryExists := ret[dOut.Target.ChainID()]
				if !seqEntryExists {
					seqEntry = DelegationsOnSequencer{
						Delegations: make(map[base.ChainID]ledger.DelegationOutput),
					}
				}
				seqEntry.Delegations[o.ChainID] = dOut
				ret[dOut.Target.ChainID()] = seqEntry
			}
		}
	}
	return ret, nil
}

func (s SugaredStateReader) GetDelegationsForSequencer(seqID base.ChainID, filter ...func(o *ledger.DelegationOutput) bool) ([]ledger.DelegationOutput, error) {
	flt := func(o *ledger.DelegationOutput) bool { return true }
	if len(filter) > 0 {
		flt = filter[0]
	}
	seqChainLock := ledger.ChainLockFromChainID(seqID)
	ret := make([]ledger.DelegationOutput, 0)
	err := s.IterateChainedOutputs(func(out ledger.OutputWithChainID) bool {
		lock := out.Output.Lock()
		if lock.Name() != ledger.DelegateLockName {
			return true
		}
		delegateLock := lock.(*ledger.DelegateLock)
		if !ledger.EqualAccountables(delegateLock.Target, seqChainLock) {
			return true
		}
		if dOut, ok := ledger.AsDelegationOutput(out.Output, out.ID); ok && flt(&dOut) {
			ret = append(ret, dOut)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) GetTagAlongBacklogForSequencer(seqID base.ChainID, filter ...func(o ledger.OutputWithID) bool) ([]ledger.OutputWithID, error) {
	flt := func(o ledger.OutputWithID) bool { return true }
	if len(filter) > 0 {
		flt = filter[0]
	}
	seqChainLock := ledger.ChainLockFromChainID(seqID)
	ret := make([]ledger.OutputWithID, 0)

	err := s.IterateOutputsForAccount(seqChainLock, func(oid base.OutputID, o *ledger.Output) bool {
		if _, idx := o.ChainConstraint(); idx != 0xff {
			// skip chained outputs
			return true
		}
		out := ledger.OutputWithID{
			ID:     oid,
			Output: o,
		}
		if flt(out) {
			ret = append(ret, out)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s SugaredStateReader) IterateTagAlongBacklog(seqID base.ChainID, fun func(o *ledger.TagAlongOutput) bool) error {
	return s.IterateOutputsForAccount(ledger.ChainLockFromChainID(seqID), func(oid base.OutputID, o *ledger.Output) bool {
		out := ledger.OutputWithID{
			ID:     oid,
			Output: o,
		}
		if ta := out.AsTagAlong(); ta.TagAlongLock != nil {
			return fun(&ta)
		}
		return true
	})
}

func (s SugaredStateReader) GetTagAlongBacklog(seqID base.ChainID) []*ledger.TagAlongOutput {
	ret := make([]*ledger.TagAlongOutput, 0)
	err := s.IterateTagAlongBacklog(seqID, func(o *ledger.TagAlongOutput) bool {
		ret = append(ret, o)
		return true
	})
	util.AssertNoError(err)
	return ret
}
