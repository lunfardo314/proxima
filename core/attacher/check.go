package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func (a *milestoneAttacher) checkConsistencyBeforeWrapUp() error {
	err := a._checkConsistencyBeforeFinalization()
	if err != nil {
		err = fmt.Errorf("checkConsistencyBeforeWrapUp in attacher %s: %v\n---- attacher lines ----\n%s", a.name, err, a.dumpLinesString("       "))
	}
	return err
}

func (a *milestoneAttacher) _checkConsistencyBeforeFinalization() (err error) {
	if a.containsUndefinedExcept(a.vid) {
		return fmt.Errorf("still contains undefined vertices")
	}
	// should be at least one rooted output ( ledger baselineCoverage must be > 0)
	if len(a.rooted) == 0 {
		return fmt.Errorf("at least one rooted output is expected")
	}
	for vid := range a.rooted {
		if !a.isKnownDefined(vid) {
			return fmt.Errorf("all rooted must be defined. This one is not: %s", vid.IDShortString())
		}
	}
	if len(a.vertices) == 0 {
		return fmt.Errorf("vertices is empty")
	}
	sumRooted := uint64(0)
	for vid, consumed := range a.rooted {
		var o *ledger.Output
		consumed.ForEach(func(idx byte) bool {
			o, err = vid.OutputAt(idx)
			if err != nil {
				return false
			}
			sumRooted += o.Amount()
			return true
		})
	}
	if err != nil {
		return
	}
	if sumRooted == 0 {
		err = fmt.Errorf("sum of rooted cannot be 0")
		return
	}
	for vid, flags := range a.vertices {
		if !flags.FlagsUp(flagAttachedVertexKnown) {
			return fmt.Errorf("wrong flags 1 %08b in %s", flags, vid.IDShortString())
		}
		if !flags.FlagsUp(flagAttachedVertexDefined) && vid != a.vid {
			return fmt.Errorf("wrong flags 2 %08b in %s", flags, vid.IDShortString())
		}
		if vid == a.vid {
			if vid.GetTxStatus() == vertex.Bad {
				return fmt.Errorf("vertex %s is BAD", vid.IDShortString())
			}
			continue
		}
		status := vid.GetTxStatus()
		if status == vertex.Bad {
			return fmt.Errorf("BAD vertex in the past cone: %s", vid.IDShortString())
		}
		// transaction can be undefined in the past cone (virtual, non-sequencer etc)

		if a.isKnownRooted(vid) {
			// do not check dependencies if transaction is rooted
			continue
		}
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			missingInputs, missingEndorsements := v.NumMissingInputs()
			if missingInputs+missingEndorsements > 0 {
				err = fmt.Errorf("not all dependencies solid in %s\n      missing inputs: %d\n      missing endorsements: %d,\n      missing input txs: [%s]",
					vid.IDShortString(), missingInputs, missingEndorsements, v.MissingInputTxIDString())
			}
		}})
		if err != nil {
			return
		}
	}

	a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		if err = a._checkMonotonicityOfInputTransactions(v); err != nil {
			return
		}
		err = a._checkMonotonicityOfEndorsements(v)
	}})
	return
}

func (a *milestoneAttacher) _checkMonotonicityOfEndorsements(v *vertex.Vertex) (err error) {
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if vidEndorsed.IsBranchTransaction() {
			return true
		}
		lc := vidEndorsed.GetLedgerCoverageP()
		if lc == nil {
			err = fmt.Errorf("accumulatedCoverage not set in the endorsed %s", vidEndorsed.IDShortString())
			return false
		}
		if a.accumulatedCoverage < *lc {
			diff := *lc - a.accumulatedCoverage
			err = fmt.Errorf("accumulatedCoverage should not decrease along endorsement.\nGot: delta(%s) at %s <= delta(%s) in %s. diff: %s",
				util.Th(a.accumulatedCoverage), a.vid.Timestamp().String(), util.Th(*lc), vidEndorsed.IDShortString(), util.Th(diff))
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
			err = fmt.Errorf("accumulatedCoverage not set in the input tx %s", vidInp.IDShortString())
			return false
		}
		if a.accumulatedCoverage < *lc {
			diff := *lc - a.accumulatedCoverage
			err = fmt.Errorf("accumulatedCoverage should not decrease along consumed transactions on the same slot.\nGot: delta(%s) at %s <= delta(%s) in %s. diff: %s",
				util.Th(a.accumulatedCoverage), a.vid.Timestamp().String(), util.Th(*lc), vidInp.IDShortString(), util.Th(diff))
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
const enforceConsistencyWithTxMetadata = true

// checkConsistencyWithMetadata does not check root
func (a *milestoneAttacher) checkConsistencyWithMetadata() {
	if a.metadata == nil {
		return
	}
	var err error
	switch {
	case a.metadata.LedgerCoverage != nil && *a.metadata.LedgerCoverage != a.accumulatedCoverage:
		err = fmt.Errorf("checkConsistencyWithMetadata %s: major inconsistency: computed accumulatedCoverage (%s) not equal to the accumulatedCoverage provided in the metadata (%s). Diff=%s",
			a.vid.IDShortString(), util.Th(a.accumulatedCoverage), util.Th(*a.metadata.LedgerCoverage),
			util.Th(int64(a.accumulatedCoverage)-int64(*a.metadata.LedgerCoverage)))
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
