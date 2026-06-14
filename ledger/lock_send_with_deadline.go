package ledger

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SendWithDeadlineLock is the typed wrapper for a `sendWithDeadline`-
// locked output. See claude/send_with_deadline_lock.md for the design.
//
// The (master, target) pair lives in the index-value tuple at output
// element index 1: position 0 = masterID (master-first §4.1 convention),
// position 1 = targetID (32-byte sigLock holderID, or 24-byte chainID per
// TargetType). The 3-arg public `sendWithDeadline` constraint at output
// element index 2 carries the policy (targetType, acceptanceSlots,
// cleanupSlots).
type SendWithDeadlineLock struct {
	MasterID        base.HolderID // sender / reclaim signer
	TargetID        base.HolderID // 32-byte sigLock holderID, or 24-byte chainID (first ChainIDLength bytes) per TargetType
	TargetType      byte          // SendWithDeadlineTargetSigLock | SendWithDeadlineTargetChainLock
	AcceptanceSlots uint32        // target's window, must be ≥ SendWithDeadlineMinAcceptanceSlots
	CleanupSlots    uint32        // cleanup boundary, must be in [AcceptanceSlots + SendWithDeadlineMinReclaimSlots, SendWithDeadlineMaxReclaimSlots]
}

const SendWithDeadlineLockName = "sendWithDeadline"

const (
	SendWithDeadlineTargetSigLock   byte = 0x00
	SendWithDeadlineTargetChainLock byte = 0x01
)

// Floors and the cleanup-deadline ceiling echoed in Go for wallet-side validation;
// the on-chain constraint enforces the same numbers (see
// constSendWithDeadlineMinAcceptanceSlots / constSendWithDeadlineMinReclaimSlots /
// constSendWithDeadlineMaxReclaimSlots in def/lock_send_with_deadline.easyfl).
// SendWithDeadlineMaxReclaimSlots caps cleanupSlots so dust (SWD outputs are exempt
// from the storage-deposit floor) becomes publicly claimable within ≈8.5h.
const (
	SendWithDeadlineMinAcceptanceSlots uint32 = 30
	SendWithDeadlineMinReclaimSlots    uint32 = 1000
	SendWithDeadlineMaxReclaimSlots    uint32 = 3000
)

//go:embed def/lock_send_with_deadline.easyfl
var sendWithDeadlineLockConstraintSource string

// targetType is encoded as a raw 1-byte 0xXX literal — easyfl has no
// u8/ typed-literal form, and a raw hex byte is exactly what the
// constraint reads via byte($0, 0).
const sendWithDeadlineTemplate = SendWithDeadlineLockName + "(0x%02x, u32/%d, u32/%d)"

func (l *SendWithDeadlineLock) Name() string { return SendWithDeadlineLockName }

func (l *SendWithDeadlineLock) Source() string {
	return fmt.Sprintf(sendWithDeadlineTemplate, l.TargetType, l.AcceptanceSlots, l.CleanupSlots)
}

func (l *SendWithDeadlineLock) LockBytecode() []byte {
	return mustBinFromSource(l.Source())
}

// targetIDBytes returns the meaningful target bytes: a 24-byte chainID for
// a chainLock target (stored in the first ChainIDLength bytes of TargetID),
// or the full 32-byte holderID for a sigLock target.
func (l *SendWithDeadlineLock) targetIDBytes() []byte {
	if l.TargetType == SendWithDeadlineTargetChainLock {
		return l.TargetID[:base.ChainIDLength]
	}
	return l.TargetID[:]
}

// IndexValues returns [masterID, targetID] — written at output element
// index 1, two trie index entries per sendWithDeadline output so both
// parties can find their pending sends via the standard indexer query.
func (l *SendWithDeadlineLock) IndexValues() [][]byte {
	return [][]byte{l.MasterID[:], l.targetIDBytes()}
}

func (l *SendWithDeadlineLock) String() string {
	kind := "sigLock"
	if l.TargetType == SendWithDeadlineTargetChainLock {
		kind = "chainLock"
	}
	return fmt.Sprintf("sendWithDeadline(master=%s, target=%s [%s], accept=%d slots, cleanup=%d slots)",
		hex.EncodeToString(l.MasterID[:]), hex.EncodeToString(l.targetIDBytes()),
		kind, l.AcceptanceSlots, l.CleanupSlots)
}

// SendWithDeadlineLockFromOutputElements rebuilds the typed lock from
// the two output elements (index-values at slot 1, bytecode at slot 2).
func SendWithDeadlineLockFromOutputElements(indexValuesBytes, lockBytecode []byte, lib *Library) (*SendWithDeadlineLock, error) {
	values, err := IndexValuesFromBytes(indexValuesBytes)
	if err != nil {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: %w", err)
	}
	if len(values) != 2 || len(values[0]) != 32 {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: expected master index value of 32 bytes")
	}
	sym, _, args, err := lib.ParseBytecodeOneLevel(lockBytecode, 3)
	if err != nil {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: %w", err)
	}
	if sym != SendWithDeadlineLockName {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: expected %s, got %s",
			SendWithDeadlineLockName, sym)
	}
	typeBytes := easyfl.StripDataPrefix(args[0])
	if len(typeBytes) != 1 {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: targetType must be 1 byte, got %d", len(typeBytes))
	}
	acceptBytes := easyfl.StripDataPrefix(args[1])
	if len(acceptBytes) != 4 {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: acceptanceSlots must be 4 bytes, got %d", len(acceptBytes))
	}
	cleanupBytes := easyfl.StripDataPrefix(args[2])
	if len(cleanupBytes) != 4 {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: cleanupSlots must be 4 bytes, got %d", len(cleanupBytes))
	}

	ret := &SendWithDeadlineLock{
		TargetType:      typeBytes[0],
		AcceptanceSlots: binary.BigEndian.Uint32(acceptBytes),
		CleanupSlots:    binary.BigEndian.Uint32(cleanupBytes),
	}
	copy(ret.MasterID[:], values[0])
	// target is a 24-byte chainID for a chainLock target, a 32-byte holderID otherwise
	expectedTargetLen := 32
	if ret.TargetType == SendWithDeadlineTargetChainLock {
		expectedTargetLen = base.ChainIDLength
	}
	if len(values[1]) != expectedTargetLen {
		return nil, fmt.Errorf("SendWithDeadlineLockFromOutputElements: target index value must be %d bytes, got %d", expectedTargetLen, len(values[1]))
	}
	copy(ret.TargetID[:], values[1])
	return ret, nil
}

// NewSendWithDeadlineOutput builds an output of given amount locked by
// the typed SendWithDeadlineLock instance.
func NewSendWithDeadlineOutput(amount uint64, l *SendWithDeadlineLock) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithTokenBalance(amount)
		o.WithLock(l)
	})
}

// registerSendWithDeadlineLock registers the lock with the library at
// upgrade-0 time. Public symbol is a 3-arg call (targetType,
// acceptanceSlots, cleanupSlots) so it can't use the 0-arg
// `registerLockKind` helper used by sigLock / chainLock / tagAlong.
// Mirrors `registerHTLCLock`.
func registerSendWithDeadlineLock(lib *Library) {
	lib.mustRegisterConstraint(SendWithDeadlineLockName, 3, func(data []byte) (Constraint, error) {
		return &lockKindMarker{name: SendWithDeadlineLockName, bytecode: bytes.Clone(data)}, nil
	})
}
