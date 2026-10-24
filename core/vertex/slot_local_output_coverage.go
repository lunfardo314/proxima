package vertex

import (
	"context"
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

// CurrentSlotOutputCoverage evaluates the post-cone UTXO set without scanning the
// whole ledger. Native causality means a same-slot baseline can contain current
// outputs only from that tick-zero branch. Everything else newly created in the
// activity slot is in this validated delta cone. Roots and virtual reservations
// are read only. A branch closes its PRE-branch state at (slot-1,127), so its own
// future spends/outputs must not alter that state.
func (pc *PastCone) CurrentSlotOutputCoverage(ctx context.Context,
	getState func(base.TransactionID) multistate.StateReader,
	target base.LedgerTime, createdOutputs []*ledger.Output) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if pc.delta != nil || pc.baselineBranchID == nil {
		return 0, fmt.Errorf("output coverage: unfinished cone or missing baseline")
	}
	at := target
	if target.IsSlotBoundary() {
		if target.Slot == 0 {
			return 0, fmt.Errorf("cannot close pre-genesis slot")
		}
		at = base.T(target.Slot-1, base.MaxTickValue)
	}
	baseline := *pc.baselineBranchID
	// Foreign snapshot baselines are handled by the existing attacher sync rules.
	if baseline.Timestamp().After(at) {
		return 0, nil
	}
	rdr := getState(baseline)
	if rdr == nil {
		return 0, fmt.Errorf("output coverage: missing baseline state")
	}
	var sum, initial uint64
	add := func(dst *uint64, value uint64) error {
		if math.MaxUint64-*dst < value {
			return fmt.Errorf("output coverage overflow")
		}
		*dst += value
		return nil
	}
	// All consumers are already known native dependencies; do not invoke a VM,
	// selector, pull, or another state transition while measuring coverage.
	consumed := make(map[base.OutputID]bool)
	for vid := range pc.vertices {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		for idx, consumers := range pc.consumersByOutputIndex(vid) {
			for _, consumer := range consumers {
				ts := target
				if consumer != nil {
					ts = consumer.Timestamp()
				}
				if !ts.After(at) {
					consumed[vid.OutputID(idx)] = true
				}
			}
		}
	}
	// The native cone deduplicates transactions; consumed IDs deduplicate spends.
	accumulate := func(o *ledger.Output, id base.OutputID) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o == nil {
			return fmt.Errorf("output coverage: unavailable output %s", id.StringShort())
		}
		// Preserve the baseline's initial contribution across gaps even after spending.
		if id.TransactionID() == baseline {
			if err := add(&initial, ledger.CurrentSlotOutputCoverage(o, id, baseline.Timestamp())); err != nil {
				return err
			}
		}
		if consumed[id] {
			return nil
		}
		return add(&sum, ledger.CurrentSlotOutputCoverage(o, id, at))
	}
	for i := 0; i < baseline.NumProducedOutputs(); i++ {
		id := base.MustNewOutputID(baseline, byte(i))
		if err := accumulate(multistate.GetOutputFromStateReader(rdr, id), id); err != nil {
			return 0, err
		}
	}
	for vid := range pc.vertices {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if vid.ID() == baseline || pc.IsInTheState(vid) || vid.Slot() != at.Slot || vid.Timestamp().After(at) {
			continue
		}
		for i := 0; i < vid.NumProducedOutputs(); i++ {
			id := vid.OutputID(byte(i))
			if consumed[id] {
				continue
			}
			o, err := vid.OutputAt(byte(i))
			if err != nil {
				return 0, err
			}
			if err = accumulate(o, id); err != nil {
				return 0, err
			}
		}
	}
	if !target.IsSlotBoundary() && createdOutputs != nil {
		if len(createdOutputs) == 0 || len(createdOutputs) > 256 {
			return 0, fmt.Errorf("invalid candidate output count")
		}
		// Weight uses timestamp, not transaction hash (origin chains have no inherited
		// frozen weight). These are the actual outputs prepared by the builder;
		// ordinary native validation still runs on the final transaction.
		var hash base.TransactionIDShort
		hash[0] = byte(len(createdOutputs) - 1)
		txid := base.NewTransactionID(target, hash, true)
		for i, o := range createdOutputs {
			if err := accumulate(o, base.MustNewOutputID(txid, byte(i))); err != nil {
				return 0, err
			}
		}
	}
	if at.Slot > baseline.Slot() {
		if pc.branches == nil {
			return 0, fmt.Errorf("output coverage: branch history unavailable")
		}
		carry, err := ledger.SlotLocalHistoricalCarry(pc.branches.LedgerCoverage(baseline), initial, at.Slot-baseline.Slot())
		if err != nil {
			return 0, err
		}
		if err = add(&sum, carry); err != nil {
			return 0, err
		}
	}
	return sum, nil
}
