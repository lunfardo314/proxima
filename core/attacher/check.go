package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
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
		memdag.SavePastConeFromTxStore(a.vid.ID(), a.TxBytesStore(), a.vid.Slot()-3, "inconsist_"+util.Ref(a.vid.ID()).AsFileNameShort()+".gv")
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
			err = fmt.Errorf("ledger coverage not set in the endorsed %s", vidEndorsed.IDShortString())
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
		if !vidInp.IsSequencerMilestone() || vidInp.IsBranchTransaction() || v.Tx.Slot() != vidInp.Slot() {
			// checking sequencer, non-branch inputs on the same slot
			return true
		}
		lc := vidInp.GetLedgerCoverageP()
		if lc == nil {
			err = fmt.Errorf("ledger coverage not set in the input tx %s", vidInp.IDShortString())
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

// consistentCoveragesFromMetadata checks consistency between calculated and provided the ledger coverage
// If the transaction is close to the snapshot, calculated coverage usually is less than provided
func consistentCoveragesFromMetadata(calculated, provided *uint64, slotsFromSnapshot uint32) bool {
	if calculated == nil || provided == nil || *calculated == *provided {
		return true
	}
	if slotsFromSnapshot < 64 {
		return *calculated <= *provided
	}
	return false
}

// checkConsistencyWithMetadata checks but not enforces
func (a *milestoneAttacher) checkConsistencyWithMetadata() {
	if a.providedMetadata == nil {
		return
	}
	msg := ""
	slotsFromSnapshot := uint32(a.vid.Slot() - a.Branches().SnapshotSlot())
	if !consistentCoveragesFromMetadata(a.finals.TransactionMetadata.LedgerCoverage, a.providedMetadata.LedgerCoverage, slotsFromSnapshot) {
		msg = fmt.Sprintf("inconsistent ledger coverage in tx metadata (slots from snapshot %d)", slotsFromSnapshot)
	} else if !a.providedMetadata.IsConsistentWithExceptCoverage(&a.finals.TransactionMetadata) {
		msg = fmt.Sprintf("inconsistency in tx metadata")
	}
	if msg != "" {
		a.Log().Warnf("%s of tx %s (source seq: %s, '%s'):\n   calculated metadata: %s\n   provided metadata: %s",
			msg, a.vid.IDShortString(), a.vid.SequencerID.Load().StringShort(), a.vid.SequencerName(),
			a.finals.TransactionMetadata.String(), a.providedMetadata.String())
	}
}
