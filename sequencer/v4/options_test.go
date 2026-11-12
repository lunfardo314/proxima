package v4

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// The defaults are the spec's (kb/sequencer_v4.md) and pass validation, with no `sequencer.v4`
// subsection in the config.
func TestV4OptionsDefaults(t *testing.T) {
	viper.Reset()
	o, err := readV4Options()
	require.NoError(t, err)
	require.Equal(t, defaultV4Options(), o)
	require.True(t, o.lastMilestone)
	require.Equal(t, 7, o.finalLeadTicks)
	require.Equal(t, 2, o.lastBuildLeadTicks)
	require.Equal(t, 150*time.Millisecond, o.fallbackReserve)
}

// Keys set in `sequencer.v4` override the defaults one by one; absent keys keep theirs.
func TestV4OptionsOverride(t *testing.T) {
	viper.Reset()
	defer viper.Reset()
	viper.Set("sequencer.v4.last_milestone", false)
	viper.Set("sequencer.v4.final_lead", 10)
	viper.Set("sequencer.v4.fallback_reserve_ms", 0)
	o, err := readV4Options()
	require.NoError(t, err)
	require.False(t, o.lastMilestone)
	require.Equal(t, 10, o.finalLeadTicks)
	require.Equal(t, 2, o.lastBuildLeadTicks)
	require.Equal(t, time.Duration(0), o.fallbackReserve)
}

// The last milestone's build must start after the final milestone's stamp and before the boundary,
// so inconsistent leads are refused at start rather than producing a silent schedule.
func TestV4OptionsInvalid(t *testing.T) {
	for _, tc := range []struct{ final, last int }{{1, 0}, {7, 0}, {7, 7}, {7, 9}} {
		o := defaultV4Options()
		o.finalLeadTicks, o.lastBuildLeadTicks = tc.final, tc.last
		require.Error(t, o.valid(), "final_lead %d last_build_lead %d", tc.final, tc.last)
	}
	o := defaultV4Options()
	o.fallbackReserve = -time.Millisecond
	require.Error(t, o.valid())
}

// With the last milestone on, the final milestone stops being retried where the last one's build
// starts; with it off, v3's schedule applies.
func TestFinalLastTryTicks(t *testing.T) {
	seq := &Sequencer{v4: defaultV4Options()}
	require.EqualValues(t, 2, seq.finalLastTryTicks())
	seq.v4.lastBuildLeadTicks = 3
	require.EqualValues(t, 3, seq.finalLastTryTicks())
	seq.v4.lastMilestone = false
	require.EqualValues(t, finalConsolidationLastTryTicks, seq.finalLastTryTicks())
}
