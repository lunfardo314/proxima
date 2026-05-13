package ledger

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
)

// NativeTokenAggregator caches per-tag native-token sums for a single
// transaction. Populated lazily by the first `token(...)` builtin call
// in the tx; later calls hit the cache.
//
// Observed sums (per tag) are collected by scanning every
// `tokenAmount(tag, amount)` constraint across all consumed and
// produced outputs. Declared tags are recorded by each `token(tag,
// ...)` call at the TxConstraints level. Phase D consumes both sets to
// enforce: every observed tag must be declared.
//
// All state is per-tx and accessed single-threadedly by the EasyFL
// evaluator — no mutex required.
type NativeTokenAggregator struct {
	scanned  bool
	observed map[base.ChainID]*nativeTokenSum
	declared map[base.ChainID]struct{}
}

type nativeTokenSum struct {
	consumed uint64
	produced uint64
}

func NewNativeTokenAggregator() *NativeTokenAggregator {
	return &NativeTokenAggregator{
		observed: map[base.ChainID]*nativeTokenSum{},
		declared: map[base.ChainID]struct{}{},
	}
}

func (a *NativeTokenAggregator) Scanned() bool { return a.scanned }
func (a *NativeTokenAggregator) MarkScanned()  { a.scanned = true }

// AddConsumed accumulates a tokenAmount on the consumed side.
// Returns an overflow error if the sum would wrap.
func (a *NativeTokenAggregator) AddConsumed(tag base.ChainID, amount uint64) error {
	e := a.entry(tag)
	if e.consumed+amount < e.consumed {
		return fmt.Errorf("native token consumed sum overflow for tag %s", tag.String())
	}
	e.consumed += amount
	return nil
}

// AddProduced accumulates a tokenAmount on the produced side.
func (a *NativeTokenAggregator) AddProduced(tag base.ChainID, amount uint64) error {
	e := a.entry(tag)
	if e.produced+amount < e.produced {
		return fmt.Errorf("native token produced sum overflow for tag %s", tag.String())
	}
	e.produced += amount
	return nil
}

func (a *NativeTokenAggregator) entry(tag base.ChainID) *nativeTokenSum {
	e, ok := a.observed[tag]
	if !ok {
		e = &nativeTokenSum{}
		a.observed[tag] = e
	}
	return e
}

// Sum returns the (consumed, produced) sums for tag. The third return
// is true if the tag has at least one tokenAmount instance in the tx.
func (a *NativeTokenAggregator) Sum(tag base.ChainID) (consumed, produced uint64, observed bool) {
	e, ok := a.observed[tag]
	if !ok {
		return 0, 0, false
	}
	return e.consumed, e.produced, true
}

// Declare records that a tx-level `token(tag, ...)` constraint was
// invoked for this tag. Idempotent.
func (a *NativeTokenAggregator) Declare(tag base.ChainID) {
	a.declared[tag] = struct{}{}
}

// IsDeclared reports whether `token(tag, ...)` was invoked for this tag.
func (a *NativeTokenAggregator) IsDeclared(tag base.ChainID) bool {
	_, ok := a.declared[tag]
	return ok
}

// ObservedTags iterates each tag that appeared in any tokenAmount
// constraint with its accumulated (consumed, produced) sums. Iteration
// order is unspecified.
func (a *NativeTokenAggregator) ObservedTags(fn func(tag base.ChainID, consumed, produced uint64)) {
	for tag, e := range a.observed {
		fn(tag, e.consumed, e.produced)
	}
}