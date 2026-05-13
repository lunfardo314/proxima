package transaction

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// NativeTokenAggregator returns the per-tx native-token aggregator,
// allocating on first call. Mirrors the lazy-alloc shape of
// redeemedScripts — typical txs never invoke `token(...)` and pay zero
// allocation cost. Phase B / native_token.md.
func (tx *Transaction) NativeTokenAggregator() *ledger.NativeTokenAggregator {
	if tx.nativeTokenAggregator == nil {
		tx.nativeTokenAggregator = ledger.NewNativeTokenAggregator()
	}
	return tx.nativeTokenAggregator
}

// validateNativeTokenAuditability enforces the "every observed tag must
// be declared by a tx-level token(tag, ...)" rule (Phase D / §4 of
// claude/native_token.md). Run at the tail of validateOutputs once all
// tx-level constraints and per-output constraints have fired.
//
// Two cases:
//   - At least one token() was invoked: the aggregator is populated and
//     scanned; iterate observed tags and reject any missing declaration.
//   - No token() was invoked: kick off a one-shot scan so we still catch
//     stray tokenAmount instances. If no tokenAmount instances exist
//     either, the scan is a no-op and the aggregator stays empty.
func (tx *Transaction) validateNativeTokenAuditability() error {
	agg := tx.NativeTokenAggregator()
	if !agg.Scanned() {
		ctx := ledger.NewEvalContext(tx)
		if err := ledger.ScanNativeTokens(ctx); err != nil {
			return fmt.Errorf("native token scan: %w", err)
		}
	}
	var auditErr error
	agg.ObservedTags(func(tag base.ChainID, _, _ uint64) {
		if auditErr == nil && !agg.IsDeclared(tag) {
			auditErr = fmt.Errorf("undeclared native token tag %s", tag.String())
		}
	})
	return auditErr
}
