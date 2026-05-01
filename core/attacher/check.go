package attacher

import (
	"bytes"
	"fmt"

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
		delta, _ := a.CoverageDelta()
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
// refactor §6 D1, §9.6). Mismatch on baselineRoot is a hard error — it is
// trivially deterministic from the predecessor branch's trie root and any
// disagreement is an out-of-consensus condition that must escalate.
//
// The other aggregates (CoverageDelta / FrozenCoverage / SlotInflation /
// NumTransactions / TotalSupply / TotalCoverage) are also expected to agree
// in steady state, but proposer/attacher past-cone resolution timing can
// legitimately differ in transient cases (the proposer publishes before all
// endorsement past-cones are fully resolved; the milestone attacher walks
// deeper). We log mismatches loudly and TODO escalate to panic once the
// proposer-attacher view is reconciled.
func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func (a *milestoneAttacher) enforceStemValues(stemLock *ledger.StemLock) {
	a.Assertf(a.vid.IsBranchTransaction(), "enforceStemValues: branch tx expected")

	// baselineRoot: hard-deterministic when the stem carries one. A stem with
	// an all-zero BaselineRoot signals "not provided" (test infra path) — log
	// it but don't fail the branch, since the constraint only enforces
	// mustSize($8, 24). When the stem provides a non-zero root, mismatch with
	// the actual predecessor trie root is an out-of-consensus condition.
	if !isAllZero(stemLock.BaselineRoot) {
		if bd := a.Branches().Get(a.finals.baseline); bd != nil && bd.Root != nil {
			want := bd.Root.Bytes()
			a.Assertf(bytes.Equal(stemLock.BaselineRoot, want),
				"enforceStemValues: stemLock.BaselineRoot != predecessor branch's trie root for %s\n  stem: %x\n  want: %x",
				a.vid.IDShortString(), stemLock.BaselineRoot, want)
		}
	} else {
		a.Log().Warnf("enforceStemValues[%s]: stem carries empty BaselineRoot (test or pre-Phase-D path)", a.vid.IDShortString())
	}

	delta, frozen := a.CoverageDelta()
	slotInflation := a.SlotInflation()
	supply := a.BaselineSupply() + slotInflation
	totalCov := a.FinalLedgerCoverage(a.vid.Timestamp(), delta)
	numTx := uint32(a.pastCone.NumNewTransactions())

	mismatch := func(name string, computed, onStem uint64) {
		if computed != onStem {
			a.Log().Warnf("enforceStemValues[%s]: %s mismatch — computed %s, on stem %s",
				a.vid.IDShortString(), name, util.Th(computed), util.Th(onStem))
		}
	}
	mismatch("CoverageDelta", delta, stemLock.CoverageDelta)
	mismatch("FrozenCoverage", frozen, stemLock.FrozenCoverage)
	mismatch("SlotInflation", slotInflation, stemLock.SlotInflation)
	mismatch("TotalSupply", supply, stemLock.TotalSupply)
	mismatch("TotalCoverage", totalCov, stemLock.TotalCoverage)
	if uint64(numTx) != uint64(stemLock.NumTransactions) {
		a.Log().Warnf("enforceStemValues[%s]: NumTransactions mismatch — computed %d, on stem %d",
			a.vid.IDShortString(), numTx, stemLock.NumTransactions)
	}
}
