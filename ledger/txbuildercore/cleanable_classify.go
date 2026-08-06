package txbuildercore

import (
	"encoding/binary"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
)

// CleanClass categorises whether an output has decayed all the way to the
// public window of its conditional lock — the state in which ANY signer may
// consume it — and whether taking it is a plain sweep.
//
// This is the complement of SpendClass: SpendClass answers "can this account
// claim it by role", CleanClass answers "has it stopped belonging to anyone in
// particular". Both are computed from raw output bytes so the node's scan and
// the wallet agree on what dust is.
type CleanClass int

const (
	// CleanNotPublic — the output is not (yet) claimable by anybody: an
	// unconditional lock, or a conditional one whose public deadline has not
	// passed at targetSlot. Never touch these; they still belong to someone.
	CleanNotPublic CleanClass = iota

	// CleanSimple — publicly claimable and consumable with a plain signature
	// unlock producing nothing in return.
	CleanSimple

	// CleanNeedsReturn — publicly claimable, but the output carries
	// returnToSender, which discriminates on the SIGNER rather than on the
	// window: anyone other than the master must still pay the return receipt
	// in the same transaction. Cleanable only by a flow that builds receipts.
	CleanNeedsReturn

	// CleanUnknown — publicly claimable by its lock, but carrying additional
	// constraints the wallet does not recognise, so the consume structure is
	// unknown. Leave alone.
	CleanUnknown
)

// ClassifyCleanable reports whether the output at createSlot has fallen into
// its lock's public window by targetSlot.
//
// Covers the two conditional locks that have such a window:
//   - sendWithDeadline, public once Δ ≥ cleanupSlots (read off the lock's own
//     arguments, so each output carries its own deadline);
//   - tagAlong, public once Δ ≥ tagAlongReclaimSlots, which is a ledger
//     constant and therefore supplied by the caller.
//
// Everything else — sigLock, chainLock, delegateLock, chained outputs — is
// CleanNotPublic by construction: those locks have no window after which they
// stop having an owner.
func ClassifyCleanable(parser BytecodeParser, utxoBytes []byte, createSlot, targetSlot, tagAlongReclaimSlots uint32) (CleanClass, error) {
	if targetSlot < createSlot {
		return CleanNotPublic, nil
	}
	delta := targetSlot - createSlot

	o, err := OutputFromBytes(utxoBytes)
	if err != nil {
		return CleanNotPublic, err
	}
	lockBin := o.MustConstraintAt(ConstraintIndexLock)
	sym, _, args, err := parser.ParseBytecodeOneLevel(lockBin)
	if err != nil {
		return CleanNotPublic, err
	}

	switch sym {
	case lockSymSWD:
		cleanupSlots, ok := swdCleanupSlots(args)
		if !ok || delta < cleanupSlots {
			return CleanNotPublic, nil
		}
	case TagAlongLockName:
		if tagAlongReclaimSlots == 0 || delta < tagAlongReclaimSlots {
			return CleanNotPublic, nil
		}
	default:
		return CleanNotPublic, nil
	}

	hasReturnToSender, hasUnknownExtra := scanAdditionalConstraints(parser, o)
	switch {
	case hasUnknownExtra:
		return CleanUnknown, nil
	case hasReturnToSender:
		return CleanNeedsReturn, nil
	}
	return CleanSimple, nil
}

// swdCleanupSlots reads cleanupSlots — the third sendWithDeadline lock
// argument, after targetType and acceptanceSlots.
func swdCleanupSlots(args [][]byte) (uint32, bool) {
	if len(args) < 3 {
		return 0, false
	}
	b := easyfl.StripDataPrefix(args[2])
	if len(b) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b), true
}

// CleanableOutput is one piece of publicly-claimable dust found by a scan.
type CleanableOutput struct {
	ID          base.OutputID
	OutputBytes []byte
}
