package glb

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// MaxAutoTagAlongFee is the maximum fee that will be accepted without user confirmation
const MaxAutoTagAlongFee = uint64(100)

// GetSequencerMinimumFee fetches the sequencer's declared MinimumFee via the
// /get_sequencer_target_info endpoint. JSON-decoded server-side; no client-side
// output parsing, so this path stays free of the ledger.L() singleton (unlike
// client.GetSequencerData, which parses the chain output via the singleton).
func GetSequencerMinimumFee(seqID base.ChainID) (uint64, error) {
	info, err := GetClient().GetSequencerTargetInfo(seqID)
	if err != nil {
		return 0, err
	}
	return info.MinimumFee, nil
}

// GetRequiredTagAlongFee determines the tag-along fee to use based on profile and sequencer settings.
// Logic:
// - If profile fee > 0 and profile fee >= sequencer minimum: use profile fee
// - If sequencer minimum > profile fee (and profile fee > 0): ask user if OK to use higher fee
// - If profile fee is 0: use sequencer minimum (with MaxAutoTagAlongFee safety check)
// - If sequencer not found or error: use profile fee (or 0 if not set)
func GetRequiredTagAlongFee(seqID base.ChainID) (uint64, error) {
	profileFee := GetTagAlongFee()

	seqMinFee, err := GetSequencerMinimumFee(seqID)
	if err != nil {
		// Sequencer not found or error - use profile fee
		Verbosef("GetRequiredTagAlongFee: could not retrieve sequencer data for %s: %v", seqID.StringShort(), err)
		return profileFee, nil
	}

	// If profile has a fee set and it's sufficient for the sequencer, use it
	if profileFee > 0 && profileFee >= seqMinFee {
		Verbosef("using profile tag-along fee: %s (sequencer minimum: %s)", util.Th(profileFee), util.Th(seqMinFee))
		return profileFee, nil
	}

	// If sequencer requires more than profile fee
	if seqMinFee > profileFee {
		if profileFee > 0 {
			// Profile has a fee set but sequencer wants more - ask user
			prompt := fmt.Sprintf("Sequencer %s requires tag-along fee of %s tokens (profile has %s). Accept higher fee?",
				seqID.StringShort(), util.Th(seqMinFee), util.Th(profileFee))
			if !YesNoPrompt(prompt, false) {
				return 0, fmt.Errorf("user declined higher tag-along fee of %s (profile: %s)", util.Th(seqMinFee), util.Th(profileFee))
			}
		} else if seqMinFee > MaxAutoTagAlongFee {
			// No profile fee set and sequencer wants more than safety limit - ask user
			prompt := fmt.Sprintf("Sequencer %s requires tag-along fee of %s tokens (> %s). Accept?",
				seqID.StringShort(), util.Th(seqMinFee), util.Th(MaxAutoTagAlongFee))
			if !YesNoPrompt(prompt, false) {
				return 0, fmt.Errorf("user declined high tag-along fee of %s", util.Th(seqMinFee))
			}
		}
		return seqMinFee, nil
	}

	// Profile fee is 0 and sequencer minimum is 0
	return 0, nil
}
