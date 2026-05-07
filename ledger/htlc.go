package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
)

// HTLC is the typed wrapper for an htlc-locked output. Both 32-byte
// indexable values live in the index-value tuple at output element
// index 1: position 0 = reclaim holderID, position 1 = hash. The
// deadline slot is carried as a 1-arg of the public `htlc` symbol at
// output element index 2.
type HTLC struct {
	HolderID base.HolderID // reclaim holder (timeout path signer)
	Hash     [32]byte      // blake2b(preimage)
	Deadline uint32        // ledger slot at which the timeout path opens
}

const (
	HTLCName     = "htlc"
	htlcTemplate = HTLCName + "(u32/%d)"
)

func (h *HTLC) Name() string { return HTLCName }

func (h *HTLC) Source() string {
	return fmt.Sprintf(htlcTemplate, h.Deadline)
}

// LockBytecode compiles `htlc(u32/<deadline>)` for this instance. The
// holder/hash do NOT appear in the bytecode — they live in the index-value
// tuple at output element index 1.
func (h *HTLC) LockBytecode() []byte {
	return mustBinFromSource(h.Source())
}

// IndexValues returns [holderID, hash] — written at output element
// index 1, two trie index entries per htlc-locked UTXO.
func (h *HTLC) IndexValues() [][]byte {
	return [][]byte{h.HolderID[:], h.Hash[:]}
}

func (h *HTLC) String() string {
	return fmt.Sprintf("htlc(holder=%s, hash=%s, deadline=%d)",
		hex.EncodeToString(h.HolderID[:]), hex.EncodeToString(h.Hash[:]), h.Deadline)
}

// HTLCFromOutputElements rebuilds an HTLC from the two output elements
// (index-value tuple at output[1] and lock bytecode at output[2]).
func HTLCFromOutputElements(indexValuesBytes, lockBytecode []byte, lib *Library) (*HTLC, error) {
	values, err := IndexValuesFromBytes(indexValuesBytes)
	if err != nil {
		return nil, fmt.Errorf("HTLCFromOutputElements: %w", err)
	}
	if len(values) != 2 || len(values[0]) != 32 || len(values[1]) != 32 {
		return nil, fmt.Errorf("HTLCFromOutputElements: expected 2 index values of 32 bytes each")
	}
	sym, _, args, err := lib.ParseBytecodeOneLevel(lockBytecode, 1)
	if err != nil {
		return nil, fmt.Errorf("HTLCFromOutputElements: %w", err)
	}
	if sym != HTLCName {
		return nil, fmt.Errorf("HTLCFromOutputElements: expected %s, got %s", HTLCName, sym)
	}
	deadlineBin, err := base.SlotFromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, fmt.Errorf("HTLCFromOutputElements: %w", err)
	}
	ret := &HTLC{Deadline: uint32(deadlineBin)}
	copy(ret.HolderID[:], values[0])
	copy(ret.Hash[:], values[1])
	return ret, nil
}

// htlc is a 1-arg public symbol (the deadline slot) so it cannot ride the
// 0-arg `registerLockKind` helper used by sigLock/chainLock/tagAlong.
// Registers a marker for `ConstraintFromBytesWithLib`; full reconstruction
// (including index-values) goes through `LockFromOutputElementsWithLib`.
func registerHTLCLock(lib *Library) {
	lib.mustRegisterConstraint(HTLCName, 1, func(data []byte) (Constraint, error) {
		return &lockKindMarker{name: HTLCName, bytecode: bytes.Clone(data)}, nil
	})
}
