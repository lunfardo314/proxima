package seqdata

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// The branch bonus a sequencer asks is the stored value clamped to the interval the ledger accepts
// from library upgrade 7, [MinBranchBonus, DefaultBranchBonus]; no key means the default, and
// removing the key returns to it.
func TestBranchBonus(t *testing.T) {
	require.EqualValues(t, DefaultBranchBonus, (&SequencerData{}).BranchBonus(), "no key: the default")
	for _, c := range []struct{ stored, asked uint64 }{
		{0, MinBranchBonus},
		{1, 1},
		{500_000, 500_000},
		{DefaultBranchBonus, DefaultBranchBonus},
		{DefaultBranchBonus + 1, DefaultBranchBonus},
		{math.MaxUint64, DefaultBranchBonus},
	} {
		sd := (&SequencerData{}).SetBranchBonus(c.stored)
		require.EqualValues(t, c.asked, sd.BranchBonus(), "stored %d", c.stored)
		// the stored value survives the encoding unchanged, and is clamped when read
		back, err := FromBytes(sd.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, c.asked, back.BranchBonus(), "stored %d, decoded", c.stored)
	}
	require.EqualValues(t, DefaultBranchBonus, (&SequencerData{}).SetBranchBonus(1).RemoveBranchBonus().BranchBonus())
}
