package memdag

import (
	"errors"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

func init() {
	ledger.InitWithTestingLedgerData()
}

// A rejected branch is remembered for badBranchTTLSlots after it was last rejected or referenced: a
// reference renews it, so a branch diverged nodes keep building on stays known for as long as they
// do, and one nobody refers to any more is forgotten.
func TestBadBranchRememberedUntilTTL(t *testing.T) {
	d := &MemDAG{badBranches: make(map[base.TransactionID]badBranchRecord)}
	txid := base.RandomTransactionID(true, 2, base.T(1000, 0))
	rejection := errors.New("stem-value mismatch")

	require.NoError(t, d.KnownBadBranchNoLock(txid))
	d.RememberBadBranch(txid, rejection)
	require.Equal(t, rejection, d.KnownBadBranchNoLock(txid))

	last := d.badBranches[txid].lastSlot
	d.pruneBadBranchesNoLock(last + badBranchTTLSlots)
	require.Equal(t, rejection, d.KnownBadBranchNoLock(txid), "still within the TTL")

	// a reference renews the record: its age counts from the latest reference
	rec := d.badBranches[txid]
	rec.lastSlot = last - 10
	d.badBranches[txid] = rec
	require.Equal(t, rejection, d.KnownBadBranchNoLock(txid))
	require.GreaterOrEqual(t, d.badBranches[txid].lastSlot, last)

	d.pruneBadBranchesNoLock(d.badBranches[txid].lastSlot + badBranchTTLSlots + 1)
	require.NoError(t, d.KnownBadBranchNoLock(txid), "forgotten past the TTL")
}
