package multistate

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

// Collection of functions to query the state and collect all kind of stats

// TODO revisit and optimize the whole set of APIs

type (
	LockedAccountInfo struct {
		Balance    uint64
		NumOutputs int
	}

	ChainRecordInfo struct {
		Balance uint64
		Output  *ledger.OutputDataWithID
	}

	AccountInfo struct {
		LockedAccounts map[string]LockedAccountInfo
		ChainRecords   map[base.ChainID]ChainRecordInfo
	}

	SummarySupplyAndInflation struct {
		NumberOfBranches int
		OldestSlot       uint32
		LatestSlot       uint32
		BeginSupply      uint64
		EndSupply        uint64
		TotalInflation   uint64
		InfoPerSeqID     map[base.ChainID]SequencerInfo
	}

	SequencerInfo struct {
		BeginBalance      uint64
		EndBalance        uint64
		NumBranches       int
		StemInTheHeaviest base.OutputID
	}
)

func MustCollectAccountInfo(store global.Store, root common.VCommitment) *AccountInfo {
	rdr := MustNewReadable(store, root)
	chainRecs, err := MakeSugared(rdr).GetAllChainsOld() // TODO a bit ugly
	util.AssertNoError(err)
	return &AccountInfo{
		LockedAccounts: rdr.AccountsByLocks(),
		ChainRecords:   chainRecs,
	}
}

