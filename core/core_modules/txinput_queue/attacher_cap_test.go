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

// TestAttacherCapBoundsUnknownSenderBranch checks that the branch allowance above
// the attacher cap is itself bounded. The branch has no funded sender, so the
// unknown-sender exemption applies; without a bound, a throwaway key could spawn
// attachers forever with fabricated inputs.
func TestAttacherCapBoundsUnknownSenderBranch(t *testing.T) {
	// Force the cap far below zero, which exhausts the branch headroom too: the
	// branch must then be dropped like any other sequencer transaction at the cap.
	q := &TxInputQueue{environment: capTestEnvironment{maxAttachers: -1000}}
	branch := makeSequencerTxForCapTest(t, 79300, 0)
	require.True(t, branch.IsBranchTransaction())
	require.False(t, q.shouldAttachSequencer(branch), "an unknown-sender branch must not bypass an exhausted cap")
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
		q := &TxInputQueue{environment: env(-1000)}
		branch := makeSequencerTxForCapTest(t, 0, 0)
		q.doAttach(branch, false, nil)
		require.Zero(t, attached, "an exhausted branch headroom must stop the attacher")
	})

	t.Run("attached", func(t *testing.T) {
		attached = 0
		q := &TxInputQueue{environment: env(0)}
		branch := makeSequencerTxForCapTest(t, 0, 0)
		q.doAttach(branch, false, nil)
		require.Equal(t, 1, attached, "a branch within the headroom must attach")
	})
}
