// Unit tests for the compaction category scan behind `proxi node balance
// --compact`. They build raw outputs with the real ledger library and classify
// them with the WALLET library (txbuildercore.Library[any], the same type the
// CLI gets from the node over the API), so what is exercised is the wallet-side
// path, not a node-side shortcut.
//
// What is worth testing here is the mapping from the two existing classifiers
// onto the disjoint category buckets — in particular the two places where the
// scan decides something neither classifier does: splitting tag-along reclaims
// at the public-window boundary, and telling "cannot claim yet" apart from
// "cannot claim at all". The classifiers themselves are covered by
// ledger/tests/spendable_classify_test.go.
package node_cmd

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

const (
	csAmount = uint64(1_000_000_000)
	csAccept = uint32(60)  // sendWithDeadline acceptance window
	csCreate = uint32(100) // slot every test output is created in
)

func init() {
	ledger.InitWithTestingLedgerData()
}

// walletLibrary builds the singleton-free library the wallet uses, from the
// same definitions the node would serve over /api/v1/ledger_definition.
func walletLibrary(t *testing.T) (*txbuildercore.Library[any], *txbuildercore.Constants) {
	t.Helper()
	l := ledger.L(base.MaxSlot)
	desc, err := easyfl.ReadLibraryFromJSON(l.DefinitionsJSON())
	require.NoError(t, err)
	lib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)
	return lib, ledger.ConstantsFromLibrary(l.Library)
}

// at pins an output to csCreate so Δ is exactly targetSlot − csCreate.
func at(o *ledger.Output) *ledger.OutputWithID {
	return &ledger.OutputWithID{ID: base.RandomOutputID(base.T(csCreate, 1)), Output: o}
}

func swdOut(master, target base.HolderID) *ledger.Output {
	return ledger.NewSendWithDeadlineOutput(csAmount, &ledger.SendWithDeadlineLock{
		MasterID:        master,
		TargetID:        target,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: csAccept,
		CleanupSlots:    1100,
	})
}

// scanOne classifies a single output and returns the category it landed in
// plus its entry, asserting nothing else was bucketed.
func scanOne(t *testing.T, o *ledger.OutputWithID, account base.HolderID, targetSlot uint32) (compactCategory, scanEntry) {
	t.Helper()
	lib, consts := walletLibrary(t)
	s := scanForCompaction(lib, consts, []*ledger.OutputWithID{o}, account, targetSlot)
	for c := compactCategory(0); c < numCategories; c++ {
		if s.count(c) == 1 {
			return c, s.entries[c][0]
		}
	}
	t.Fatalf("output was not classified into any category")
	return 0, scanEntry{}
}

// A plain sigLock output the account holds is the base case.
func TestScanSigLock(t *testing.T) {
	lock := ledger.SigLockRandom()
	account := base.HolderID(lock)
	c, e := scanOne(t, at(ledger.OutputBasic(int64(csAmount), lock)), account, csCreate+10)
	require.Equal(t, catSigLock, c)
	require.Equal(t, csAmount, e.amount)
}

// A sigLock output belonging to somebody else is in the index (the account may
// appear elsewhere in the same output) but is not this account's to claim.
func TestScanForeignSigLockHasNoClaim(t *testing.T) {
	other := base.HolderID(ledger.SigLockRandom())
	c, _ := scanOne(t, at(ledger.OutputBasic(int64(csAmount), ledger.SigLockRandom())), other, csCreate+10)
	require.Equal(t, catNoClaim, c)
}

// Chain outputs are pulled out before the spendable classifier sees them:
// a delegation carries a sigLock the account matches, and would otherwise be
// reported as an unrecognised structure rather than as the chain it is.
func TestScanChainOutputIsNotUnknown(t *testing.T) {
	lock := ledger.SigLockRandom()
	account := base.HolderID(lock)
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(csAmount).WithLock(lock)
		o.PutConstraint(ledger.NewChainOrigin(csCreate).Bytes(), ledger.ConstraintIndexChain)
	})
	c, _ := scanOne(t, at(o), account, csCreate+10)
	require.Equal(t, catChained, c)
}

// The target of a sendWithDeadline can accept while the window is open, and
// the scan reports how much of it is left — the one category that loses tokens
// when it is missed.
func TestScanSWDAcceptReportsRemainingWindow(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	c, e := scanOne(t, at(swdOut(master, target)), target, csCreate+10)
	require.Equal(t, catSWDAccept, c)
	require.EqualValues(t, csAccept-10, e.windowSlots)
}

// Once the acceptance window closes the target's claim is gone for good, so it
// is not "pending" — nothing will reopen it.
func TestScanSWDAcceptExpiredIsNoClaim(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	c, _ := scanOne(t, at(swdOut(master, target)), target, csCreate+csAccept)
	require.Equal(t, catNoClaim, c)
}

// The master's reclaim opens at exactly acceptanceSlots and never closes.
func TestScanSWDMasterPendingThenReclaim(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())

	// Before the window: claimable later, and the scan says in how long.
	c, e := scanOne(t, at(swdOut(master, target)), master, csCreate+10)
	require.Equal(t, catPending, c)
	require.EqualValues(t, csAccept-10, e.windowSlots)

	c, _ = scanOne(t, at(swdOut(master, target)), master, csCreate+csAccept)
	require.Equal(t, catSWDReclaim, c)
}

