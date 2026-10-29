package txinput_queue

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// capTestEnvironment provides only the environment methods shouldAttachSequencer
// reaches when the cap is forced to zero.
type capTestEnvironment struct {
	environment
}

func (capTestEnvironment) MaxConcurrentAttachers() int   { return 0 }
func (capTestEnvironment) Tracef(string, string, ...any) {}
func (capTestEnvironment) IncCounter(string)             {}

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
