package v4

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// v4Options are the settings only v4 understands, read from the `sequencer.v4` config subsection so
// that a node switching versions does not misread them (kb/sequencer_v4.md).
type v4Options struct {
	// lastMilestone adds a pre-branch milestone stamped at the slot's last tick, collecting the
	// peers' final milestones, which nobody else can endorse before the branch
	lastMilestone bool
	// finalLeadTicks is how many ticks before the boundary the final milestone is stamped
	finalLeadTicks int
	// lastBuildLeadTicks is how many ticks before the boundary the last milestone's build starts,
	// with its timestamp fixed at the last tick, ahead of the clock
	lastBuildLeadTicks int
	// fallbackReserve is the time before the build deadline the factory's proposal must be done by
	// when a cheaper proposal is already complete and eligible; 0 turns it off
	fallbackReserve time.Duration
}

func defaultV4Options() v4Options {
	return v4Options{
		lastMilestone:      true,
		finalLeadTicks:     7,
		lastBuildLeadTicks: 2,
		fallbackReserve:    150 * time.Millisecond,
	}
}

// readV4Options reads the `sequencer.v4` subsection over the defaults. Absent keys keep the default.
func readV4Options() (v4Options, error) {
	ret := defaultV4Options()
	sub := viper.Sub("sequencer.v4")
	if sub == nil {
		return ret, nil
	}
	if sub.IsSet("last_milestone") {
		ret.lastMilestone = sub.GetBool("last_milestone")
	}
	if sub.IsSet("final_lead") {
		ret.finalLeadTicks = sub.GetInt("final_lead")
	}
	if sub.IsSet("last_build_lead") {
		ret.lastBuildLeadTicks = sub.GetInt("last_build_lead")
	}
	if sub.IsSet("fallback_reserve_ms") {
		ret.fallbackReserve = time.Duration(sub.GetInt("fallback_reserve_ms")) * time.Millisecond
	}
	return ret, ret.valid()
}

// valid checks the ticks against each other: the last milestone's build starts after the final
// milestone's stamp and leaves at least one tick before the boundary.
func (o v4Options) valid() error {
	if o.finalLeadTicks < 2 {
		return fmt.Errorf("sequencer.v4.final_lead must be at least 2, got %d", o.finalLeadTicks)
	}
	if o.lastBuildLeadTicks < 1 || o.lastBuildLeadTicks >= o.finalLeadTicks {
		return fmt.Errorf("sequencer.v4.last_build_lead must be in [1, final_lead), got %d (final_lead %d)",
			o.lastBuildLeadTicks, o.finalLeadTicks)
	}
	if o.fallbackReserve < 0 {
		return fmt.Errorf("sequencer.v4.fallback_reserve_ms must not be negative")
	}
	return nil
}

func (o v4Options) String() string {
	return fmt.Sprintf("last_milestone: %v, final_lead: %d, last_build_lead: %d, fallback_reserve: %v",
		o.lastMilestone, o.finalLeadTicks, o.lastBuildLeadTicks, o.fallbackReserve)
}