func (a *AccountInfo) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	ret.Add("Locked accounts: %d", len(a.LockedAccounts))
	lockedAccountsSorted := util.KeysSorted(a.LockedAccounts, func(k1, k2 string) bool {
		if strings.HasPrefix(k1, "stem") {
			return true
		}
		if strings.HasPrefix(k2, "stem") {
			return false
		}
		return k1 < k2
	})
	sum := uint64(0)
	for _, k := range lockedAccountsSorted {
		ai := a.LockedAccounts[k]
		ret.Add("   %s :: balance: %s, outputs: %d", k, util.Th(ai.Balance), ai.NumOutputs)
		sum += ai.Balance
	}
	ret.Add("--------------------------------")
	ret.Add("   Total in locked accounts: %s", util.Th(sum))

	ret.Add("Chains: %d", len(a.ChainRecords))
	chainIDSSorted := util.KeysSorted(a.ChainRecords, func(k1, k2 base.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	sum = 0
	for _, chainID := range chainIDSSorted {
		ci := a.ChainRecords[chainID]
		ret.Add("   %s :: %s   seq=%v branch=%v", chainID.String(), util.Th(ci.Balance), ci.Output.ID.IsSequencerTransaction(), ci.Output.ID.IsBranchTransaction())
		sum += ci.Balance
	}
	ret.Add("--------------------------------")
	ret.Add("   Total on chains: %s", util.Th(sum))
	return ret
}

func FetchSummarySupply(stateStore global.Store, nBack int) *SummarySupplyAndInflation {
	branchData := FetchHeaviestBranchChainNSlotsBack(stateStore, nBack) // descending
	util.Assertf(len(branchData) > 0, "len(branchData) > 0")

	ret := &SummarySupplyAndInflation{
		BeginSupply:      branchData[len(branchData)-1].Supply,
		EndSupply:        branchData[0].Supply,
		TotalInflation:   branchData[0].Supply - branchData[len(branchData)-1].Supply,
		NumberOfBranches: len(branchData),
		OldestSlot:       branchData[len(branchData)-1].Stem.Timestamp().Slot,
		LatestSlot:       branchData[0].Stem.Timestamp().Slot,
		InfoPerSeqID:     make(map[base.ChainID]SequencerInfo),
	}
	// count branches per sequencer
	for i := 0; i < len(branchData)-1; i++ {
		seqInfo := ret.InfoPerSeqID[branchData[i].SequencerID]
		seqInfo.NumBranches++
		ret.InfoPerSeqID[branchData[i].SequencerID] = seqInfo
	}
	util.Assertf(ret.EndSupply-ret.BeginSupply == ret.TotalInflation, "FetchSummarySupply: ret.EndSupply - ret.BeginSupply == ret.SlotInflation")

	for seqID, seqInfo := range ret.InfoPerSeqID {
		rdr := MustNewSugaredReadableState(stateStore, branchData[0].Root) // heaviest
		o, err := rdr.GetChainOutputWithID(seqID)
		if err == nil {
			seqInfo.EndBalance = o.Output.TokenBalance()
		}
		stem, err := rdr.GetChainOutputWithID(seqID)
		util.AssertNoError(err)
		seqInfo.StemInTheHeaviest = stem.ID

		rdr = MustNewSugaredReadableState(stateStore, branchData[len(branchData)-1].Root)
		o, err = rdr.GetChainOutputWithID(seqID)
		if err == nil {
			seqInfo.BeginBalance = o.Output.TokenBalance()
		}
		ret.InfoPerSeqID[seqID] = seqInfo
	}
	return ret
}

func (s *SummarySupplyAndInflation) Lines(prefix ...string) *lines.Lines {
	pInfl := util.Percent(int(s.TotalInflation), int(s.BeginSupply))
	nSlots := s.LatestSlot - s.OldestSlot + 1

	ret := lines.New(prefix...).
		Add("Slots from %d to %d inclusive. Total %d slots", s.OldestSlot, s.LatestSlot, nSlots).
		Add("Number of branches: %d", s.NumberOfBranches).
		Add("Supply: %s -> %s (+%s, %.6f%%)", util.Th(s.BeginSupply), util.Th(s.EndSupply), util.Th(s.TotalInflation), pInfl).
		Add("Per sequencer along the heaviest chain:")

	sortedSeqIDs := util.KeysSorted(s.InfoPerSeqID, func(k1, k2 base.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	for _, seqId := range sortedSeqIDs {

		seqInfo := s.InfoPerSeqID[seqId]
		var inflStr string
		if seqInfo.EndBalance >= seqInfo.BeginBalance {
			inflStr = "+" + util.Th(seqInfo.EndBalance-seqInfo.BeginBalance)
		} else {
			inflStr = "-" + util.Th(seqInfo.BeginBalance-seqInfo.EndBalance)
		}
		ret.Add("         %s : last milestone in the heaviest: %25s, branches: %d, balance: %s -> %s (%s)",
			seqId.StringShort(),
			seqInfo.StemInTheHeaviest.StringShort(),
			seqInfo.NumBranches,
			util.Th(seqInfo.BeginBalance), util.Th(seqInfo.EndBalance),
			inflStr)
	}
	return ret
}

func (r *Readable) AccountsByLocks() map[string]LockedAccountInfo {
	lib := ledger.L(base.MaxSlot)
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var oid base.OutputID
	var err error

	ret := make(map[string]LockedAccountInfo)

	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	r.trie.Iterator([]byte{TriePartitionControllers}).IterateKeys(func(k []byte) bool {
		oid, err = base.OutputIDFromBytes(k[2+k[1]:])
		util.AssertNoError(err)

		oData, found := r._getUTXO(oid, partition)
		util.Assertf(found, "can't get output")

		// Use output's slot for parsing
		_, amounts, lock, err := ledger.OutputFromBytesMainWithLib(oData, lib)
		util.AssertNoError(err)

		lockStr := lock.String()
		lockInfo := ret[lockStr]
		lockInfo.Balance += amounts.TokenBalance()
		lockInfo.NumOutputs++
		ret[lockStr] = lockInfo

		return true
	})
	return ret
}

type ScannedState struct {
	NumUTXOs        int
	Supply          uint64
	TotalOnChains   uint64
	Stem            *ledger.OutputWithID
	Chains          map[base.ChainID]*ledger.OutputWithID
	Inconsistencies []string
}

func (s *ScannedState) AddInconsistency(format string, args ...any) {
	s.Inconsistencies = append(s.Inconsistencies, fmt.Sprintf(format, args...))
}

func (s *ScannedState) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	ln.Add("Number of UTXOs: %d", s.NumUTXOs)
	ln.Add("Total supply: %s", util.Th(s.Supply))
	ln.Add("Total on chains: %s", util.Th(s.TotalOnChains))
	ln.Add("Stem output: %s", s.Stem.ID.StringShort())
	ln.Add("Inconsistencies (%d):", len(s.Inconsistencies))
	for _, inconsistency := range s.Inconsistencies {
		ln.Add("   %s", inconsistency)
	}
	ln.Add("Chains (%d):", len(s.Chains))
	for chainID, o := range s.Chains {
		ln.Add("             %s: %s", chainID.String(), o.String())
	}
	return ln
}

// ScanState scans state, collects info and checks its consistency
// FIXME
func (r *Readable) ScanState() *ScannedState {
	ret := &ScannedState{
		Chains:          make(map[base.ChainID]*ledger.OutputWithID),
		Inconsistencies: make([]string, 0),
	}

	r.IterateUTXOs(func(o ledger.OutputWithID) bool {
		ret.NumUTXOs++
		ret.Supply += o.Output.TokenBalance()
		if _, isStemOutput := o.Output.StemLock(); isStemOutput {
			if ret.Stem != nil {
				ret.AddInconsistency("duplicate stem:\n--- 1\n%s--- 2\n%s",
					ret.Stem.LinesSource("   ").String(),
					o.LinesSource("   ").String())
			}
			if _, stemBytes := r.GetStem(); !bytes.Equal(stemBytes, o.Output.Bytes()) {
				ret.AddInconsistency("stem output %s inconsistent with the stem index", o.ID.String())
			}
			ret.Stem = util.Ref(o)
		}
		for _, accountable := range o.Output.Lock().Controllers() {
			accountKey := makeAccountKey(accountable.ControllerID(), o.ID)
			if oData := r.trie.Get(accountKey); len(oData) == 0 {
				ret.AddInconsistency("output %s is not in the accounts index", o.ID.String())
			}
		}
		if chainID, ok := o.ExtractChainID(); ok {
			if _, already := ret.Chains[chainID]; already {
				ret.AddInconsistency("duplicated chain record:\n--- 1\n%s--- 2\n%s",
					ret.Chains[chainID].LinesSource("   ").String(),
					o.LinesSource("   ").String())
			}
			ret.Chains[chainID] = util.Ref(o)
			if _, err := r.GetUTXOForChainID(chainID); err != nil {
				ret.AddInconsistency("chain record %s is not in the UTXO index: %v", chainID.String(), err)
			}
			ret.TotalOnChains += o.Output.TokenBalance()
		}
		return true
	})
	return ret
}
