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
		// lastApplied is the most recent own milestone whose transitions were applied.
		// ApplyMilestone walks the chain back to it so milestones skipped by the
		// latest-only milestoneWatcher are not missed.
		lastApplied base.TransactionID
	}

	// chainTransition pairs a delegation's ChainID with the pending transition
	// derived from one own milestone.
	chainTransition struct {
		chainID base.ChainID
		pt      pendingTransition
	}
)

// maxApplyChainDepth bounds the own-milestone chain walk in ApplyMilestone (the
// walk normally stops at lastApplied or the latest branch long before this).
const maxApplyChainDepth = 256

const (
	transitionFreeze transitionKind = iota
	transitionUnfreeze
)

const (
	reconcilePeriod = time.Second

	// discoveryPeriod is the LRB rescan cadence. Deliberately much slower than
	// reconcilePeriod: the scan iterates every output indexed under the
	// sequencer's account (tag-alongs included, since they share the account
	// bytes), and a delegation is frozen for many epochs, so a delay of this
	// order before enrolling a missed one is immaterial.
	discoveryPeriod = 30 * time.Second
)

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
	ret.discoverFromLRB()
	// Delegation outputs index under their Target account, whose bytes equal the
	// chain-lock account bytes of the same chain ID. ListenToControllerAccount
	// fires for any produced output whose index-values contain these bytes; the
	// callback filters to delegations actually targeting this sequencer.
	env.ListenToControllerAccount(ledger.ChainLockFromChainID(seqID), ret.onNewOutput)

	env.RepeatInBackground(env.SequencerName()+"_delegationPoolReconcile", reconcilePeriod, func() bool {
		ret.Reconcile()
		return true
	}, true)

	env.RepeatInBackground(env.SequencerName()+"_delegationPoolDiscover", discoveryPeriod, func() bool {
		ret.discoverFromLRB()
		return true
	}, true)
	env.Tracef(TraceTag, "[%s] delegation pool started, %d delegations bootstrapped", env.SequencerName(), len(ret.entries))
	return ret, nil
}

// discoverFromLRB enrolls delegations targeting this sequencer that are in the
// LRB but not yet in the pool. Best-effort: with no LRB established (very early
// startup) the pool stays empty and is filled by the listener or a later scan.
//
// Runs at startup and periodically. The periodic repeat is what makes discovery
// self-healing: the push listener gets one chance per delegation (its event is
// delivered asynchronously and the producing vertex may already be detached by
// then, especially for non-sequencer transactions), and Reconcile only settles
// entries the pool already knows. Without a rescan a delegation missed by the
// listener stays invisible — and therefore never frozen — until restart.
func (p *DelegationPool) discoverFromLRB() {
	lrb := p.Branches().FindLatestReliableBranch()
	if lrb == nil {
		return
	}
	rdr := multistate.MustNewSugaredReadableState(p.StateStore(), lrb.Root, 0)
	found := make(map[base.ChainID]*delegationEntry)
	rdr.IterateDelegatedOutputs(p.target, func(o *ledger.DelegationOutput) bool {
		found[o.ChainID] = entryFromOutput(o)
		return true
	})
	if n := p.mergeDiscovered(found); n > 0 {
		p.Tracef(TraceTag, "[%s] discovered %d delegation(s) absent from the pool", p.SequencerName(), n)
	}
}

// mergeDiscovered adds only the entries the pool does not already know, and
// returns how many were added. Merge-only is essential: a known entry may carry
// a pending transition or a listener-added tentative state that the LRB does not
// reflect yet, and overwriting it would lose that. At startup the pool is empty,
// so the merge is a plain fill.
func (p *DelegationPool) mergeDiscovered(found map[base.ChainID]*delegationEntry) int {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	added := 0
	for cid, e := range found {
		if _, known := p.entries[cid]; known {
			continue
		}
		p.entries[cid] = e
		added++
	}
	return added
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

// ApplyMilestone records the sequencer's own freeze / unfreeze transitions as
// tentative (pending) until reconciled against the LRB. milestoneWatcher reports
// only the LATEST own milestone, so when the chain advances by more than one
// between polls the intermediate milestones are skipped. We therefore walk the
// own-milestone chain from vid back to the last-applied milestone (or the latest
// branch, since freezes live only in non-branch milestones after it) and apply
// every milestone's transitions, not just vid's.
func (p *DelegationPool) ApplyMilestone(vid *vertex.WrappedTx) {
	p.mutex.RLock()
	lastApplied := p.lastApplied
	p.mutex.RUnlock()

	// collect transitions newest-first along the chain back to the last-applied
	// milestone. We must NOT stop at a branch: onMilestoneConfirmed is frequently
	// called with a branch as the latest milestone, and the freezes live in the
	// non-branch milestones BEFORE it (its sequencer predecessors). Stopping at the
	// branch would miss them. The lastApplied bound (set every call) keeps the walk
	// short in steady state; the depth cap bounds the first/post-switch walk.
	var all []chainTransition
	ms := vid
	for depth := 0; ms != nil && depth < maxApplyChainDepth; depth++ {
		if ms.ID() == lastApplied {
			break
		}
		trs, pred := p.milestoneTransitions(ms)
		all = append(all, trs...)
		ms = pred
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()
	// apply oldest-first (collected newest-first) so the most recent transition for
	// a given delegation wins.
	for k := len(all) - 1; k >= 0; k-- {
		t := all[k]
		e := p.entries[t.chainID]
		if e == nil {
			// freezing a delegation not yet in the pool (discovery raced). Create a
			// bare entry; Reconcile fills confirmed fields when it settles.
			e = &delegationEntry{addedSlot: t.pt.slot}
			p.entries[t.chainID] = e
		}
		pt := t.pt
		e.pending = &pt
	}
	p.lastApplied = vid.ID()
}

// milestoneTransitions extracts this milestone's freeze/unfreeze transitions and
// returns its own (sequencer) predecessor for the chain walk.
func (p *DelegationPool) milestoneTransitions(ms *vertex.WrappedTx) (transitions []chainTransition, pred *vertex.WrappedTx) {
	slot := ms.Slot()
	ms.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		v.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
			dOut, ok := ledger.AsDelegationOutput(o, oid)
			if !ok || dOut.Target != p.target {
				return true
			}
			switch dOut.State {
			case ledger.DelegateLockStateFrozen:
				transitions = append(transitions, chainTransition{dOut.ChainID, pendingTransition{
					kind:        transitionFreeze,
					slot:        slot,
					untilEpoch:  dOut.LastFrozenEpoch,
					amount:      dOut.Output.TokenBalance(),
					successorID: oid,
				}})
			case ledger.DelegateLockStateOnHold:
				transitions = append(transitions, chainTransition{dOut.ChainID, pendingTransition{
					kind:        transitionUnfreeze,
					slot:        slot,
					successorID: oid,
				}})
			}
			return true
		})
		// own (sequencer-chain) predecessor for the walk
		if seqData := v.SequencerTransactionData(); seqData != nil {
			predIdx := seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex
			if predIdx != 0xff && int(predIdx) < len(v.Inputs) {
				pred = v.Inputs[predIdx]
			}
		}
	}})
	return
}

