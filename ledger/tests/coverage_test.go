package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/stretchr/testify/require"
)

func TestSerialization(t *testing.T) {
	rr := multistate.RootRecord{
		Root:           ledger.RandomVCommitment(),
		SequencerID:    ledger.RandomChainID(),
		LedgerCoverage: ledger.Coverage{1337, 1337 + 1337},
	}
	bin := rr.Bytes()
	rrBack, err := multistate.RootRecordFromBytes(bin)
	require.NoError(t, err)
	require.True(t, ledger.CommitmentModel.EqualCommitments(rr.Root, rrBack.Root))
	require.EqualValues(t, rr.SequencerID, rrBack.SequencerID)
	require.EqualValues(t, rr.LedgerCoverage, rrBack.LedgerCoverage)
	require.EqualValues(t, ledger.Coverage{1337, 1337 + 1337}, rrBack.LedgerCoverage)
	require.True(t, rr.LedgerCoverage.LatestDelta() == 1337)
}
