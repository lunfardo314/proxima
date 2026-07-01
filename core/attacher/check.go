package attacher

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func (a *milestoneAttacher) checkConsistencyBeforeWrapUp() (err error) {
	if a.vid.GetTxStatus() == vertex.Bad {
		return fmt.Errorf("checkConsistencyBeforeWrapUp: vertex %s is BAD", a.vid.IDShortString())
	}
	brid := a.Branches().SnapshotBranchID()
	if brid.Timestamp().AfterOrEqual(a.vid.Timestamp()) {
		// attacher is before the snapshot -> no need to check inputs, it must be in the state anyway
		return nil
	}
	// During snapshot-restore + forward-sync a milestone is re-attached against a
	// foreign baseline (the forward-sync anchor) whose slot is >= the milestone's
	// own. Coverage recomputed from that foreign-baseline cone is meaningless — the
	// same reason enforceSeqCoverageDelta skips its cross-check (wrapup.go) — so the
	// monotonicity comparison below is invalid and must be skipped, not FATALed.
	// Real-time attachment always has baseline slot < milestone slot, so it is
	// unaffected; this only relaxes the historical sync re-attach path.
	if a.pastCone.GetBaseline().Slot() >= a.vid.Slot() {
		return nil
	}
	a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		if err = a._checkMonotonicityOfInputTransactions(v); err != nil {
			return
		}
		err = a._checkMonotonicityOfEndorsements(v)
	}})
	if err != nil {
		err = fmt.Errorf("checkConsistencyBeforeWrapUp in attacher %s: %v\n---- attacher lines ----\n%s", a.name, err, a.dumpLinesString("       "))
	}
	return err
}

func (a *milestoneAttacher) _checkMonotonicityOfEndorsements(v *vertex.Vertex) (err error) {
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if vidEndorsed.IsBranchTransaction() {
			return true
		}
		lcEnd := vidEndorsed.GetLedgerCoverageP()
		if lcEnd == nil {
			// Endorsed vid was reattached during this attacher's lifetime — its coverage
			// was cleared and the new attacher hasn't restored it yet. Bail this milestone
			// without marking the (still-fine) consumer Bad. See ErrAttacherTransientStaleState.
			err = fmt.Errorf("%w: endorsed %s coverage cleared (reattached)", ErrAttacherTransientStaleState, vidEndorsed.IDShortString())
			return false
		}
		lcCalc := a.FinalLedgerCoverage(a.vid.Timestamp())
		if lcCalc < *lcEnd {
			diff := *lcEnd - lcCalc
			err = fmt.Errorf("ledger coverage should not decrease along endorsement.\nGot: LC(%s) at %s <= LC(%s) in %s. diff: %s",
				util.Th(lcCalc), a.vid.Timestamp().String(), util.Th(*lcEnd), vidEndorsed.IDShortString(), util.Th(diff))
			return false
		}
		return true
	})
	return
}

func (a *milestoneAttacher) _checkMonotonicityOfInputTransactions(v *vertex.Vertex) (err error) {
	setOfInputTransactions := v.SetOfInputTransactions()
	util.Assertf(len(setOfInputTransactions) > 0, "len(setOfInputTransactions)>0")

	setOfInputTransactions.ForEach(func(vidInp *vertex.WrappedTx) bool {
		if !vidInp.IsSequencerTransaction() || vidInp.IsBranchTransaction() || v.Slot() != vidInp.Slot() {
			// checking sequencer, non-branch inputs on the same slot
			return true
		}
		lc := vidInp.GetLedgerCoverageP()
		if lc == nil {
			// Input was reattached during this attacher's lifetime — its coverage
			// was cleared and the new attacher hasn't restored it yet. Bail this
			// milestone without marking the (still-fine) consumer Bad. See
			// ErrAttacherTransientStaleState.
			err = fmt.Errorf("%w: input %s coverage cleared (reattached)", ErrAttacherTransientStaleState, vidInp.IDShortString())
			return false
		}
		delta := a.CoverageDelta()
		lcCalc := a.FinalLedgerCoverage(a.vid.Timestamp(), delta)
		if lcCalc < *lc {
			diff := *lc - lcCalc
			err = fmt.Errorf("ledger coverage should not decrease along consumed transactions on the same slot.\nGot: delta(%s) at %s <= delta(%s) in %s. diff: %s",
				util.Th(lcCalc), a.vid.Timestamp().String(), util.Th(*lc), vidInp.IDShortString(), util.Th(diff))
			return false
		}
		return true
	})
	return
}

