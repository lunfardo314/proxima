package glb

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// MaxAutoTagAlongFee is the maximum fee that will be accepted without user confirmation
const MaxAutoTagAlongFee = uint64(100)

// GetRequiredTagAlongFee retrieves the minimum tag-along fee from a sequencer.
// Returns (fee, nil) on success. If fee > MaxAutoTagAlongFee, prompts user for confirmation.
// If sequencer not found or no minimum set, returns 0.
func GetRequiredTagAlongFee(seqID base.ChainID) (uint64, error) {
	md, err := GetClient().GetSequencerData(seqID)
	if err != nil {
		// Sequencer not found or error - return 0 (let sequencer decide)
		Verbosef("GetRequiredTagAlongFee: could not retrieve sequencer data for %s: %v", seqID.StringShort(), err)
		return 0, nil
	}
	fee := md.MinimumFee()
	if fee > MaxAutoTagAlongFee {
		prompt := fmt.Sprintf("Sequencer %s requires tag-along fee of %s tokens (> %s). Accept?",
			seqID.StringShort(), util.Th(fee), util.Th(MaxAutoTagAlongFee))
		if !YesNoPrompt(prompt, false) {
			return 0, fmt.Errorf("user declined high tag-along fee of %s", util.Th(fee))
		}
	}
	return fee, nil
}
