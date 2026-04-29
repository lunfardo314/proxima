package multistate

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// BalanceOnLock returns balance and number of outputs
func BalanceOnLock(rdr StateIndexReader, account ledger.Controller) (uint64, int) {
	oDatas, err := rdr.GetUTXOsForController(account.ControllerID())
	util.AssertNoError(err)

	balance := uint64(0)
	num := 0
	for _, od := range oDatas {
		o, err := od.Parse()
		util.AssertNoError(err)
		balance += o.Output.TokenBalance()
		num++
	}
	return balance, num
}

func BalanceOnChainOutput(rdr StateIndexReader, chainID base.ChainID) uint64 {
	oData, err := rdr.GetUTXOForChainID(chainID)
	if err != nil {
		return 0
	}
	o, err := oData.ParseAsChainOutput()
	util.AssertNoError(err)
	return o.Output.TokenBalance()
}
