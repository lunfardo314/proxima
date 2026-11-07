package ledger

import (
	"bytes"
	_ "embed"
	"text/template"
)

// Library upgrade 6 (kb/library_upgrade6_mining.md): the mine chain's proof of work
// becomes the transit's own transaction ID and the reward is paid as the miner decides;
// mining paused for Upgrade6MiningPauseSlots after the activation; sequencer pace 6;
// a minimum own balance for a new sequencer chain (hard) and for a sequencer to branch,
// to issue milestones in the pre-branch zone and to freeze delegations afresh (soft); the top-up guarantor requires a delegateLock
// predecessor. Pure EasyFL, no new embedded function. Built on the library of upgrade 5.

//go:embed def/upgrade6.easyfl
var upgrade6Source string

// Upgrade6MiningPauseSlots is how long after the activation no transit is valid.
const Upgrade6MiningPauseSlots = 1000

// BuildUpgrade6LibraryAt returns the builder of upgrade 6 listed at slot: it applies the
// upgrade to the compiled JSON of the library before it, the slot-dependent constants
// filled in, and returns the compiled JSON of the upgraded one.
func BuildUpgrade6LibraryAt(slot uint32) func(prevJSON []byte) ([]byte, error) {
	return func(prevJSON []byte) ([]byte, error) {
		var src bytes.Buffer
		err := template.Must(template.New("upgrade6").Parse(upgrade6Source)).Execute(&src, struct {
			UpgradeSlot, MiningResumeSlot uint32
		}{slot, slot + Upgrade6MiningPauseSlots})
		if err != nil {
			return nil, err
		}
		return buildUpgrade(prevJSON, "upgrade 6", src.String())
	}
}
