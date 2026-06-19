// Package delegationpool maintains a per-sequencer in-memory model of the
// delegations targeted at the sequencer, used to optimize the freeze-epoch
// distribution (see claude/delegation_freeze_distribution.md).
//
// It is an OPTIMIZATION-ONLY cache: every freeze is still bounded and
// re-validated by FreezeDelegation, and the proposer reads the objective
// delegation state from the ledger immediately before consuming it. A stale or
// wrong pool can at worst cost a missed or wasted freeze attempt, never an
// invalid transaction. Freeze-state is sequencer-controlled and maintained
// incrementally from the sequencer's own accepted milestones; the LRB is read
// only to bootstrap, to reconcile the sequencer's own tentative transitions,
// and to discover new delegations (via a push listener, not a periodic scan).
package delegationpool

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const TraceTag = "delegationpool"

type (
	Environment interface {
		global.NodeGlobal
		attacher.Environment // for Branches() + StateStore() (nil-safe LRB access)
		SequencerID() base.ChainID
		SequencerName() string
		ListenToControllerAccount(account ledger.Controller, fun func(wOut vertex.WrappedOutput))
		BacklogTTLSlots() (int, int)
	}

	transitionKind byte

	// pendingTransition is a sequencer-authored freeze or unfreeze (askStop ->
	// OnHold response) that has been applied in an accepted own milestone but
	// not yet confirmed in the LRB. The carrying milestone may orphan, so the
	// entry's confirmed fields are left untouched until Reconcile settles or
	// voids it.
	pendingTransition struct {
		kind        transitionKind
		slot        uint32        // ledger slot the transition was applied in
		untilEpoch  uint32        // freeze only: assigned freezeUntilEpoch (its position in D)
		amount      uint64        // freeze only: frozen successor token balance (its weight in D)
		successorID base.OutputID // produced output, used to confirm/void against the LRB
	}

	// delegationEntry is the slim per-delegation record (keyed by ChainID).
	// The full ledger.DelegationOutput is never stored — it is fetched by ID
	// only for the handful of candidates actually selected to freeze.
	delegationEntry struct {
		outputID          base.OutputID // current confirmed UTXO
		amount            uint64        // confirmed TokenBalance
		state             byte          // confirmed Undef / Frozen / OnHold
		lastFrozenEpoch   uint32        // confirmed last-frozen epoch (0 if never)
		maxFrozenEpochs   byte          // per-output cap
		freezableFromSlot uint32        // first slot at which IsUnlockableByTargetForFreezing is true
		addedSlot         uint32        // when the entry entered the pool (for TTL of unconfirmed)
		confirmed         bool          // seen in the LRB (bootstrap or reconcile); else listener-tentative
		pending           *pendingTransition
	}

	// Candidate is a freezable delegation handed to the proposer. State is Undef
	// (first-time freeze) or Frozen (continuation, window elapsed).
	Candidate struct {
		ChainID         base.ChainID
		OutputID        base.OutputID
		Amount          uint64
		State           byte
		MaxFrozenEpochs byte
	}

	DelegationPool struct {
		Environment
		target  base.ChainID
		mutex   sync.RWMutex
		entries map[base.ChainID]*delegationEntry
	}
)

const (
	transitionFreeze transitionKind = iota
	transitionUnfreeze
)

const reconcilePeriod = time.Second

// New bootstraps the pool from the LRB, registers the new-delegation listener
// and starts periodic reconciliation. Bootstrap must complete before the first
// freeze, hence it is synchronous here (called from sequencer.New).
func New(env Environment) (*DelegationPool, error) {
	seqID := env.SequencerID()
	ret := &DelegationPool{
		Environment: env,
		target:      seqID,
		entries:     make(map[base.ChainID]*delegationEntry),
	}
	ret.bootstrap()
	// Delegation outputs index under their Target account, whose bytes equal the
	// chain-lock account bytes of the same chain ID. ListenToControllerAccount
	// fires for any produced output whose index-values contain these bytes; the
	// callback filters to delegations actually targeting this sequencer.
	env.ListenToControllerAccount(ledger.ChainLockFromChainID(seqID), ret.onNewOutput)

	env.RepeatInBackground(env.SequencerName()+"_delegationPoolReconcile", reconcilePeriod, func() bool {
		ret.Reconcile()
		return true
	}, true)
	env.Tracef(TraceTag, "[%s] delegation pool started, %d delegations bootstrapped", env.SequencerName(), len(ret.entries))
	return ret, nil
}

