package utxodb

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
)

func (u *UTXODB) MakeParallelTransferSequences(nSeq, howLong int, amount uint64) ([][][]byte, error) {
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(0, nSeq, amount)
	firstOuts := make([]*ledger.OutputWithID, nSeq)

	for i := range firstOuts {
		odatas, err := u.StateReader().GetUTXOsInAccount(addrs[i].AccountID())
		if err != nil {
			return nil, err
		}
		if len(odatas) != 1 {
			return nil, fmt.Errorf("inconsistency: len(odatas) != 1")
		}
		outs, err := ledger.ParseAndSortOutputData(odatas, nil)
		if err != nil {
			return nil, err
		}
		firstOuts[i] = outs[0]
	}

	ret, err := txbuilder.MakeTransactionSequences(howLong, firstOuts, privKeys)
	if err != nil {
		return nil, err
	}
	return ret, nil
}
