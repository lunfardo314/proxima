package attacher

import (
	"testing"

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
	done := countAttacher(fresh, env, false)
	require.Equal(t, before, NumPastAttachers())
	done()

	// the same milestone reattached or revived is catch-up work
	done = countAttacher(fresh, env, true)
	require.Equal(t, before+1, NumPastAttachers())
	done()
	require.Equal(t, before, NumPastAttachers())

}
