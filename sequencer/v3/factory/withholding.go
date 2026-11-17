package factory

import (
	"bytes"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// maxForkScan bounds how many milestones of one chain in one slot the fork clause reads.
const maxForkScan = 256

// withheldBranch tells whether the first move should pass over the branch because its chain
// predecessor was withheld (library upgrade 7, the sequencer's withholding policy): the chain
// entered the previous slot from an output two or more slots older than the branch; or, from
// the upgrade on, its final milestone reached this node only after the slot boundary; or the chain
// published, in the previous slot, a milestone committing to another bid than the final's. Each lets
// the branch bring in what the others had no chance to see: capital kept out of the slot, or a bid
// chosen after the branches. Where the DAG cannot tell, the branch is followed. Later in the slot
// such a lineage is followed as any other.
func withheldBranch(br *vertex.WrappedTx) (withheld bool, reason string) {
	slot := br.Slot()
	if !br.IsBranchTransaction() || slot == 0 {
		return false, ""
	}
	noReattach := func(base.TransactionID) *vertex.WrappedTx { return nil }
	final := br.SequencerPredecessor(noReattach)
	if final == nil {
		return false, ""
	}
	prevSlot := slot - 1

	// the chain must reach back to its own branch of the previous slot or to an output of the slot
	// before it: a sequencer whose branch lost cannot extend that branch on the winner's lineage and
	// re-enters from its own last milestone, which most chains do every slot. entry is the milestone
	// whose output the chain entered the previous slot from.
	var entry *vertex.WrappedTx
	for cur := final; ; {
		if cur.Slot() < prevSlot {
			if cur.Slot()+1 < prevSlot {
				return true, "the chain entered the previous slot from an output two or more slots back"
			}
			entry = cur
			break
		}
		if cur.IsBranchTransaction() {
			entry = cur
			break
		}
		pred := cur.SequencerPredecessor(noReattach)
		if pred == nil {
			break
		}
		cur = pred
	}

	if !ledger.L(slot).HasUpgrade7() {
		return false, ""
	}
	if seen, ok := final.FirstSeen(); ok && seen.After(ledger.ClockTime(base.T(slot, 0))) {
		return true, "its final milestone arrived after the slot boundary"
	}

	// a fork of the chain in the previous slot committing to another bid: with several published
	// commitments the issuer could choose its bid after seeing the others' branches. The reference
	// sequencer commits to the same bid on every milestone of a slot, so its restarts and carries
	// pass.
	want := bidCommitmentOf(final)
	seqID := br.SequencerID.Load()
	if entry == nil || want == nil || seqID == nil {
		return false, ""
	}
	queue := []*vertex.WrappedTx{entry}
	for visited := 0; len(queue) > 0 && visited < maxForkScan; {
		v := queue[0]
		queue = queue[1:]
		for c := range v.ConsumersOf(v.SequencerWrappedOutput().Index) {
			if !c.IsSequencerTransaction() || c.IsBranchTransaction() || c.Slot() != prevSlot {
				continue
			}
			if cid := c.SequencerID.Load(); cid == nil || *cid != *seqID {
				continue
			}
			visited++
			if got := bidCommitmentOf(c); got != nil && !bytes.Equal(got, want) {
				return true, "the chain published milestones committing to different bids in the previous slot"
			}
			queue = append(queue, c)
		}
	}
	return false, ""
}

// bidCommitmentOf is the bid commitment on the milestone's sequencer output, nil when unknown.
func bidCommitmentOf(ms *vertex.WrappedTx) []byte {
	tx := ms.GetTransaction()
	if tx == nil {
		return nil
	}
	c, err := ledger.SeqBidCommitment(tx.SequencerOutput().Output)
	if err != nil {
		return nil
	}
	return c
}
