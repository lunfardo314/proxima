package txbuildercore_test

// Byte-identity tests for the sendWithDeadline wallet helpers — the
// wallet-side composer must emit bytes server-side parsers accept.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestNewSendWithDeadlineLockBytecode_ByteIdentity exercises the
// 3-arg bytecode helper across the two target-type values, against
// (*ledger.SendWithDeadlineLock).LockBytecode().
func TestNewSendWithDeadlineLockBytecode_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var master, target base.HolderID
	for i := range master {
		master[i] = byte(i + 1)
	}
	for i := range target {
		target[i] = byte(i + 100)
	}

	cases := []struct {
		name       string
		targetType byte
		accept     uint32
		cleanup    uint32
	}{
		{"sigLock target", txbuildercore.SendWithDeadlineTargetSigLock, 60, 8000},
		{"chainLock target", txbuildercore.SendWithDeadlineTargetChainLock, 30, 1030},
		{"large windows", txbuildercore.SendWithDeadlineTargetSigLock, 1_000_000, 2_000_000},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			walletBin, err := lib.NewSendWithDeadlineLockBytecode(c.targetType, c.accept, c.cleanup)
			require.NoError(t, err)

			serverBin := (&ledger.SendWithDeadlineLock{
				MasterID:        master,
				TargetID:        target,
				TargetType:      c.targetType,
				AcceptanceSlots: c.accept,
				CleanupSlots:    c.cleanup,
			}).LockBytecode()
			require.Equal(t, serverBin, walletBin)
		})
	}
}

// TestNewSendWithDeadlineOutput_ByteIdentity verifies the full
// composer (amounts + index-values + lock) matches the
// ledger.NewOutput(o.WithTokenBalance(...).WithLock(swd)) path.
func TestNewSendWithDeadlineOutput_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var master, target base.HolderID
	for i := range master {
		master[i] = byte(i + 1)
	}
	for i := range target {
		target[i] = byte(i + 100)
	}

	cases := []struct {
		name       string
		amount     uint64
		targetType byte
		accept     uint32
		cleanup    uint32
	}{
		{"sigLock target small", 1_000_000, txbuildercore.SendWithDeadlineTargetSigLock, 60, 8000},
		{"chainLock target", 50_000_000, txbuildercore.SendWithDeadlineTargetChainLock, 30, 1030},
		{"large amount + windows", 1_234_567_890, txbuildercore.SendWithDeadlineTargetSigLock, 1_000_000, 2_000_000},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			walletOut, err := lib.NewSendWithDeadlineOutput(txbuildercore.SendWithDeadlineOutputParams{
				Amount:          c.amount,
				MasterID:        master,
				TargetID:        target,
				TargetType:      c.targetType,
				AcceptanceSlots: c.accept,
				CleanupSlots:    c.cleanup,
			})
			require.NoError(t, err)

			swd := &ledger.SendWithDeadlineLock{
				MasterID:        master,
				TargetID:        target,
				TargetType:      c.targetType,
				AcceptanceSlots: c.accept,
				CleanupSlots:    c.cleanup,
			}
			serverOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(c.amount).WithLock(swd)
			})
			require.Equal(t, serverOut.Bytes(), walletOut.Bytes(), "case: %s", c.name)
		})
	}
}
