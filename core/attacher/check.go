package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
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
		memdag.SavePastConeFromTxStore(a.vid.ID, a.TxBytesStore(), a.vid.Slot()-3, "inconsist_"+a.vid.ID.AsFileNameShort()+".gv")
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

func (a *milestoneAttacher) calculatedMetadata() *txmetadata.TransactionMetadata {
	return &txmetadata.TransactionMetadata{
		StateRoot:      a.finals.root,
		LedgerCoverage: util.Ref(a.LedgerCoverage()),
		SlotInflation:  util.Ref(a.slotInflation),
		Supply:         util.Ref(a.baselineSupply + a.slotInflation),
	}
}

// checkConsistencyWithMetadata check but not enforces
func (a *milestoneAttacher) checkConsistencyWithMetadata() {
	calcMeta := a.calculatedMetadata()
	if !a.metadata.IsConsistentWith(calcMeta) {
		a.Log().Errorf("inconsistency in metadata of %s (source seq: %s):\n   calculated metadata: %s\n   provided metadata: %s",
			a.vid.IDShortString(), a.vid.SequencerID.Load().StringShort(), calcMeta.String(), a.metadata.String())
	}
}
