// Package blocklist is node policy, not a ledger rule: it freezes listed
// outputs and listed signers by making invalid every transaction that consumes
// such an output or is signed by such a holder. It only rejects, so nodes
// holding the coverage majority enforce it for the network.
package blocklist

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
)

// ActivationSlot is the first slot whose transactions are checked for a frozen
// signer: listed keys signed history committed before the list was cut, which
// must replay and reattach unchanged. Set to a slot by which every enforcing
// node runs this list. Frozen outputs are checked at every slot: each was
// unspent when the list was cut, so no committed transaction spends one, and a
// spend back-dated before this slot is refused as well.
const ActivationSlot = 254500

// ErrFrozen marks a refusal by this policy. It condemns the transaction ID, not
// the bytes: the ID commits to the inputs and, from library upgrade 6, to the
// signer.
var ErrFrozen = errors.New("blocklist")

var (
	frozenOutputs = mustOutputIDs(frozenOutputIDs...)
	frozenHolders = mustHolderIDs(frozenHolderIDs...)
)

// CheckInputs returns an error when a transaction consumes a frozen output.
func CheckInputs(inputs []base.OutputID) error {
	for _, oid := range inputs {
		if _, found := frozenOutputs[oid]; found {
			return fmt.Errorf("%w: input %s is frozen", ErrFrozen, oid.StringHex())
		}
	}
	return nil
}

// CheckSigner returns an error when the signer is a frozen holder. Applies to
// transactions from ActivationSlot on.
func CheckSigner(signer base.HolderID) error {
	if _, found := frozenHolders[signer]; found {
		return fmt.Errorf("%w: signer %s is frozen", ErrFrozen, hex.EncodeToString(signer[:]))
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