// enforceStemValues compares the deterministic values declared on the produced stem against what
// this attacher computed from its past cone (metadata-refactor §6 D1, §9.6). Any mismatch rejects
// the branch and logs the consolidated computed-vs-declared oracle block. Two of these values are
// hard, snapshot-independent invariants that additionally HALT the node when they diverge on a
// real-time attachment (baseline strictly older than the branch, so the node's recomputation is
// authoritative): the resulting trie root (BaselineRoot of the successor) and TotalSupply. A
// mismatch there is genuine non-determinism — the committed state no longer agrees with network
// consensus — and cannot be produced by the transient detach/reattach race, so halting is safe. A
// root divergence additionally dumps the full mutation set so the divergent trie leaf can be
// diffed. Every other value (TotalCoverage, SlotInflation, FrozenCoverage, the counts) only warns
// and rejects — those can be perturbed by the reattach race and must never be fatal.
//
// Against a foreign/newer baseline (baseline slot >= the branch's own, a snapshot-restore +
// forward-sync re-attach) the recomputed values are meaningless, so nothing halts — only reject.
//
// BaselineRoot is checked only when the predecessor branch is known locally (skipped for a
// pre-snapshot / genesis baseline — nothing to compare against).
func (a *milestoneAttacher) enforceStemValues(stemLock *ledger.StemLock, oracleData *ledger.OracleData, muts *multistate.Mutations) error {
	a.Assertf(a.vid.IsBranchTransaction(), "enforceStemValues: branch tx expected")

	delta := a.CoverageDelta()
	slotInflation := a.SlotInflation()
	supply := a.BaselineSupply() + slotInflation
	totalCov := a.FinalLedgerCoverage(a.vid.Timestamp(), delta)
	// Single pass over the past cone for the three OracleData count aggregates.
	numTx, numSeqTx, numSeq := a.pastCone.NumNewTransactionStats()
	// FrozenCoverage is the cumulative total of tokens frozen by delegations across all
	// sequencers, accumulated like supply: baseline value plus this slot's signed delta.
	frozenDelta := a.SequencerFrozenCoverageDelta()
	frozen := int64(a.BaselineFrozenCoverage()) + frozenDelta

	// BaselineRoot lives on the unconstrained OracleData tuple. Known-locally only.
	var baselineRootMismatch bool
	var localBaselineRoot []byte
	if bd := a.Branches().Get(a.finals.baseline); bd != nil && bd.Root != nil {
		localBaselineRoot = bd.Root.Bytes()
		baselineRootMismatch = !bytes.Equal(oracleData.BaselineRoot, localBaselineRoot)
	}

	var supplyMismatch bool
	var mismatches []string
	add := func(name string, computed, declared any) {
		mismatches = append(mismatches, fmt.Sprintf("%s(computed=%v declared=%v)", name, computed, declared))
	}
	if baselineRootMismatch {
		add("BaselineRoot", fmt.Sprintf("%x", localBaselineRoot), fmt.Sprintf("%x", oracleData.BaselineRoot))
	}
	// Safe-arithmetic sanity: the per-slot change and the accumulated total must both stay
	// within total supply (frozen tokens are a subset of supply).
	if frozenDelta > int64(supply) || frozenDelta < -int64(supply) || frozen < 0 || uint64(frozen) > supply {
		add("FrozenCoverageRange", fmt.Sprintf("delta=%d acc=%d supply=%s", frozenDelta, frozen, util.Th(supply)), util.Th(oracleData.FrozenCoverage))
	}
	if uint64(frozen) != oracleData.FrozenCoverage {
		add("FrozenCoverage", util.Th(frozen), util.Th(oracleData.FrozenCoverage))
	}
	if slotInflation != stemLock.SlotInflation {
		add("SlotInflation", util.Th(slotInflation), util.Th(stemLock.SlotInflation))
	}
	if supply != stemLock.TotalSupply {
		supplyMismatch = true
		add("TotalSupply", util.Th(supply), util.Th(stemLock.TotalSupply))
	}
	if totalCov != stemLock.TotalCoverage {
		add("TotalCoverage", util.Th(totalCov), util.Th(stemLock.TotalCoverage))
	}
	if uint32(numTx) != oracleData.NumConfirmedTransactions {
		add("NumConfirmedTransactions", numTx, oracleData.NumConfirmedTransactions)
	}
	if uint32(numSeqTx) != oracleData.NumSeqTransactions {
		add("NumSeqTransactions", numSeqTx, oracleData.NumSeqTransactions)
	}
	if uint32(numSeq) != oracleData.NumSeq {
		add("NumSeq", numSeq, oracleData.NumSeq)
	}

	if len(mismatches) == 0 {
		return nil
	}

	// Consolidated oracle block: node-computed vs stem-declared, for every checked value.
	oracle := lines.New("    ")
	oracle.Add("branch:                   %s", a.vid.IDShortString())
	oracle.Add("baseline:                 %s", a.finals.baseline.StringShort())
	oracle.Add("TotalSupply:              computed=%s declared=%s", util.Th(supply), util.Th(stemLock.TotalSupply))
	oracle.Add("TotalCoverage:            computed=%s declared=%s", util.Th(totalCov), util.Th(stemLock.TotalCoverage))
	oracle.Add("SlotInflation:            computed=%s declared=%s", util.Th(slotInflation), util.Th(stemLock.SlotInflation))
	oracle.Add("FrozenCoverage:           computed=%s declared=%s", util.Th(uint64(frozen)), util.Th(oracleData.FrozenCoverage))
	oracle.Add("NumConfirmedTransactions: computed=%d declared=%d", numTx, oracleData.NumConfirmedTransactions)
	oracle.Add("NumSeqTransactions:       computed=%d declared=%d", numSeqTx, oracleData.NumSeqTransactions)
	oracle.Add("NumSeq:                   computed=%d declared=%d", numSeq, oracleData.NumSeq)
	oracle.Add("BaselineRoot:             computed=%x declared=%x", localBaselineRoot, oracleData.BaselineRoot)

	// Only the two hardest, snapshot-independent invariants — the resulting trie root
	// (BaselineRoot) and total supply — halt the node, and only on a real-time attachment
	// (baseline strictly older than the branch), where the node's recomputation is
	// authoritative. These cannot be perturbed by the transient detach/reattach race, so a
	// mismatch is genuine non-determinism: the committed state no longer agrees with network
	// consensus. Every other value (coverage, counts, inflation, frozen) only warns and rejects
	// the branch — those CAN be perturbed by the race and must never be fatal.
	hardHalt := (baselineRootMismatch || supplyMismatch) && a.finals.baseline.Slot() < a.vid.Slot()
	if hardHalt {
		// Root divergence additionally needs the full mutation set to locate the divergent leaf.
		if baselineRootMismatch && muts != nil {
			oracle.Add("---- mutations ----")
			oracle.Append(muts.Sort().Lines("      "))
		}
		a.Log().Errorf(">>>>>>>> **************** NON-DETERMINISM ****************** in branch %s [%s]\n%s",
			a.vid.IDShortString(), strings.Join(mismatches, "; "), oracle.String())
		a.GracefulShutdown(fmt.Sprintf("non-determinism committing branch %s: %s",
			a.vid.IDShortString(), strings.Join(mismatches, "; ")))
	} else {
		a.Log().Warnf("stem-value mismatch in branch %s [%s]\n%s",
			a.vid.IDShortString(), strings.Join(mismatches, "; "), oracle.String())
	}
	return fmt.Errorf("branch %s rejected: stem-value mismatch [%s]",
		a.vid.IDShortString(), strings.Join(mismatches, "; "))
}