// bootstrap does the single startup IterateDelegatedOutputs scan. Best-effort:
// if no LRB is established yet (e.g. at very early startup), the pool starts
// empty and is populated by the listener + reconciliation.
func (p *DelegationPool) bootstrap() {
	lrb := p.Branches().FindLatestReliableBranch()
	if lrb == nil {
		return
	}
	rdr := multistate.MustNewSugaredReadableState(p.StateStore(), lrb.Root, 0)
	entries := make(map[base.ChainID]*delegationEntry)
	rdr.IterateDelegatedOutputs(p.target, func(o *ledger.DelegationOutput) bool {
		entries[o.ChainID] = entryFromOutput(o)
		return true
	})
	p.mutex.Lock()
	p.entries = entries
	p.mutex.Unlock()
}

// onNewOutput enrolls a freshly-seen delegation as a new pool entry. Known
// ChainIDs are left untouched — their freeze-state is event-authoritative
// (maintained by ApplyMilestone), never overwritten by discovery.
func (p *DelegationPool) onNewOutput(wOut vertex.WrappedOutput) {
	owid := wOut.OutputWithID()
	if owid == nil {
		return
	}
	dOut, ok := ledger.AsDelegationOutput(owid.Output, owid.ID)
	if !ok || dOut.Target != p.target {
		return
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if _, already := p.entries[dOut.ChainID]; already {
		return
	}
	e := entryFromOutput(&dOut)
	e.confirmed = false // came from the memDAG; may never confirm (orphan)
	p.entries[dOut.ChainID] = e
}

// ApplyMilestone records the sequencer's own freeze / unfreeze transitions from
// an accepted milestone as tentative (pending) until reconciled against the LRB.
func (p *DelegationPool) ApplyMilestone(vid *vertex.WrappedTx) {
	slot := vid.Slot()
	type tr struct {
		chainID base.ChainID
		pt      pendingTransition
	}
	var transitions []tr
	vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		v.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
			dOut, ok := ledger.AsDelegationOutput(o, oid)
			if !ok || dOut.Target != p.target {
				return true
			}
			switch dOut.State {
			case ledger.DelegateLockStateFrozen:
				transitions = append(transitions, tr{dOut.ChainID, pendingTransition{
					kind:        transitionFreeze,
					slot:        slot,
					untilEpoch:  dOut.LastFrozenEpoch,
					amount:      dOut.Output.TokenBalance(),
					successorID: oid,
				}})
			case ledger.DelegateLockStateOnHold:
				transitions = append(transitions, tr{dOut.ChainID, pendingTransition{
					kind:        transitionUnfreeze,
					slot:        slot,
					successorID: oid,
				}})
			}
			return true
		})
	}})
	if len(transitions) == 0 {
		return
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	for _, t := range transitions {
		e := p.entries[t.chainID]
		if e == nil {
			// freezing a delegation not yet in the pool (discovery raced). Create a
			// bare entry; Reconcile will fill confirmed fields when it settles.
			e = &delegationEntry{addedSlot: slot}
			p.entries[t.chainID] = e
		}
		pt := t.pt
		e.pending = &pt
	}
}

