package seqdata

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRoundTrip verifies all fields survive Bytes/FromBytes serialization.
func TestRoundTrip(t *testing.T) {
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
	sd.SetGreedy(true)
	sd.SetSeqProfitMarginPromille(500)

	sdBin := sd.Bytes()
	sdBack, err := FromBytes(sdBin)
	require.NoError(t, err)

	// verify all fields
	require.Equal(t, "kuku", sdBack.Name())
	require.EqualValues(t, 15, sdBack.MinimumFee())
	require.EqualValues(t, 5, sdBack.ChainHeight())
	require.EqualValues(t, 2, sdBack.BranchHeight())
	require.EqualValues(t, 3, sdBack.Pace())
	require.True(t, sdBack.IsGreedy())
	require.EqualValues(t, 500, sdBack.InflationProfitMarginPromille())

	// re-serialized bytes must match
	require.Equal(t, sdBin, sdBack.Bytes())

	t.Logf("compact JSON:\n%s", string(sd.Bytes()))
	t.Logf("pretty JSON:\n%s", sd.Lines("  ").String())
}

// TestEmptyRoundTrip verifies that an empty SequencerData serializes to compact JSON "{}".
func TestEmptyRoundTrip(t *testing.T) {
	sd := New()
	sdBin := sd.Bytes()
	require.Equal(t, "{}", string(sdBin))

	sdBack, err := FromBytes(sdBin)
	require.NoError(t, err)
	require.Equal(t, "", sdBack.Name())
	require.EqualValues(t, 0, sdBack.MinimumFee())
	require.EqualValues(t, 0, sdBack.ChainHeight())
	require.EqualValues(t, 0, sdBack.BranchHeight())
	require.EqualValues(t, 0, sdBack.Pace())
	require.False(t, sdBack.IsGreedy())
	require.EqualValues(t, 0, sdBack.InflationProfitMarginPromille())
}

// TestFromBytesEmpty verifies that empty/nil input returns zero-value SequencerData.
func TestFromBytesEmpty(t *testing.T) {
	sd1, err := FromBytes(nil)
	require.NoError(t, err)
	require.Equal(t, "", sd1.Name())

	sd2, err := FromBytes([]byte{})
	require.NoError(t, err)
	require.Equal(t, "", sd2.Name())
}

// TestMinimalRoundTrip verifies serialization with only one field set.
func TestMinimalRoundTrip(t *testing.T) {
	sd := New()
	sd.IncChainHeight()
	sdBin := sd.Bytes()

	// only "c" key should be present
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(sdBin, &m))
	require.Len(t, m, 1)
	require.Contains(t, m, "c")

	sdBack, err := FromBytes(sdBin)
	require.NoError(t, err)
	require.EqualValues(t, 1, sdBack.ChainHeight())
	require.Equal(t, "", sdBack.Name())

	t.Logf("minimal JSON: %s", string(sdBin))
}

// TestClone verifies deep independence and optional modifier.
func TestClone(t *testing.T) {
	sd := New()
	sd.SetName("original").SetMinimumFee(100).IncChainHeight(5)

	// clone without modifier
	cp := sd.Clone()
	require.Equal(t, sd.Name(), cp.Name())
	require.Equal(t, sd.MinimumFee(), cp.MinimumFee())
	require.Equal(t, sd.ChainHeight(), cp.ChainHeight())

	// mutating clone does not affect original
	cp.SetName("modified")
	require.Equal(t, "original", sd.Name())
	require.Equal(t, "modified", cp.Name())

	// clone with modifier
	cp2 := sd.Clone(func(u *SequencerData) {
		u.SetName("via-modifier")
		u.SetPace(7)
	})
	require.Equal(t, "via-modifier", cp2.Name())
	require.EqualValues(t, 7, cp2.Pace())
	// original unchanged
	require.Equal(t, "original", sd.Name())
	require.EqualValues(t, 0, sd.Pace())
}

// TestInflationProfitMarginEdgeCases covers the edge cases in InflationProfitMargin.
func TestInflationProfitMarginEdgeCases(t *testing.T) {
	sd := New()

	// promille == 0 => always 0
	sd.SetSeqProfitMarginPromille(0)
	require.EqualValues(t, 0, sd.InflationProfitMargin(1000))

	// promille == 500 => 50%
	sd.SetSeqProfitMarginPromille(500)
	require.EqualValues(t, 500, sd.InflationProfitMargin(1000))

	// promille == 1000 => 100%
	sd.SetSeqProfitMarginPromille(1000)
	require.EqualValues(t, 1000, sd.InflationProfitMargin(1000))

	// promille > 1000 => returns full amount
	sd.SetSeqProfitMarginPromille(1500)
	require.EqualValues(t, 1000, sd.InflationProfitMargin(1000))

	// overflow protection: very large amount with non-trivial promille
	sd.SetSeqProfitMarginPromille(2)
	require.EqualValues(t, 0, sd.InflationProfitMargin(math.MaxUint64))
}

// TestCompactJSON verifies there is no extra whitespace in Bytes() output.
func TestCompactJSON(t *testing.T) {
	sd := New()
	sd.SetName("test").SetMinimumFee(42).IncChainHeight(3)
	raw := string(sd.Bytes())

	// compact JSON has no newlines or indentation
	require.False(t, strings.Contains(raw, "\n"))
	require.False(t, strings.Contains(raw, "  "))

	t.Logf("compact: %s", raw)
}

// TestPrettyJSON verifies Lines() produces indented JSON.
func TestPrettyJSON(t *testing.T) {
	sd := New()
	sd.SetName("pretty_test").SetMinimumFee(99).SetGreedy(true)
	pretty := sd.Lines().String()

	// pretty JSON should contain newlines and indentation
	require.True(t, strings.Contains(pretty, "\n"))
	require.True(t, strings.Contains(pretty, "  "))

	t.Logf("pretty:\n%s", pretty)
}

// TestIncHeightVariadic verifies IncChainHeight and IncBranchHeight with explicit amounts.
func TestIncHeightVariadic(t *testing.T) {
	sd := New()
	sd.IncChainHeight()      // +1
	sd.IncChainHeight(9)     // +9 = 10
	sd.IncBranchHeight()     // +1
	sd.IncBranchHeight(4)    // +4 = 5

	require.EqualValues(t, 10, sd.ChainHeight())
	require.EqualValues(t, 5, sd.BranchHeight())
}

// TestSetPaceZero verifies that pace 0 round-trips correctly.
func TestSetPaceZero(t *testing.T) {
	sd := New()
	sd.SetPace(5)
	require.EqualValues(t, 5, sd.Pace())

	sd.SetPace(0)
	require.EqualValues(t, 0, sd.Pace())

	// round-trip with pace 0
	sdBack, err := FromBytes(sd.Bytes())
	require.NoError(t, err)
	require.EqualValues(t, 0, sdBack.Pace())
}

// TestGreedyRoundTrip verifies greedy flag serialization in both states.
func TestGreedyRoundTrip(t *testing.T) {
	sd := New()
	sd.SetGreedy(true)
	sdBack, err := FromBytes(sd.Bytes())
	require.NoError(t, err)
	require.True(t, sdBack.IsGreedy())

	sd.SetGreedy(false)
	sdBack, err = FromBytes(sd.Bytes())
	require.NoError(t, err)
	require.False(t, sdBack.IsGreedy())
}
