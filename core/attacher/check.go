package attacher

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
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

// enforceStemValues compares the deterministic values declared on the produced
// stem against what this attacher computed from its past cone (metadata-
// refactor §6 D1, §9.6). On any mismatch the branch transaction is invalidated
// (caller marks the vertex Bad) and a highlighted warning is logged. We do NOT
// panic: the produced stem comes from the wire and a peer-supplied malformed
// branch must not crash the node. The branch tx is simply rejected; future
// past cones referencing it will fail validation as expected.
//
// baselineRoot: when the predecessor branch is known locally, the stem's
// BaselineRoot must equal its trie root. When the predecessor is unknown
// (pre-snapshot baseline, genesis edge case) the check is skipped — there is
// nothing to compare against.
//
// Other aggregates (FrozenCoverage / SlotInflation /
// NumConfirmedTransactions / TotalSupply / TotalCoverage) are deterministic from the
// past cone. By the time we reach wrap-up the past cone is fully resolved, so
// any mismatch indicates either a malformed remote branch or a node bug.
// Either way the right action is to reject the branch.
func (a *milestoneAttacher) enforceStemValues(stemLock *ledger.StemLock, stemData *ledger.StemData) error {
	a.Assertf(a.vid.IsBranchTransaction(), "enforceStemValues: branch tx expected")

	var mismatches []string
	report := func(name string, computed, onStem any) {
		mismatches = append(mismatches,
			fmt.Sprintf("%s: computed=%v stem=%v", name, computed, onStem))
		a.Log().Errorf(">>>>>>>> **************** VIOLATION OF DETERMINISM ****************** stem-value mismatch in branch %s: %s computed=%v stem=%v",
			a.vid.IDShortString(), name, computed, onStem)
	}

	// BaselineRoot lives on the unconstrained StemData tuple now.
	if bd := a.Branches().Get(a.finals.baseline); bd != nil && bd.Root != nil {
		want := bd.Root.Bytes()
		if !bytes.Equal(stemData.BaselineRoot, want) {
			report("BaselineRoot",
				fmt.Sprintf("%x", want),
				fmt.Sprintf("%x", stemData.BaselineRoot))
		}
	}

	delta := a.CoverageDelta()
	slotInflation := a.SlotInflation()
	supply := a.BaselineSupply() + slotInflation
	totalCov := a.FinalLedgerCoverage(a.vid.Timestamp(), delta)
	// Single pass over the past cone for the three StemData count aggregates.
	numTx, numSeqTx, numSeq := a.pastCone.NumNewTransactionStats()

	// FrozenCoverage is the cumulative total of tokens frozen by delegations
	// across all sequencers, accumulated like supply: baseline value plus this
	// slot's signed delta (see claude/frozen_coverage.md).
	frozenDelta := a.SequencerFrozenCoverageDelta()
	frozen := int64(a.BaselineFrozenCoverage()) + frozenDelta

	// SlotInflation / TotalSupply / TotalCoverage stay on the constrained
	// stemLock; FrozenCoverage and the count aggregates are on the unconstrained
	// StemData tuple. coverageDelta moved to the sequencer constraint and is
	// cross-checked per-milestone in wrapUpAttacher (enforceSeqCoverageDelta).
	// Safe-arithmetic sanity: the per-slot change and the accumulated total must
	// both stay within total supply (frozen tokens are a subset of supply).
	if frozenDelta > int64(supply) || frozenDelta < -int64(supply) || frozen < 0 || uint64(frozen) > supply {
		report("FrozenCoverageRange",
			fmt.Sprintf("delta=%d acc=%d supply=%s", frozenDelta, frozen, util.Th(supply)),
			util.Th(stemData.FrozenCoverage))
	}
	if uint64(frozen) != stemData.FrozenCoverage {
		report("FrozenCoverage", util.Th(frozen), util.Th(stemData.FrozenCoverage))
	}
	if slotInflation != stemLock.SlotInflation {
		report("SlotInflation", util.Th(slotInflation), util.Th(stemLock.SlotInflation))
	}
	if supply != stemLock.TotalSupply {
		report("TotalSupply", util.Th(supply), util.Th(stemLock.TotalSupply))
	}
	if totalCov != stemLock.TotalCoverage {
		report("TotalCoverage", util.Th(totalCov), util.Th(stemLock.TotalCoverage))
	}
	if uint32(numTx) != stemData.NumConfirmedTransactions {
		report("NumConfirmedTransactions", numTx, stemData.NumConfirmedTransactions)
	}
	if uint32(numSeqTx) != stemData.NumSeqTransactions {
		report("NumSeqTransactions", numSeqTx, stemData.NumSeqTransactions)
	}
	if uint32(numSeq) != stemData.NumSeq {
		report("NumSeq", numSeq, stemData.NumSeq)
	}

	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("branch %s rejected: stem-value mismatch [%s]",
		a.vid.IDShortString(), strings.Join(mismatches, "; "))
}