// Reconcile settles or voids previous-slot tentative transitions against the
// LRB and evicts stale unconfirmed (listener-discovered) entries. Both work on
// small subsets only — never an O(all) scan. Driven by a background timer.
func (p *DelegationPool) Reconcile() {
	lrb := p.Branches().FindLatestReliableBranch()
	if lrb == nil {
		return
	}
	rdr := multistate.MustNewSugaredReadableState(p.StateStore(), lrb.Root, 0)
	lrbSlot := lrb.Stem.ID.Slot()
	_, ttlDelegationSlots := p.BacklogTTLSlots()

	// collect suspects under RLock (small sets), do state reads outside the lock
	type suspect struct {
		chainID  base.ChainID
		outputID base.OutputID
		pending  bool
	}
	var suspects []suspect
	p.mutex.RLock()
	for cid, e := range p.entries {
		switch {
		case e.pending != nil && e.pending.slot <= lrbSlot:
			suspects = append(suspects, suspect{cid, e.pending.successorID, true})
		case e.pending == nil && !e.confirmed && e.state == ledger.DelegateLockStateUndef &&
			e.addedSlot+uint32(ttlDelegationSlots) < lrbSlot:
			suspects = append(suspects, suspect{cid, e.outputID, false})
		}
	}
	p.mutex.RUnlock()
	if len(suspects) == 0 {
		return
	}

	type action struct {
		chainID  base.ChainID
		checkID  base.OutputID    // the ID this action was decided against (guards concurrent updates)
		pending  bool             // action concerns a pending transition (vs an unconfirmed Undef)
		settleTo *delegationEntry // non-nil => settle pending to this confirmed state
		drop     bool
		confirm  bool             // mark an unconfirmed Undef enrollment as confirmed
	}
	actions := make([]action, 0, len(suspects))
	for _, s := range suspects {
		owid, err := rdr.GetOutputWithID(s.outputID)
		present := err == nil && owid != nil
		a := action{chainID: s.chainID, checkID: s.outputID, pending: s.pending}
		if s.pending {
			if present {
				if dOut, ok := ledger.AsDelegationOutput(owid.Output, owid.ID); ok {
					a.settleTo = entryFromOutput(&dOut)
				}
			}
			// settleTo == nil here means void (carrying milestone orphaned)
		} else {
			if present {
				a.confirm = true
			} else {
				a.drop = true
			}
		}
		actions = append(actions, a)
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()
	for _, a := range actions {
		e := p.entries[a.chainID]
		if e == nil {
			continue
		}
		if a.pending {
			// skip if a newer milestone superseded the pending we reconciled
			if e.pending == nil || e.pending.successorID != a.checkID {
				continue
			}
			if a.settleTo != nil {
				a.settleTo.addedSlot = e.addedSlot
				p.entries[a.chainID] = a.settleTo
			} else {
				e.pending = nil // void
			}
			continue
		}
		// unconfirmed Undef enrollment: only act if still the same untouched entry
		if e.pending != nil || e.confirmed || e.outputID != a.checkID {
			continue
		}
		if a.drop {
			delete(p.entries, a.chainID)
		} else if a.confirm {
			e.confirmed = true
		}
	}
}

// Snapshot returns the freezable candidates and the per-epoch frozen-amount load
// vector D (settled Frozen entries at their lastFrozenEpoch, pending freezes at
// their untilEpoch). currentSlot is the slot of the transaction being built.
func (p *DelegationPool) Snapshot(currentSlot uint32) (candidates []Candidate, loadByEpoch map[uint32]uint64) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	loadByEpoch = make(map[uint32]uint64)
	for cid, e := range p.entries {
		switch {
		case e.pending != nil && e.pending.kind == transitionFreeze:
			loadByEpoch[e.pending.untilEpoch] += e.pending.amount
		case e.pending == nil && e.state == ledger.DelegateLockStateFrozen:
			loadByEpoch[e.lastFrozenEpoch] += e.amount
		}
		if e.pending == nil && e.state != ledger.DelegateLockStateOnHold && currentSlot >= e.freezableFromSlot {
			candidates = append(candidates, Candidate{
				ChainID:         cid,
				OutputID:        e.outputID,
				Amount:          e.amount,
				State:           e.state,
				MaxFrozenEpochs: e.maxFrozenEpochs,
			})
		}
	}
	return
}

func entryFromOutput(o *ledger.DelegationOutput) *delegationEntry {
	return &delegationEntry{
		outputID:          o.ID,
		amount:            o.Output.TokenBalance(),
		state:             o.State,
		lastFrozenEpoch:   o.LastFrozenEpoch,
		maxFrozenEpochs:   o.MaxFrozenEpochs,
		freezableFromSlot: freezableFromSlot(o),
		addedSlot:         o.ID.Slot(),
		confirmed:         true,
	}
}

// freezableFromSlot is the earliest slot at which IsUnlockableByTargetForFreezing
// becomes true (and stays true until the next transition):
//   - Undef: one slot after creation (target unlock requires output strictly older).
//   - Frozen: after the frozen window AND the ledger-enforced safe-revocation window.
//   - OnHold: never (until master/sequencer changes it).
func freezableFromSlot(o *ledger.DelegationOutput) uint32 {
	switch o.State {
	case ledger.DelegateLockStateFrozen:
		// UnfreezeSlot = lastSlotInEpoch+1; the safe-revocation window spans
		// [lastSlot+1, lastSlot+SafeRevocationSlots], so freezable from
		// lastSlot+SafeRevocationSlots+1 = UnfreezeSlot + SafeRevocationSlots.
		return o.UnfreezeSlot() + ledger.L(o.ID.Slot()).SafeRevocationSlots
	case ledger.DelegateLockStateOnHold:
		return base.MaxSlot
	default:
		return o.ID.Slot() + 1
	}
}
