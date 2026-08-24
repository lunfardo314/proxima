package transaction

import (
	"github.com/lunfardo314/proxima/ledger"
)

// NativeTokenAggregator returns the per-tx native-token aggregator,
// allocating on first call. Mirrors the lazy-alloc shape of
// redeemedScripts — typical txs never invoke token() or tokenAmount()
// and pay zero allocation cost. See claude/archive/shipped/native_token.md.
func (tx *Transaction) NativeTokenAggregator() *ledger.NativeTokenAggregator {
	if tx.nativeTokenAggregator == nil {
		tx.nativeTokenAggregator = ledger.NewNativeTokenAggregator()
	}
	return tx.nativeTokenAggregator
}
