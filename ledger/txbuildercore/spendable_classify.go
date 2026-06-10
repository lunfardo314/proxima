package txbuildercore

import (
	"bytes"
	"encoding/binary"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SpendClass categorises HOW (and whether) a given account can claim an
// output at a given ledger slot, considering BOTH the lock AND any
// additional constraints on the output. It is computed purely from the raw
// output bytes + a bytecode parser (singleton-free), so the same classifier
// runs wallet-side (proxi) and node-side (the get_outputs spendable filter).
//
// A "claim" here means a single-input signature unlock by accountHID. Only
// SpendSimple is consumable in a plain sweep; the other recognised-but-
// constrained cases are surfaced so callers can decide what to do.
type SpendClass int

const (
	// SpendNotForAccount — accountHID has no claim on this output at
	// targetSlot: wrong holder, SWD window closed for the account's role,
	// chainLock-target SWD (needs a chain input), unrecognised lock, or
	// malformed bytes.
	SpendNotForAccount SpendClass = iota

	// SpendSimple — claimable with a plain single-input signature unlock,
	// producing no extra output:
	//   - a 3-element sigLock(accountHID) output;
	//   - an SWD master-reclaim (Δ ≥ acceptanceSlots), optionally carrying
	//     returnToSender (a noop when the master spends);
	//   - an SWD sigLock-target accept (Δ < acceptanceSlots) with no
	//     additional constraints.
	SpendSimple

	// SpendNeedsReturn — an SWD sigLock-target accept (Δ < acceptanceSlots)
	// carrying a returnToSender constraint. Claimable, but ONLY by also
	// producing the return receipt to the master in the same tx — so it
	// can't go into a plain sweep.
	SpendNeedsReturn

	// SpendUnknown — accountHID has a lock-level claim (sig holder match or
	// SWD master/target window) but the output carries additional
	// constraints the wallet doesn't recognise, so the claim structure is
	// unknown. Callers building a simple sweep must skip these.
	SpendUnknown
)

// BytecodeParser is the minimal library surface ClassifySpendable needs.
// Both *ledger.Library and *txbuildercore.Library[T] satisfy it.
type BytecodeParser interface {
	ParseBytecodeOneLevel(code []byte, expectedNumArgs ...int) (string, []byte, [][]byte, error)
}

// ClassifySpendable classifies how accountHID can claim the output at
// targetSlot. createSlot is the slot of the output's ID (used for the SWD
// Δ = targetSlot − createSlot window check). A non-nil error is returned
// only for malformed output bytes; ambiguous cases map to a SpendClass.
func ClassifySpendable(parser BytecodeParser, utxoBytes []byte, createSlot uint32, accountHID base.HolderID, targetSlot uint32) (SpendClass, error) {
	o, err := OutputFromBytes(utxoBytes)
	if err != nil {
		return SpendNotForAccount, err
	}
	lockBin := o.MustConstraintAt(ConstraintIndexLock)
	sym, _, args, err := parser.ParseBytecodeOneLevel(lockBin)
	if err != nil {
		return SpendNotForAccount, err
	}

	hasReturnToSender, hasUnknownExtra := scanAdditionalConstraints(parser, o)

	switch sym {
	case lockSymSig:
		// Plain sigLock to the account is the canonical 3-element shape.
		// Any extra constraint (chain, an inline literal from a return
		// receipt, …) makes the claim structure non-canonical → Unknown.
		if !bytes.Equal(sigLockHolder(o), accountHID[:]) {
			return SpendNotForAccount, nil
		}
		if hasReturnToSender || hasUnknownExtra || o.NumElements() != 3 {
			return SpendUnknown, nil
		}
		return SpendSimple, nil

	case lockSymSWD:
		role, ok := swdRoleForAccount(o, args, accountHID, createSlot, targetSlot)
		if !ok {
			return SpendNotForAccount, nil
		}
		if hasUnknownExtra {
			return SpendUnknown, nil
		}
		switch role {
		case swdRoleMaster:
			// returnToSender is a noop when the master reclaims.
			return SpendSimple, nil
		default: // swdRoleTarget
			if hasReturnToSender {
				return SpendNeedsReturn, nil
			}
			return SpendSimple, nil
		}
	}
	return SpendNotForAccount, nil
}

// sigLockHolder returns the holderID at index-values[0] (where sigLock reads
// its holder), or nil if absent/malformed.
func sigLockHolder(o *Output) []byte {
	ivBin, err := o.ConstraintAt(ConstraintIndexIndexValues)
	if err != nil {
		return nil
	}
	values, err := DecodeIndexValuesTuple(ivBin)
	if err != nil || len(values) < 1 {
		return nil
	}
	return values[0]
}

type swdRole int

const (
	swdRoleMaster swdRole = iota
	swdRoleTarget
)

// swdRoleForAccount determines whether accountHID can claim a sendWithDeadline
// output at targetSlot, and in which role. Mirrors the on-chain windows:
//   - master reclaim: accountHID == masterID AND Δ ≥ acceptanceSlots;
//   - sigLock-target accept: accountHID == targetID, targetType == sigLock,
//     AND Δ < acceptanceSlots.
//
// args are the lock's parsed call args (targetType, acceptanceSlots, cleanup).
func swdRoleForAccount(o *Output, args [][]byte, accountHID base.HolderID, createSlot, targetSlot uint32) (swdRole, bool) {
	if targetSlot < createSlot {
		return 0, false
	}
	delta := targetSlot - createSlot

	ivBin, err := o.ConstraintAt(ConstraintIndexIndexValues)
	if err != nil {
		return 0, false
	}
	values, err := DecodeIndexValuesTuple(ivBin)
	if err != nil || len(values) < 2 || len(values[0]) != 32 {
		return 0, false
	}
	if len(args) < 2 {
		return 0, false
	}
	acc := easyfl.StripDataPrefix(args[1])
	if len(acc) != 4 {
		return 0, false
	}
	acceptanceSlots := binary.BigEndian.Uint32(acc)

	if bytes.Equal(values[0], accountHID[:]) {
		if delta >= acceptanceSlots {
			return swdRoleMaster, true
		}
		return 0, false
	}
	tt := easyfl.StripDataPrefix(args[0])
	if len(tt) == 1 && tt[0] == swdTargetTypeSigLock && bytes.Equal(values[1], accountHID[:]) && delta < acceptanceSlots {
		return swdRoleTarget, true
	}
	return 0, false
}

// scanAdditionalConstraints inspects the output's constraint slots beyond the
// framework triple (amounts/indexValues/lock, indices 0..2). It reports
// whether a returnToSender constraint is present and whether any non-empty
// extra is unrecognised. Empty padding slots are ignored.
func scanAdditionalConstraints(parser BytecodeParser, o *Output) (hasReturnToSender, hasUnknownExtra bool) {
	for i := int(ConstraintIndexChain); i < o.NumElements(); i++ {
		elem, err := o.ConstraintAt(byte(i))
		if err != nil || len(elem) == 0 {
			continue
		}
		sym, _, _, perr := parser.ParseBytecodeOneLevel(elem)
		if perr == nil && sym == ReturnToSenderName {
			hasReturnToSender = true
			continue
		}
		hasUnknownExtra = true
	}
	return
}
