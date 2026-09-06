package global

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// White-box test of the sync-target registry reap/cooldown lifecycle — the fix
// for the audit FCORE-1 finding (a target that never commits used to pin the
// node in permanent sync mode because nothing removed it).
func TestSyncTargetReapCooldown(t *testing.T) {
	// isolate from any state other tests in the process left behind
	syncTargetsMutex.Lock()
	syncTargets = map[base.TransactionID]struct{}{}
	reapedTargets = map[base.TransactionID]time.Time{}
	syncTargetsMutex.Unlock()

	id := base.RandomTransactionID(true, 1) // a branch txid

	// add -> pending
	require.True(t, AddSyncTarget(id))
	require.False(t, AddSyncTarget(id), "idempotent")
	require.True(t, SyncTargetsPending())

	// reap -> gone, and refused re-registration during cooldown
	require.True(t, ReapSyncTarget(id))
	require.False(t, SyncTargetsPending(), "reaped target must drain the registry")
	require.False(t, AddSyncTarget(id), "re-add during cooldown must be refused")
	require.False(t, SyncTargetsPending(), "still empty — the lying/withheld target cannot re-pin")

	// simulate cooldown expiry (white-box) -> re-add allowed again
	syncTargetsMutex.Lock()
	reapedTargets[id] = time.Now().Add(-SyncTargetReapCooldown - time.Second)
	syncTargetsMutex.Unlock()
	require.True(t, AddSyncTarget(id), "after cooldown a genuine target may be re-adopted")
	require.True(t, SyncTargetsPending())

	// committing (RemoveSyncTarget) clears any cooldown so a reachable target is unaffected
	require.True(t, RemoveSyncTarget(id))
	require.False(t, SyncTargetsPending())
	require.True(t, AddSyncTarget(id), "no lingering cooldown after a successful commit")

	// cleanup
	syncTargetsMutex.Lock()
	syncTargets = map[base.TransactionID]struct{}{}
	reapedTargets = map[base.TransactionID]time.Time{}
	syncTargetsMutex.Unlock()
}
