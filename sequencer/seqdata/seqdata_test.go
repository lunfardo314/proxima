package seqdata

import (
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/stretchr/testify/require"
)

func TestSequencerData(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		sd := New()
		sd.SetName("kuku")
		sd.SetMinimumFee(15)
		sd.IncChainHeight()
		sd.IncChainHeight()
		sd.IncChainHeight()
		sd.IncChainHeight()
		sd.IncChainHeight()
		sd.IncBranchHeight()
		sd.IncBranchHeight()
		sd.SetPace(3)
		sdBin := sd.Bytes()
		sdBack, err := FromBytes(sdBin)
		require.NoError(t, err)
		require.EqualValues(t, sdBack.Bytes(), sdBin)
		if sdBack.Name() != sd.Name() ||
			sdBack.MinimumFee() != sd.MinimumFee() ||
			sdBack.ChainHeight() != sd.ChainHeight() ||
			sdBack.BranchHeight() != sd.BranchHeight() ||
			sdBack.Pace() != sd.Pace() {
			t.Error("wrong sequencer data")
		}
		t.Logf("-----\n%s", sd.Lines("     ").String())
		t.Logf("----- \nbytes = %s", easyfl_util.Fmt(sd.Bytes()))
	})
	t.Run("2", func(t *testing.T) {
		sd := New()
		t.Logf("----- empty\n%s", sd.Lines("     ").String())
		t.Logf("----- \nbytes = %s", easyfl_util.Fmt(sd.Bytes()))
		sd.IncChainHeight()
		t.Logf("----- with chain height\n%s", sd.Lines("     ").String())
		t.Logf("----- \nbytes = %s", easyfl_util.Fmt(sd.Bytes()))
	})

}