// A tag-along fee moves through three states for its sender: the sequencer's
// exclusive claim, the sender's exclusive reclaim, and finally the public
// window where anyone may take it. Splitting the last two is the scan's own
// decision — the spendable classifier calls both simply claimable — and it is
// what makes "at risk" mean something.
func TestScanTagAlongThreeWindows(t *testing.T) {
	_, consts := walletLibrary(t)
	sender := base.HolderID(ledger.SigLockRandom())
	out := func() *ledger.Output {
		return ledger.NewTagAlongOutput(csAmount, base.RandomChainID(), sender)
	}

	c, e := scanOne(t, at(out()), sender, csCreate+consts.TagAlongSlots-1)
	require.Equal(t, catPending, c)
	require.EqualValues(t, 1, e.windowSlots)

	c, _ = scanOne(t, at(out()), sender, csCreate+consts.TagAlongSlots)
	require.Equal(t, catTagAlongReclaim, c)

	c, _ = scanOne(t, at(out()), sender, csCreate+consts.TagAlongReclaimSlots-1)
	require.Equal(t, catTagAlongReclaim, c)

	// At the boundary the lock opens to everyone: still the sender's own fee,
	// but now a race.
	c, _ = scanOne(t, at(out()), sender, csCreate+consts.TagAlongReclaimSlots)
	require.Equal(t, catTagAlongCleanup, c)
}

// The aggregate view: buckets are disjoint, the compactable subtotal counts
// only the compactable ones, and at-risk is driven by the two categories that
// lose tokens if ignored.
func TestScanAggregates(t *testing.T) {
	lib, consts := walletLibrary(t)
	lock := ledger.SigLockRandom()
	account := base.HolderID(lock)
	other := base.HolderID(ledger.SigLockRandom())

	outs := []*ledger.OutputWithID{
		at(ledger.OutputBasic(int64(csAmount), lock)),
		at(ledger.OutputBasic(int64(csAmount), lock)),
		at(swdOut(other, account)),                                            // accept, window open
		at(ledger.NewTagAlongOutput(csAmount, base.RandomChainID(), account)), // reclaim
		at(ledger.OutputBasic(int64(csAmount), ledger.SigLockRandom())),       // not ours
	}
	targetSlot := csCreate + consts.TagAlongSlots // past reclaim, inside accept
	require.Less(t, consts.TagAlongSlots, csAccept, "test needs the accept window to outlast the tag-along one")

	s := scanForCompaction(lib, consts, outs, account, targetSlot)
	count, amount := s.compactableCount()
	require.Equal(t, 4, count)
	require.Equal(t, 4*csAmount, amount)
	require.Equal(t, 5, s.numScanned)
	require.Equal(t, 1, s.count(catNoClaim))
	require.True(t, s.atRisk(), "an open accept window is at risk")

	// Every output landed in exactly one bucket.
	total := 0
	for c := compactCategory(0); c < numCategories; c++ {
		total += s.count(c)
	}
	require.Equal(t, len(outs), total)
}

// An output of the account's own that has decayed into its public window is
// flagged, because it is simultaneously the account's to claim and part of the
// pool any cleaner works through — the overlap the grand total has to subtract
// so the same output is not counted twice.
func TestScanMarksOwnPublicOutputs(t *testing.T) {
	lib, consts := walletLibrary(t)
	sender := base.HolderID(ledger.SigLockRandom())
	out := func() *ledger.OutputWithID {
		return at(ledger.NewTagAlongOutput(csAmount, base.RandomChainID(), sender))
	}

	// Reclaimable by the sender alone: not yet public.
	s := scanForCompaction(lib, consts, []*ledger.OutputWithID{out()}, sender, csCreate+consts.TagAlongSlots)
	require.Equal(t, 1, s.count(catTagAlongReclaim))
	n, _ := s.ownPublic()
	require.Zero(t, n)
	require.False(t, s.atRisk())

	// Past the reclaim window the lock opens to everybody.
	s = scanForCompaction(lib, consts, []*ledger.OutputWithID{out()}, sender, csCreate+consts.TagAlongReclaimSlots)
	require.Equal(t, 1, s.count(catTagAlongCleanup))
	n, amount := s.ownPublic()
	require.Equal(t, 1, n)
	require.Equal(t, csAmount, amount)
	require.True(t, s.atRisk(), "an output anyone may take is at risk")
}

// The public flag keys off the lock's public window, not off the tag-along
// case specifically, so a sendWithDeadline past its cleanup deadline is caught
// the same way.
func TestScanMarksPublicSendWithDeadline(t *testing.T) {
	lib, consts := walletLibrary(t)
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())

	// Well past cleanupSlots (1100 in swdOut): public to any signer.
	s := scanForCompaction(lib, consts, []*ledger.OutputWithID{at(swdOut(master, target))}, master, csCreate+2000)
	require.Equal(t, 1, s.count(catSWDReclaim), "still the master's to reclaim")
	n, _ := s.ownPublic()
	require.Equal(t, 1, n, "and simultaneously public")
}
