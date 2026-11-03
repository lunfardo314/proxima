package txinput_queue

import (
	"testing"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// capTestEnvironment provides only the environment methods shouldAttachSequencer
// reaches when the cap is under test.
type capTestEnvironment struct {
	environment
	maxAttachers int
	attachFun    func(tx *transaction.Transaction, opts ...attacher.AttachTxOption)
}

func (e capTestEnvironment) MaxConcurrentAttachers() int { return e.maxAttachers }
func (capTestEnvironment) Tracef(string, string, ...any) {}
func (capTestEnvironment) IncCounter(string)             {}
func (e capTestEnvironment) AttachFun() func(tx *transaction.Transaction, opts ...attacher.AttachTxOption) {
	return e.attachFun
}

// makeSequencerTxForCapTest builds a parseable sequencer transaction with the
// given timestamp. Tick 0 makes it a branch; any other tick makes it a milestone.
func makeSequencerTxForCapTest(t *testing.T, slot uint32, tick byte) *transaction.Transaction {
	txb := txbuildercore.New(0)
	txb.SetTimestamp(base.T(slot, tick))
	txb.SetSequencerData(0, txbuildercore.SequencerOutputIndexNone)

	out := txbuildercore.NewOutputBuilder()
	out.MustPushConstraint([]byte{0x80})
	out.MustPushConstraint(nil)
	out.MustPushConstraint([]byte{0x80})
	txb.ProduceOutput(out.Bytes())

	tx, err := transaction.ParseLibraryAgnostic(txb.Bytes())
	require.NoError(t, err)
	return tx
}

// TestAttacherCapDoesNotDropBranchGossip checks that a branch still attaches when the
// attacher cap is full. Dropping it there was the wedge: after a stale attacher had
// abandoned a missing branch, no fresh attacher could re-pull it, so the depth cap was
// never reached and forward sync was never triggered. Non-branch milestones stay capped.
func TestAttacherCapDoesNotDropBranchGossip(t *testing.T) {
	q := &TxInputQueue{environment: capTestEnvironment{}}

	branch := makeSequencerTxForCapTest(t, 79300, 0)
	require.True(t, branch.IsBranchTransaction())
	require.True(t, q.shouldAttachSequencer(branch), "branch gossip must pass at the attacher cap")

	milestone := makeSequencerTxForCapTest(t, 79300, 1)
	require.False(t, milestone.IsBranchTransaction())
	require.False(t, q.shouldAttachSequencer(milestone), "non-branch milestone must still be capped")
}

// TestAttacherCapBoundsUnknownSenderBranch pins the branch headroom boundary.
// The branch has no funded sender, so the unknown-sender exemption applies;
// without a bound, a throwaway key could spawn attachers forever.
func TestAttacherCapBoundsUnknownSenderBranch(t *testing.T) {
	branch := makeSequencerTxForCapTest(t, 79300, 0)
	require.True(t, branch.IsBranchTransaction())

	t.Run("at the headroom limit", func(t *testing.T) {
		q := &TxInputQueue{environment: capTestEnvironment{maxAttachers: -branchAttacherHeadroom}}
		require.False(t, q.shouldAttachSequencer(branch), "a branch at the headroom limit must be dropped")
	})
	t.Run("one below the headroom limit", func(t *testing.T) {
		q := &TxInputQueue{environment: capTestEnvironment{maxAttachers: -branchAttacherHeadroom + 1}}
		require.True(t, q.shouldAttachSequencer(branch), "a branch below the headroom limit must attach")
	})
}

// TestAttacherCapAntiStarvation checks that an older milestone still drains at the
// cap while a branch at the same timestamp is subject to the branch headroom and is
// dropped once that headroom is exhausted.
func TestAttacherCapAntiStarvation(t *testing.T) {
	newest := makeSequencerTxForCapTest(t, 79300, 0)
	require.True(t, newest.IsBranchTransaction())
	older := makeSequencerTxForCapTest(t, 79299, 5)
	require.False(t, older.IsBranchTransaction())
	newestID := newest.ID()
	newestTicks := newestID.Timestamp().TicksSinceGenesis()

	qMilestone := &TxInputQueue{environment: capTestEnvironment{maxAttachers: 0}}
	qMilestone.latestAttachedTimestamp.Store(newestTicks)
	require.True(t, qMilestone.shouldAttachSequencer(older), "an older milestone must drain even at the cap")

	qBranch := &TxInputQueue{environment: capTestEnvironment{maxAttachers: -branchAttacherHeadroom}}
	qBranch.latestAttachedTimestamp.Store(newestTicks)
	require.False(t, qBranch.shouldAttachSequencer(newest), "a branch must not use anti-starvation past its headroom")
}

// TestAttacherCapRecheckedAtAttach covers the gap the reception gate leaves for
// future-dated transactions: they are admitted now and attached after the clock
// catches up, so many can reach the attach point with the same attacher count. The
// branch must be dropped there once the headroom is exhausted, and attached while
// it still fits.
func TestAttacherCapRecheckedAtAttach(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	attached := 0
	env := func(maxAttachers int) capTestEnvironment {
		return capTestEnvironment{
			maxAttachers: maxAttachers,
			attachFun: func(*transaction.Transaction, ...attacher.AttachTxOption) {
				attached++
			},
		}
	}

	t.Run("dropped", func(t *testing.T) {
		attached = 0
		q := &TxInputQueue{environment: env(-branchAttacherHeadroom)}
		branch := makeSequencerTxForCapTest(t, 0, 0)
		q.doAttachDelayed(branch, false, nil)
		require.Zero(t, attached, "a branch at the headroom limit must stop the attacher")
	})

	t.Run("attached", func(t *testing.T) {
		attached = 0
		q := &TxInputQueue{environment: env(-branchAttacherHeadroom + 1)}
		branch := makeSequencerTxForCapTest(t, 0, 0)
		q.doAttachDelayed(branch, false, nil)
		require.Equal(t, 1, attached, "a branch below the headroom limit must attach")
	})
}
