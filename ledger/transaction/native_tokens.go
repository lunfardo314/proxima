package transaction

import (
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/ledger"
)

// NativeTokenAggregator returns the per-tx native-token aggregator,
// allocating on first call. Mirrors the lazy-alloc shape of
// redeemedScripts — typical txs never invoke token() or tokenAmount()
// and pay zero allocation cost. See kb/archive/shipped/native_token.md.
func (tx *Transaction) NativeTokenAggregator() *ledger.NativeTokenAggregator {
	if tx.nativeTokenAggregator == nil {
		tx.nativeTokenAggregator = ledger.NewNativeTokenAggregator()
	}
	return tx.nativeTokenAggregator
}

// accumulateNativeTokenAmounts sums native-token amounts across the consumed
// and produced outputs by scanning each output tuple structurally and counting
// only TOP-LEVEL tokenAmount(tag, amount) constraints (indices >= lock slot;
// the two data slots 0/1 are skipped). This replaces the former
// accounting-by-evaluation-side-effect: a tokenAmount nested inside a lazy
// conditional is not a top-level constraint and is therefore never counted, so
// it cannot credit one side only (the native-token counterfeit).
//
// The per-tag entry must already exist (declared by a token(...) call, run
// earlier in validateTxLevelConstraints); a top-level tokenAmount referencing an
// undeclared tag is an error. Additions are overflow-checked.
func (tx *Transaction) accumulateNativeTokenAmounts(consumedOuts, producedOuts []*ledger.Output) error {
	// Must run even when no tag was declared (aggregator nil): a tokenAmount with
	// no matching token(...) declaration must be rejected, otherwise it fabricates
	// native tokens (uncounted at creation, spendable as real later).
	agg := tx.nativeTokenAggregator
	add := func(o *ledger.Output, produced bool) error {
		raw := o.ConstraintsRawBytes()
		// skip the two data slots (0 = amounts, 1 = index-values); scan real
		// constraint bytecode from the lock slot onward (tokenAmount may sit at
		// any position from the lock slot up).
		for i := int(ledger.ConstraintIndexLock); i < len(raw); i++ {
			ta, err := ledger.TokenAmountFromBytesWithLib(raw[i], tx.Library)
			if err != nil {
				continue // not a top-level tokenAmount constraint
			}
			if agg == nil {
				return fmt.Errorf("tokenAmount: tag %s not declared at tx level (no token(...) call in tx)", ta.Tag.String())
			}
			e := agg.Entry(ta.Tag)
			if e == nil {
				return fmt.Errorf("tokenAmount: tag %s not declared at tx level (missing token(...) call)", ta.Tag.String())
			}
			if produced {
				if e.ProducedSum > math.MaxUint64-ta.Amount {
					return fmt.Errorf("native token produced sum overflow for tag %s", ta.Tag.String())
				}
				e.ProducedSum += ta.Amount
			} else {
				if e.ConsumedSum > math.MaxUint64-ta.Amount {
					return fmt.Errorf("native token consumed sum overflow for tag %s", ta.Tag.String())
				}
				e.ConsumedSum += ta.Amount
			}
		}
		return nil
	}
	for _, o := range consumedOuts {
		if err := add(o, false); err != nil {
			return err
		}
	}
	for _, o := range producedOuts {
		if err := add(o, true); err != nil {
			return err
		}
	}
	return nil
}
