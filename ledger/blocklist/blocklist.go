// Package blocklist is node policy, not a ledger rule: it freezes listed
// outputs and listed signers by making invalid every transaction that consumes
// such an output or is signed by such a holder. It only rejects, so nodes
// holding the coverage majority enforce it for the network.
package blocklist

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
)

// ActivationSlot is the first slot whose transactions are checked. Earlier
// transactions stay valid, so history committed before the list was cut
// replays and reattaches unchanged. Set to a slot by which every enforcing node
// runs this list.
const ActivationSlot = 254500

var (
	frozenOutputs = mustOutputIDs(frozenOutputIDs...)
	frozenHolders = mustHolderIDs(frozenHolderIDs...)
)

// Check returns an error when a transaction of the given slot is signed by a
// frozen holder or consumes a frozen output.
func Check(slot uint32, signer base.HolderID, inputs []base.OutputID) error {
	if slot < ActivationSlot {
		return nil
	}
	if _, found := frozenHolders[signer]; found {
		return fmt.Errorf("blocklist: signer %s is frozen", hex.EncodeToString(signer[:]))
	}
	for _, oid := range inputs {
		if _, found := frozenOutputs[oid]; found {
			return fmt.Errorf("blocklist: input %s is frozen", oid.StringHex())
		}
	}
	return nil
}

func mustOutputIDs(hexIDs ...string) map[base.OutputID]struct{} {
	ret := make(map[base.OutputID]struct{}, len(hexIDs))
	for _, s := range hexIDs {
		oid, err := base.OutputIDFromHexString(s)
		if err != nil {
			panic(fmt.Errorf("blocklist: output %q: %w", s, err))
		}
		ret[oid] = struct{}{}
	}
	return ret
}

func mustHolderIDs(hexIDs ...string) map[base.HolderID]struct{} {
	ret := make(map[base.HolderID]struct{}, len(hexIDs))
	for _, s := range hexIDs {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != len(base.HolderID{}) {
			panic(fmt.Errorf("blocklist: holder %q is not a 32-byte hex", s))
		}
		ret[base.HolderID(b)] = struct{}{}
	}
	return ret
}
