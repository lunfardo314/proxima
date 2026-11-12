package attacher

import (
	"testing"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

func init() {
	ledger.InitWithTestingLedgerData()
}

// countTestEnvironment implements only what countAttacher calls; the embedded nil interface
// panics on anything else.
type countTestEnvironment struct {
	Environment
}

func (countTestEnvironment) VertexTTLSlots() uint32 { return 24 }
func (countTestEnvironment) IncCounter(string)      {}
func (countTestEnvironment) DecCounter(string)      {}

// A reattach or revival is catch-up work the input gate never admitted, so it must count as a past
// attacher even for a milestone well inside the memDAG TTL. In the 2026-11-08 network halts one
// stale milestone reattached hundreds of milestones 13-19 slots old: counted toward the cap, they
// made every node drop all live milestones for a whole TTL.
func TestCountAttacherCatchUp(t *testing.T) {
	env := countTestEnvironment{}
	fresh := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: ledger.TimeNow().Slot, Tick: 10})

	before := NumPastAttachers()

	// a fresh milestone admitted by the gate is live work
	done := countAttacher(fresh, env, false, false)
	require.Equal(t, before, NumPastAttachers())
	done()

	// the same milestone reattached or revived is catch-up work
	done = countAttacher(fresh, env, true, false)
	require.Equal(t, before+1, NumPastAttachers())
	done()
	require.Equal(t, before, NumPastAttachers())

	// the same milestone solicited for an attacher that needed it is catch-up work too
	done = countAttacher(fresh, env, false, true)
	require.Equal(t, before+1, NumPastAttachers())
	done()
	require.Equal(t, before, NumPastAttachers())
}

// A milestone counts as solicited when it was pulled from a peer or loaded from the txstore for an
// attacher, not when it arrived as gossip or from the own sequencer. After a restart on 2026-11-12 the
// rebuild of the live milestones' deltas solicited hundreds of milestones under 24 slots old; counted
// as live they filled the cap, and the gate refused every branch the rebuild was waiting for.
func TestIsSolicited(t *testing.T) {
	require.False(t, isSolicited(nil))
	require.False(t, isSolicited(&txmetadata.TransactionMetadata{}), "gossip carries no source type")
	require.False(t, isSolicited(&txmetadata.TransactionMetadata{SourceTypeNonPersistent: txmetadata.SourceTypeSequencer}))
	require.True(t, isSolicited(&txmetadata.TransactionMetadata{SourceTypeNonPersistent: txmetadata.SourceTypePulled}))
	require.True(t, isSolicited(&txmetadata.TransactionMetadata{SourceTypeNonPersistent: txmetadata.SourceTypeTxStore}))
}
