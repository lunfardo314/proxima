package multistate

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
)

// BalanceOnLock returns balance and number of outputs
func BalanceOnLock(rdr global.StateIndexReader, account core.Accountable) (uint64, int) {
	oDatas, err := rdr.GetUTXOsLockedInAccount(account.AccountID())
	util.AssertNoError(err)

	balance := uint64(0)
	num := 0
	for _, od := range oDatas {
		o, err := od.Parse()
		util.AssertNoError(err)
		balance += o.Output.Amount()
		num++
	}
	return balance, num
}

func BalanceOnChainOutput(rdr global.StateIndexReader, chainID *core.ChainID) uint64 {
	oData, err := rdr.GetUTXOForChainID(chainID)
	if err != nil {
		return 0
	}
	o, _, err := oData.ParseAsChainOutput()
	util.AssertNoError(err)
	return o.Output.Amount()
}
