package transaction

import (
	"slices"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

func UnlockDataToString(data []byte) string {
	arr, err := tuples.TupleFromBytes(data)
	if err != nil {
		return err.Error()
	}
	return arr.String()
}

func ParseBytesToString(txBytes []byte, fetchOutput func(oid base.OutputID) ([]byte, bool)) string {
	tx, err := Parse(txBytes)
	if err != nil {
		return err.Error()
	}
	if err = tx.SetFullContext(tx.InputLoaderByIndex(fetchOutput)); err != nil {
		return err.Error()
	}
	return tx.String()
}

func PickOutputFromListFunc(lst []*ledger.OutputWithID) func(oid base.OutputID) ([]byte, bool) {
	return func(oid base.OutputID) ([]byte, bool) {
		idx := slices.IndexFunc(lst, func(o *ledger.OutputWithID) bool {
			return o.ID == oid
		})
		if idx < 0 {
			return nil, false
		}
		return lst[idx].Output.Bytes(), true
	}
}
