package multistate

import (
	"bytes"
	"strings"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

type (
	LockedAccountInfo struct {
		Balance    uint64
		NumOutputs int
	}

	ChainRecordInfo struct {
		Balance     uint64
		IsSequencer bool
		IsBranch    bool
	}

	AccountInfo struct {
		LockedAccounts map[string]LockedAccountInfo
		ChainRecords   map[core.ChainID]ChainRecordInfo
	}

	SummarySupplyAndInflation struct {
		NumberOfBranches int
		OldestSlot       core.TimeSlot
		LatestSlot       core.TimeSlot
		BeginSupply      uint64
		EndSupply        uint64
		TotalInflation   uint64
		InfoPerSeqID     map[core.ChainID]SequencerInfo
	}

	SequencerInfo struct {
		BeginBalance   uint64
		EndBalance     uint64
		TotalInflation uint64
		NumBranches    int
	}
)

func MustCollectAccountInfo(store general.StateStore, root common.VCommitment) *AccountInfo {
	rdr := MustNewReadable(store, root)
	return &AccountInfo{
		LockedAccounts: rdr.AccountsByLocks(),
		ChainRecords:   rdr.ChainInfo(),
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
		ret.Add("   %s :: balance: %s, outputs: %d", k, util.GoThousands(ai.Balance), ai.NumOutputs)
		sum += ai.Balance
	}
	ret.Add("--------------------------------")
	ret.Add("   Total in locked accounts: %s", util.GoThousands(sum))

	ret.Add("Chains: %d", len(a.ChainRecords))
	chainIDSSorted := util.KeysSorted(a.ChainRecords, func(k1, k2 core.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	sum = 0
	for _, chainID := range chainIDSSorted {
		ci := a.ChainRecords[chainID]
		ret.Add("   %s :: %s   seq=%v branch=%v", chainID.Short(), util.GoThousands(ci.Balance), ci.IsSequencer, ci.IsBranch)
		sum += ci.Balance
	}
	ret.Add("--------------------------------")
	ret.Add("   Total on chains: %s", util.GoThousands(sum))
	return ret
}

func FetchSummarySupplyAndInflation(stateStore general.StateStore, nBack int) *SummarySupplyAndInflation {
	branchData := FetchHeaviestBranchChainNSlotsBack(stateStore, nBack) // descending
	util.Assertf(len(branchData) > 0, "len(branchData) > 0")

	return CalcSummarySupplyAndInflation(branchData, stateStore)
}

func CalcSummarySupplyAndInflation(branchData []*BranchData, stateStore general.StateStore) *SummarySupplyAndInflation {
	ret := &SummarySupplyAndInflation{
		BeginSupply:      branchData[len(branchData)-1].Stem.Output.MustStemLock().Supply,
		EndSupply:        branchData[0].Stem.Output.MustStemLock().Supply,
		TotalInflation:   0,
		NumberOfBranches: len(branchData),
		OldestSlot:       branchData[len(branchData)-1].Stem.Timestamp().TimeSlot(),
		LatestSlot:       branchData[0].Stem.Timestamp().TimeSlot(),
		InfoPerSeqID:     make(map[core.ChainID]SequencerInfo),
	}
	for i := 0; i < len(branchData)-1; i++ {
		inflation := branchData[i].Stem.Output.MustStemLock().InflationAmount
		ret.TotalInflation += inflation

		seqInfo := ret.InfoPerSeqID[branchData[i].SequencerID]
		seqInfo.NumBranches++
		seqInfo.TotalInflation += inflation
		ret.InfoPerSeqID[branchData[i].SequencerID] = seqInfo
	}
	util.Assertf(ret.EndSupply-ret.BeginSupply == ret.TotalInflation, "FetchSummarySupplyAndInflation: ret.EndSupply - ret.BeginSupply == ret.TotalInflation")

	for seqID, seqInfo := range ret.InfoPerSeqID {
		rdr := MustNewSugaredReadableState(stateStore, branchData[0].Root)
		o, err := rdr.GetChainOutput(&seqID)
		if err == nil {
			seqInfo.EndBalance = o.Output.Amount()
		}

		for i := len(branchData) - 1; i >= 0; i-- {
			rdr = MustNewSugaredReadableState(stateStore, branchData[i].Root)
			o, err = rdr.GetChainOutput(&seqID)
			if err == nil {
				seqInfo.BeginBalance = o.Output.Amount()
				break
			}
		}
		ret.InfoPerSeqID[seqID] = seqInfo
	}
	return ret
}

func (s *SummarySupplyAndInflation) Lines(prefix ...string) *lines.Lines {
	totalInflationPercentage := float32(s.TotalInflation*100) / float32(s.BeginSupply)
	totalInflationPercentagePerSlot := totalInflationPercentage / float32(s.LatestSlot-s.OldestSlot+1)
	totalInflationPercentageYearlyExtrapolation := totalInflationPercentagePerSlot * float32(core.TimeSlotsPerYear())

	ret := lines.New(prefix...).
		Add("Slots from %d to %d inclusive. Total %d slots", s.OldestSlot, s.LatestSlot, s.LatestSlot-s.OldestSlot+1).
		Add("Number of branches: %d", s.NumberOfBranches).
		Add("Supply begin: %s", util.GoThousands(s.BeginSupply)).
		Add("Supply end: %s", util.GoThousands(s.EndSupply)).
		Add("Total inflation: %s (%.6f%%)", util.GoThousands(s.TotalInflation), totalInflationPercentage).
		Add("Average inflation per slot: %.8f%%", totalInflationPercentagePerSlot).
		Add("Annual inflation extrapolated: %.2f%%", totalInflationPercentageYearlyExtrapolation).
		Add("Info per sequencer:")

	sortedSeqIDs := util.KeysSorted(s.InfoPerSeqID, func(k1, k2 core.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	for _, seqId := range sortedSeqIDs {
		seqInfo := s.InfoPerSeqID[seqId]
		ret.Add("    %s : inflation: %s, number of branches: %d, balance: %s -> %s",
			seqId.Short(), util.GoThousands(seqInfo.TotalInflation), seqInfo.NumBranches,
			util.GoThousands(seqInfo.BeginBalance), util.GoThousands(seqInfo.EndBalance))
	}
	return ret
}
