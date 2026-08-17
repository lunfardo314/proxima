package glb

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// sequencerMinimumFee fetches the sequencer's declared MinimumFee via the
// /get_sequencer_target_info endpoint. JSON-decoded server-side; no client-side
// output parsing, so this path stays free of the ledger.L() singleton (unlike
// client.GetSequencerData, which parses the chain output via the singleton).
func sequencerMinimumFee(seqID base.ChainID) (uint64, error) {
	info, err := GetClient().GetSequencerTargetInfo(seqID)
	if err != nil {
		return 0, err
	}
	return info.MinimumFee, nil
}

// GetRequiredTagAlongFee is the single place where a tag-along fee is decided.
// The sequencer's own declared minimum is the authority: anything below it is
// simply not picked up. The profile's tag_along.fee is a preference on top of
// that — it wins only when it is the larger of the two.
//
// The sequencer must be readable: silently falling back to the profile fee
// would build a transaction the target ignores, which looks like a lost
// transaction rather than an error.
func GetRequiredTagAlongFee(seqID base.ChainID) (uint64, error) {
	seqMinFee, err := sequencerMinimumFee(seqID)
	if err != nil {
		return 0, fmt.Errorf("cannot read the minimum tag-along fee of sequencer %s: %w", seqID.StringShort(), err)
	}

	profileFee := GetTagAlongFee()
	if profileFee > seqMinFee {
		Verbosef("using profile tag-along fee %s (sequencer %s requires %s)",
			util.Th(profileFee), seqID.StringShort(), util.Th(seqMinFee))
		return profileFee, nil
	}
	Verbosef("using the minimum tag-along fee %s required by sequencer %s (profile has %s)",
		util.Th(seqMinFee), seqID.StringShort(), util.Th(profileFee))
	return seqMinFee, nil
}
