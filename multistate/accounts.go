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

// TODO check with chain origins

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