// Reconcile aligns tentative (pending) transitions and unconfirmed enrollments
// with the LRB by looking each delegation up by ChainID (a targeted per-entry
// read, not an O(all) scan). It adopts the LRB's authoritative state when the
// transition has committed, and crucially KEEPS a pending transition while the
// delegation is still Undef in the LRB — a freeze lives in a non-branch milestone
// and is not in the committed state until the next branch, so checking output
// presence too early would falsely void it (the original bug: voided freezes
// reverted to Undef, dropping them from the load vector and causing pile-ups).
// Driven by a background timer.
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
		chainID base.ChainID
		guardID base.OutputID // successorID (pending) / outputID (undef) — TOCTOU guard
		pending bool
		stale   bool // old enough to drop if still absent from the LRB
	}
	var suspects []suspect
	p.mutex.RLock()
	for cid, e := range p.entries {
		switch {
		case e.pending != nil:
			suspects = append(suspects, suspect{cid, e.pending.successorID, true,
				e.pending.slot+uint32(ttlDelegationSlots) < lrbSlot})
		case !e.confirmed && e.state == ledger.DelegateLockStateUndef &&
			e.addedSlot+uint32(ttlDelegationSlots) < lrbSlot:
			suspects = append(suspects, suspect{cid, e.outputID, false, true})
		}
	}
	p.mutex.RUnlock()
	if len(suspects) == 0 {
		return
	}

	type action struct {
		chainID  base.ChainID
		guardID  base.OutputID
		pending  bool
		settleTo *delegationEntry // settle the entry to this LRB-authoritative state
		drop     bool
		confirm  bool
		// none set => leave as-is (transition not yet committed to the LRB)
	}
	actions := make([]action, 0, len(suspects))
	for _, s := range suspects {
		a := action{chainID: s.chainID, guardID: s.guardID, pending: s.pending}
		o, err := rdr.GetChainOutputWithChainID(s.chainID)
		dOut, isDlg := ledger.DelegationOutput{}, false
		if err == nil {
			dOut, isDlg = ledger.DelegationOutputFromOutputWithChainID(&o)
		}
		if s.pending {
			switch {
			case isDlg && dOut.State != ledger.DelegateLockStateUndef:
				// committed (Frozen or OnHold) -> adopt LRB truth
				a.settleTo = entryFromOutput(&dOut)
			case isDlg:
				// still Undef in the LRB -> freeze not committed yet; keep pending
			default:
				// absent from the LRB -> drop only if aged out (truly orphaned/withdrawn)
				a.drop = s.stale
			}
		} else if isDlg {
			a.confirm = true
		} else {
			a.drop = true
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
			if e.pending == nil || e.pending.successorID != a.guardID {
				continue
			}
			switch {
			case a.settleTo != nil:
				a.settleTo.addedSlot = e.addedSlot
				p.entries[a.chainID] = a.settleTo
			case a.drop:
				delete(p.entries, a.chainID)
			}
			continue
		}
		// unconfirmed Undef enrollment: only act if still the same untouched entry
		if e.pending != nil || e.confirmed || e.outputID != a.guardID {
			continue
		}
		if a.drop {
			delete(p.entries, a.chainID)
		} else if a.confirm {
			e.confirmed = true
		}
	}
}

// Snapshot returns the freezable candidates, the per-epoch frozen-amount load vector D
// (settled Frozen entries at their lastFrozenEpoch, pending freezes at their untilEpoch),
// and countByEpoch — the number of frozen delegations per epoch (same entries, counted
// rather than amount-weighted), used for the per-epoch max-frozen-delegations cap.
// currentSlot is the slot of the transaction being built.
func (p *DelegationPool) Snapshot(currentSlot uint32) (candidates []Candidate, loadByEpoch, countByEpoch map[uint32]uint64) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	loadByEpoch = make(map[uint32]uint64)
	countByEpoch = make(map[uint32]uint64)
	for cid, e := range p.entries {
		switch {
		case e.pending != nil && e.pending.kind == transitionFreeze:
			loadByEpoch[e.pending.untilEpoch] += e.pending.amount
			countByEpoch[e.pending.untilEpoch]++
		case e.pending == nil && e.state == ledger.DelegateLockStateFrozen:
			loadByEpoch[e.lastFrozenEpoch] += e.amount
			countByEpoch[e.lastFrozenEpoch]++
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
