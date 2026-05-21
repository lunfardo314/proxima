package txbuildercore_test

// Tests for the wallet-side delegation-output parser + Constants
// epoch math. The parser/parity-check pair is what lets proxi
// node killchain do its frozen-slot UX guard without the ledger
// singleton (claude/wallet_eval_api.md Phase C-style).

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// chainIDFixture builds a fixed-but-non-trivial ChainID — first 4
// bytes form a recognisable BE uint32 so the epoch-offset math is
// easy to eyeball.
func chainIDFixture() base.ChainID {
	var id base.ChainID
	id[0] = 0x00
	id[1] = 0x01
	id[2] = 0x02
	id[3] = 0x03 // BE uint32 = 0x00010203 = 66051
	for i := 4; i < len(id); i++ {
		id[i] = byte(i)
	}
	return id
}

// TestConstants_EpochMath_Parity verifies the wallet-side
// EpochOffsetSlotsDirect / EpochLimits / LastSlotInEpochDirect
// produce the same values as the server-side ledger.Constants methods
// at a few representative inputs.
func TestConstants_EpochMath_Parity(t *testing.T) {
	target := chainIDFixture()
	walletC := ledger.L(base.MaxSlot).Constants.ToWalletConstants()
	serverC := &ledger.L(base.MaxSlot).Constants

	cases := []struct {
		name       string
		epoch      uint32
		epochSlots uint32
	}{
		{"epoch 0", 0, 600},
		{"epoch 1", 1, 600},
		{"epoch 42", 42, 600},
		{"epochSlots prime", 17, 997},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t,
				serverC.EpochOffsetSlotsDirect(target, c.epochSlots),
				walletC.EpochOffsetSlotsDirect(target, c.epochSlots),
				"EpochOffsetSlotsDirect")

			wantFirst, wantLast := serverC.EpochLimits(target, c.epoch, c.epochSlots)
			gotFirst, gotLast := walletC.EpochLimits(target, c.epoch, c.epochSlots)
			require.Equal(t, wantFirst, gotFirst, "EpochLimits firstSlot")
			require.Equal(t, wantLast, gotLast, "EpochLimits lastSlot")

			require.Equal(t,
				serverC.LastSlotInEpochDirect(target, c.epoch, c.epochSlots),
				walletC.LastSlotInEpochDirect(target, c.epoch, c.epochSlots),
				"LastSlotInEpochDirect")
		})
	}
}

// TestParseDelegationOutput_FromInit builds a known delegation-init
// output via ledger.MakeDelegationInitOutput, parses the bytes with
// the wallet helper, and verifies the recovered fields match.
func TestParseDelegationOutput_FromInit(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	target := chainIDFixture()
	var master base.HolderID
	for i := range master {
		master[i] = byte(i + 100)
	}
	const (
		amount                 uint64 = 5_000_000
		maxFrozen              byte   = 16
		requiredShare          uint16 = 850
		startSlot              uint32 = 1234
		epochSlots             uint32 = 600
		targetMaxFrozenEpochs  byte   = 32
	)
	serverOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:                 amount,
		MasterID:               master,
		Target:                 target,
		MaxFrozenEpochs:        maxFrozen,
		RequiredInflationShare: requiredShare,
		StartSlot:              startSlot,
		EpochSlots:             epochSlots,
		TargetMaxFrozenEpochs:  targetMaxFrozenEpochs,
	})

	// Synthesise an OutputID with a known creation slot so the
	// wallet view's OriginSlot is testable.
	oid := outputIDAtSlot(startSlot)

	walletOut, err := txbuildercore.OutputFromBytes(serverOut.Bytes())
	require.NoError(t, err)
	view, ok, err := lib.ParseDelegationOutput(walletOut, oid)
	require.NoError(t, err)
	require.True(t, ok, "init output must parse as a delegation output")
	require.Equal(t, startSlot, view.OriginSlot)
	require.Equal(t, target, view.Target)
	require.Equal(t, epochSlots, view.EpochSlots)
	// Init output starts at the zero state.
	require.Equal(t, uint32(0), view.LastFrozenEpoch)
	require.Equal(t, byte(0), view.State)
}

