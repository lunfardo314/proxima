package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func (a *milestoneAttacher) checkConsistencyBeforeWrapUp() (err error) {
	if a.vid.GetTxStatus() == vertex.Bad {
		return fmt.Errorf("checkConsistencyBeforeWrapUp: vertex %s is BAD", a.vid.IDShortString())
	}
	if a.SnapshotBranchID().Timestamp().AfterOrEqual(a.vid.Timestamp()) {
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
		lc := vidEndorsed.GetLedgerCoverageP()
		if lc == nil {
			err = fmt.Errorf("ledger coverage not set in the endorsed %s", vidEndorsed.IDShortString())
			return false
		}
		lcCalc := a.LedgerCoverage()
		if lcCalc < *lc {
			diff := *lc - lcCalc
			err = fmt.Errorf("ledger coverage should not decrease along endorsement.\nGot: delta(%s) at %s <= delta(%s) in %s. diff: %s",
				util.Th(lcCalc), a.vid.Timestamp().String(), util.Th(*lc), vidEndorsed.IDShortString(), util.Th(diff))
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
		if !vidInp.IsSequencerMilestone() || vidInp.IsBranchTransaction() || v.Tx.Slot() != vidInp.Slot() {
			// checking sequencer, non-branch inputs on the same slot
			return true
		}
		lc := vidInp.GetLedgerCoverageP()
		if lc == nil {
			err = fmt.Errorf("ledger coverage not set in the input tx %s", vidInp.IDShortString())
			return false
		}
		lcCalc := a.LedgerCoverage()
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

// enforceConsistencyWithTxMetadata :
// if true, node is crashed immediately upon inconsistency with provided transaction metadata
// if false, an error is reported
// The transaction metadata is optionally provided together with the sequencer transaction bytes by other nodes
// or by the tx store, therefore is not trust-less.
// A malicious node could crash other peers by sending inconsistent metadata,
// therefore in the production environment enforceConsistencyWithTxMetadata should be false
// and the connection with the malicious peer should be immediately severed
const enforceConsistencyWithTxMetadata = false

// checkConsistencyWithMetadata does not check root
func (a *milestoneAttacher) checkConsistencyWithMetadata() {
	if a.metadata == nil {
		return
	}
	var err error
	lcCalc := a.LedgerCoverage()
	switch {
	case a.metadata.LedgerCoverage != nil && *a.metadata.LedgerCoverage != lcCalc:
		err = fmt.Errorf("checkConsistencyWithMetadata %s: major inconsistency:\n   computed coverage (%s) not equal to the\n   ledger coverage provided in the metadata (%s).\n   Diff=%s",
			a.vid.IDShortString(), util.Th(lcCalc), util.Th(*a.metadata.LedgerCoverage),
			util.Th(int64(lcCalc)-int64(*a.metadata.LedgerCoverage)))
	case a.metadata.SlotInflation != nil && *a.metadata.SlotInflation != a.slotInflation:
		err = fmt.Errorf("checkConsistencyWithMetadata %s: major inconsistency: computed slot inflation (%s) not equal to the slot inflation provided in the metadata (%s)",
			a.vid.IDShortString(), util.Th(a.slotInflation), util.Th(*a.metadata.SlotInflation))
	case a.metadata.Supply != nil && *a.metadata.Supply != a.baselineSupply+a.slotInflation:
		err = fmt.Errorf("checkConsistencyWithMetadata %s: major inconsistency: computed supply (%s) not equal to the supply provided in the metadata (%s)",
			a.vid.IDShortString(), util.Th(a.baselineSupply+a.slotInflation), util.Th(*a.metadata.Supply))
	}
	if err == nil {
		return
	}
	if enforceConsistencyWithTxMetadata {
		//go memdag.SaveGraphPastCone(a.vid, "checkConsistencyWithMetadata.gv")
		//time.Sleep(10 * time.Second)
		//
		err = fmt.Errorf("%v\n================\n%s", err, a.pastCone.Lines("        ").Join("\n"))
		a.Log().Fatal(err)
	} else {
		a.Log().Error(err)
	}
}

func (a *milestoneAttacher) checkStateRootConsistentWithMetadata() {
	if a.metadata == nil || util.IsNil(a.metadata.StateRoot) {
		return
	}
	if !ledger.CommitmentModel.EqualCommitments(a.finals.root, a.metadata.StateRoot) {
		err := fmt.Errorf("commitBranch %s: major inconsistency: state root not equal to the state root provided in metadata", a.vid.IDShortString())
		if enforceConsistencyWithTxMetadata {
			a.Log().Fatal(err)
		} else {
			a.Log().Error(err)
		}
	}
}
