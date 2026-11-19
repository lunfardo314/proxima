package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// From library upgrade 7 the target honours an askstop's allowance only while the request is inside
// its tag-along window, where the request's guarantor checks the compensation ceiling. An allowance
// of ten times the ceiling is refused two slots after the request on both sides of the slot; 391
// slots after it, it is accepted before the slot and refused from it.
func TestAllowanceWindow(t *testing.T) {
	for _, c := range []struct {
		name     string
		start    uint32
		delay    uint32
		accepted bool
	}{
		{"before the slot, inside the window", 100, 2, false},
		{"before the slot, after the reclaim window", 100, 391, true},
		{"after the slot, inside the window", u7Slot + 10, 2, false},
		{"after the slot, after the reclaim window", u7Slot + 10, 391, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := &testData{T: t, seqBalance: 20_000_000_000, seqExtension: true}
			td.init()
			withUpgrade7(t, td.u)
			_, _, err := td.initDelegationUTXOMake(base.T(c.start, 10), 4, 900)
			require.NoError(t, err)
			ts := base.T(c.start+1, 10)
			require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
				ts: ts, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts), inflate: true, disableConsistencyChecks: true,
			}))
			ceiling := td.delegatedOutput.AllowanceCeiling()
			require.Greater(t, ceiling, uint64(0))
			take := 10 * ceiling
			req := td.makeAllowanceRequestOutput(base.T(c.start+2, 10), td.masterPrivateKey, td.masterAddr, td.delegatedOutput.ChainID, take)
			at := base.T(req.ID.Slot()+c.delay, 20)
			require.True(t, td.delegatedOutput.IsInFrozenSlot(at.Slot))
			err = td.revokeDelegationWithAllowance(allowanceRevokeParams{ts: at, request: req, take: take})
			if c.accepted {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if c.delay > 30 {
				require.Contains(t, err.Error(), "allowance request is past its tag-along window")
			}
		})
	}
}