// TestParseDelegationOutput_NonDelegationReturnsFalse confirms the
// helper short-circuits without parsing when the output's lock isn't
// delegateLock.
func TestParseDelegationOutput_NonDelegationReturnsFalse(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	var holder base.HolderID
	for i := range holder {
		holder[i] = byte(i + 1)
	}
	sigOut, err := txbuildercore.NewSigLockOutput(lib, 1_000_000, holder)
	require.NoError(t, err)
	view, ok, err := lib.ParseDelegationOutput(sigOut, outputIDAtSlot(7))
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, view)
}

// TestDelegationOutputView_IsInFrozenSlot_Parity builds a synthetic
// frozen delegation output (server-side), parses it with the wallet
// helper, and checks IsInFrozenSlot returns the same value as the
// server-side DelegationOutput.IsInFrozenSlot at sampled slots
// around the freeze boundary.
func TestDelegationOutputView_IsInFrozenSlot_Parity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	walletC := ledger.L(base.MaxSlot).Constants.ToWalletConstants()

	target := chainIDFixture()
	var master base.HolderID
	for i := range master {
		master[i] = byte(i + 50)
	}
	const (
		amount          uint64 = 5_000_000
		startSlot       uint32 = 1234
		epochSlots      uint32 = 600
		lastFrozenEpoch uint32 = 4
	)
	// Build the init output, then swap its zero state for a Frozen
	// state by re-serialising via the typed-API hooks (the public
	// MakeDelegationInitOutput emits a zero state, so we construct
	// the full output manually with the desired state).
	swapped := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount))
		o.WithLock(ledger.NewDelegateLock(target, master, 16, 850, epochSlots, 32))
		o.PutConstraint(ledger.NewChainOrigin(startSlot).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{
			LastFrozenEpoch: lastFrozenEpoch,
			State:           ledger.DelegateLockStateFrozen,
		}.Bytes())
	})
	oid := outputIDAtSlot(startSlot)

	// Server parse.
	dOut, ok := ledger.AsDelegationOutput(swapped, oid)
	require.True(t, ok)

	// Wallet parse.
	walletOut, err := txbuildercore.OutputFromBytes(swapped.Bytes())
	require.NoError(t, err)
	view, ok, err := lib.ParseDelegationOutput(walletOut, oid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, lastFrozenEpoch, view.LastFrozenEpoch)
	require.Equal(t, ledger.DelegateLockStateFrozen, view.State)

	// Sample slots around the freeze boundary (start, mid-epoch,
	// last slot of epoch, first slot after, several epochs later).
	lastSlot := ledger.L(base.MaxSlot).LastSlotInEpochDirect(target, lastFrozenEpoch, epochSlots)
	sample := []uint32{
		startSlot,         // creation slot — both sides false
		startSlot + 1,     // inside frozen window
		lastSlot - 1,      // still frozen
		lastSlot,          // last frozen slot — both sides true
		lastSlot + 1,      // first non-frozen — both sides false
		lastSlot + 1000,   // well past
	}
	for _, s := range sample {
		require.Equal(t, dOut.IsInFrozenSlot(s), view.IsInFrozenSlot(s, walletC),
			"IsInFrozenSlot parity at slot %d", s)
	}

	// UnfreezeSlot parity (LastFrozenEpoch + 1).
	require.Equal(t, dOut.UnfreezeSlot(), view.UnfreezeSlot(walletC), "UnfreezeSlot parity")
}

// outputIDAtSlot returns a synthetic OutputID whose Slot() == slot.
// Other bytes are zero — enough for the helpers under test, which
// only read the slot.
func outputIDAtSlot(slot uint32) base.OutputID {
	var oid base.OutputID
	ts := base.T(slot, 5)
	tsBytes := ts.Bytes()
	copy(oid[:len(tsBytes)], tsBytes)
	return oid
}
