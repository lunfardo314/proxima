package multistate

import (
	"bytes"
	"strings"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
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
		Output      *ledger.OutputDataWithID
		IsSequencer bool
		IsBranch    bool
	}

	AccountInfo struct {
		LockedAccounts map[string]LockedAccountInfo
		ChainRecords   map[ledger.ChainID]ChainRecordInfo
	}

	SummarySupplyAndInflation struct {
		NumberOfBranches int
		OldestSlot       ledger.Slot
		LatestSlot       ledger.Slot
		BeginSupply      uint64
		EndSupply        uint64
		TotalInflation   uint64
		InfoPerSeqID     map[ledger.ChainID]SequencerInfo
	}

	SequencerInfo struct {
		BeginBalance      uint64
		EndBalance        uint64
		NumBranches       int
		StemInTheHeaviest ledger.OutputID
	}
)

func MustCollectAccountInfo(store global.StateStore, root common.VCommitment) *AccountInfo {
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
		ret.Add("   %s :: balance: %s, outputs: %d", k, util.Th(ai.Balance), ai.NumOutputs)
		sum += ai.Balance
	}
	ret.Add("--------------------------------")
	ret.Add("   Total in locked accounts: %s", util.Th(sum))

	ret.Add("Chains: %d", len(a.ChainRecords))
	chainIDSSorted := util.KeysSorted(a.ChainRecords, func(k1, k2 ledger.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	sum = 0
	for _, chainID := range chainIDSSorted {
		ci := a.ChainRecords[chainID]
		ret.Add("   %s :: %s   seq=%v branch=%v", chainID.String(), util.Th(ci.Balance), ci.IsSequencer, ci.IsBranch)
		sum += ci.Balance
	}
	ret.Add("--------------------------------")
	ret.Add("   Total on chains: %s", util.Th(sum))
	return ret
}

func FetchSummarySupply(stateStore global.StateStore, nBack int) *SummarySupplyAndInflation {
	branchData := FetchHeaviestBranchChainNSlotsBack(stateStore, nBack) // descending
	util.Assertf(len(branchData) > 0, "len(branchData) > 0")

	ret := &SummarySupplyAndInflation{
		BeginSupply:      branchData[len(branchData)-1].Supply,
		EndSupply:        branchData[0].Supply,
		TotalInflation:   branchData[0].Supply - branchData[len(branchData)-1].Supply,
		NumberOfBranches: len(branchData),
		OldestSlot:       branchData[len(branchData)-1].Stem.Timestamp().Slot(),
		LatestSlot:       branchData[0].Stem.Timestamp().Slot(),
		InfoPerSeqID:     make(map[ledger.ChainID]SequencerInfo),
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
		o, err := rdr.GetChainOutput(&seqID)
		if err == nil {
			seqInfo.EndBalance = o.Output.Amount()
		}
		stem, err := rdr.GetChainOutput(&seqID)
		util.AssertNoError(err)
		seqInfo.StemInTheHeaviest = stem.ID

		rdr = MustNewSugaredReadableState(stateStore, branchData[len(branchData)-1].Root)
		o, err = rdr.GetChainOutput(&seqID)
		if err == nil {
			seqInfo.BeginBalance = o.Output.Amount()
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

	sortedSeqIDs := util.KeysSorted(s.InfoPerSeqID, func(k1, k2 ledger.ChainID) bool {
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
