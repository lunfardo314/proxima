package seqdata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSequencerData(t *testing.T) {
	sd := SequencerData{
		Name:         "kuku",
		MinimumFee:   15,
		ChainHeight:  1337,
		BranchHeight: 200,
		Pace:         3,
	}
	sdBin := sd.Bytes()
	sdBack, err := SequencerDataFromBytes(sdBin)
	require.NoError(t, err)
	require.EqualValues(t, sdBack.Bytes(), sdBin)
	if sdBack.Name != sd.Name || sdBack.MinimumFee != sd.MinimumFee || sdBack.ChainHeight != sd.ChainHeight || sdBack.BranchHeight != sd.BranchHeight || sdBack.Pace != sd.Pace {
		t.Error("wrong sequencer data")
	}
	jsonData := sd.JSON()
	t.Logf("------------\n%s", string(jsonData))
	sdBack, err = SequencerDataFromJSON(jsonData)
	require.NoError(t, err)
	require.EqualValues(t, sdBack.Bytes(), sdBack.Bytes())
}
