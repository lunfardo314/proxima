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
// the wallet helper, and verifies the recovered fields match. Covers
// both the "delegator picked a custom max" branch and the "delegator
// passed 0 → fall back to target's" branch.
func TestParseDelegationOutput_FromInit(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	target := chainIDFixture()
	var master base.HolderID
	for i := range master {
		master[i] = byte(i + 100)
	}

	const (
		amount                uint64 = 5_000_000
		requiredShare         uint16 = 850
		startSlot             uint32 = 1234
		epochSlots            uint32 = 600
		targetMaxFrozenEpochs byte   = 32
	)
	cases := []struct {
		name      string
		maxFrozen byte // delegator's chosen max; 0 → falls back to target's
		expect    byte // expected DelegationOutputView.MaxFrozenEpochs
	}{
		{"custom max", 16, 16},
		{"zero → target's", 0, targetMaxFrozenEpochs},
		{"== target's → also stored as target's", targetMaxFrozenEpochs, targetMaxFrozenEpochs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			serverOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
				Amount:                 amount,
				MasterID:               master,
				Target:                 target,
				MaxFrozenEpochs:        c.maxFrozen,
				RequiredInflationShare: requiredShare,
				StartSlot:              startSlot,
				EpochSlots:             epochSlots,
				TargetMaxFrozenEpochs:  targetMaxFrozenEpochs,
			})
			oid := outputIDAtSlot(startSlot)
			walletOut, err := txbuildercore.OutputFromBytes(serverOut.Bytes())
			require.NoError(t, err)
			view, ok, err := lib.ParseDelegationOutput(walletOut, oid)
			require.NoError(t, err)
			require.True(t, ok, "init output must parse as a delegation output")
			require.Equal(t, startSlot, view.OriginSlot)
			require.Equal(t, master, view.MasterID)
			require.Equal(t, target, view.Target)
			require.Equal(t, c.expect, view.MaxFrozenEpochs)
			require.Equal(t, epochSlots, view.EpochSlots)
			// Init output starts at the zero state.
			require.Equal(t, uint32(0), view.LastFrozenEpoch)
			require.Equal(t, byte(0), view.State)
			// Init output: chain constraint carries NilChainID; the
			// view fills in MakeOriginChainID(oid).
			require.Equal(t, base.MakeOriginChainID(oid), view.ChainID)
		})
	}
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

// TestConstants_CoveredSlotsInCurrentEpoch_Parity samples slots inside
// and at the boundaries of a couple of epochs.
func TestConstants_CoveredSlotsInCurrentEpoch_Parity(t *testing.T) {
	target := chainIDFixture()
	walletC := ledger.L(base.MaxSlot).Constants.ToWalletConstants()
	serverC := &ledger.L(base.MaxSlot).Constants

	const epochSlots uint32 = 600
	offs := serverC.EpochOffsetSlotsDirect(target, epochSlots)
	for _, s := range []uint32{
		0, 1, offs, offs + 1, offs + epochSlots/2, offs + epochSlots,
		offs + epochSlots + 1, offs + epochSlots*42 + 17,
	} {
		require.Equal(t,
			serverC.CoveredSlotsInCurrentEpoch(target, s, epochSlots),
			walletC.CoveredSlotsInCurrentEpoch(target, s, epochSlots),
			"slot %d", s)
	}
}

// TestConstants_FrozenSlotsFromFrozenEpochs_Parity covers the
// (frozenEpochs, txSlot) matrix.
func TestConstants_FrozenSlotsFromFrozenEpochs_Parity(t *testing.T) {
	target := chainIDFixture()
	walletC := ledger.L(base.MaxSlot).Constants.ToWalletConstants()
	serverC := &ledger.L(base.MaxSlot).Constants

	const epochSlots uint32 = 600
	offs := serverC.EpochOffsetSlotsDirect(target, epochSlots)
	for _, fe := range []byte{1, 2, 4, 16, 32} {
		for _, s := range []uint32{
			1, offs + 1, offs + epochSlots/2, offs + epochSlots, offs + epochSlots*7,
		} {
			require.Equal(t,
				serverC.FrozenSlotsFromFrozenEpochs(target, s, epochSlots, fe),
				walletC.FrozenSlotsFromFrozenEpochs(target, s, epochSlots, fe),
				"fe=%d slot=%d", fe, s)
		}
	}
}

// TestConstants_EpochFromSlotDirect_Parity covers the wallet-side
// EpochFromSlotDirect against the server-side version at a range of
// slots including the boundary cases (slot ≤ offset, slot in epoch
// 0, slot right at the first boundary, slot deep in a later epoch).
func TestConstants_EpochFromSlotDirect_Parity(t *testing.T) {
	target := chainIDFixture()
	walletC := ledger.L(base.MaxSlot).Constants.ToWalletConstants()
	serverC := &ledger.L(base.MaxSlot).Constants

	const epochSlots uint32 = 600
	offs := serverC.EpochOffsetSlotsDirect(target, epochSlots)
	slots := []uint32{
		0,
		offs,                  // boundary: still in epoch 0
		offs + 1,              // first slot of epoch 1
		offs + epochSlots,     // last slot of epoch 1
		offs + epochSlots + 1, // first slot of epoch 2
		offs + epochSlots*42 + 17,
	}
	for _, s := range slots {
		require.Equal(t,
			serverC.EpochFromSlotDirect(target, s, epochSlots),
			walletC.EpochFromSlotDirect(target, s, epochSlots),
			"slot %d", s)
	}
}

// TestDelegationOutputView_SafeRevocationWindow_Parity builds a
// frozen delegation, parses it, and verifies both the window endpoints
// and the IsInSafeRevocationWindow predicate match the server-side
// computation across sampled slots.
func TestDelegationOutputView_SafeRevocationWindow_Parity(t *testing.T) {
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

	dOut, ok := ledger.AsDelegationOutput(swapped, oid)
	require.True(t, ok)

	walletOut, err := txbuildercore.OutputFromBytes(swapped.Bytes())
	require.NoError(t, err)
	view, ok, err := lib.ParseDelegationOutput(walletOut, oid)
	require.NoError(t, err)
	require.True(t, ok)

	// Window endpoint parity.
	wantFrom, wantTo, wantApplicable := dOut.SafeRevocationWindow()
	gotFrom, gotTo, gotApplicable := view.SafeRevocationWindow(walletC)
	require.Equal(t, wantApplicable, gotApplicable)
	require.Equal(t, wantFrom, gotFrom)
	require.Equal(t, wantTo, gotTo)
	require.True(t, gotApplicable, "frozen output must have an applicable safe-revocation window")

	// Sample slots around the window edges.
	for _, s := range []uint32{
		gotFrom - 1,
		gotFrom,         // first slot in window
		gotFrom + 1,
		(gotFrom + gotTo) / 2,
		gotTo,           // last slot in window
		gotTo + 1,
		gotTo + 100,
	} {
		require.Equal(t, dOut.IsInSafeRevocationWindow(s), view.IsInSafeRevocationWindow(s, walletC),
			"IsInSafeRevocationWindow parity at slot %d", s)
	}

	// State convenience aliases.
	require.True(t, view.IsMarkedFrozen())
	require.False(t, view.IsMarkedOnHold())
}

// TestDelegationOutputView_SafeRevocationWindow_NotApplicable returns
// (0, 0, false) when the output's state isn't Frozen. Sampled across
// the zero state and the OnHold state.
func TestDelegationOutputView_SafeRevocationWindow_NotApplicable(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	walletC := ledger.L(base.MaxSlot).Constants.ToWalletConstants()

	target := chainIDFixture()
	var master base.HolderID
	for i := range master {
		master[i] = byte(i + 50)
	}
	const (
		amount     uint64 = 5_000_000
		startSlot  uint32 = 1234
		epochSlots uint32 = 600
	)
	for _, state := range []byte{0, ledger.DelegateLockStateOnHold} {
		swapped := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amount))
			o.WithLock(ledger.NewDelegateLock(target, master, 16, 850, epochSlots, 32))
			o.PutConstraint(ledger.NewChainOrigin(startSlot).Bytes(), ledger.ConstraintIndexChain)
			o.MustPushConstraint(ledger.DelegateLockState{State: state}.Bytes())
		})
		oid := outputIDAtSlot(startSlot)
		walletOut, err := txbuildercore.OutputFromBytes(swapped.Bytes())
		require.NoError(t, err)
		view, ok, err := lib.ParseDelegationOutput(walletOut, oid)
		require.NoError(t, err)
		require.True(t, ok)

		_, _, applicable := view.SafeRevocationWindow(walletC)
		require.False(t, applicable, "state %d", state)
		require.False(t, view.IsInSafeRevocationWindow(startSlot+10, walletC))
	}
}

// TestParseChainConstraint_Origin_Parity verifies the wallet parser
// extracts the same fields from a chain-origin constraint as the
// server-side ledger.ChainConstraintFromBytesWithLib.
func TestParseChainConstraint_Origin_Parity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	const startSlot uint32 = 1234
	bin := ledger.NewChainOrigin(startSlot).Bytes()

	view, err := lib.ParseChainConstraint(bin)
	require.NoError(t, err)
	require.Equal(t, base.NilChainID, view.ChainID)
	require.Equal(t, byte(0xff), view.PredecessorInputIndex)
	require.Equal(t, startSlot, view.OriginSlot)
	require.Equal(t, uint64(0), view.CumulativeChainInflation)
	require.Equal(t, uint64(0), view.CumulativeBranchBonus)
	require.Equal(t, uint64(0), view.TransitionCounter)
	require.Equal(t, uint32(0), view.BranchCounter)
}

// TestParseChainConstraint_Transit_Parity covers the transit case
// across a few non-zero arg combinations.
func TestParseChainConstraint_Transit_Parity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	var chainID base.ChainID
	for i := range chainID {
		chainID[i] = byte(i + 1)
	}
	cases := []struct {
		predIdx     byte
		originSlot  uint32
		cumChain    uint64
		cumBranch   uint64
		transitions uint64
		branches    uint32
	}{
		{0, 1, 0, 0, 1, 0},
		{3, 1234, 100_000, 50_000, 7, 2},
		{255, 1 << 30, 1 << 40, 1 << 38, 1 << 16, 1 << 15},
	}
	for _, c := range cases {
		cc := ledger.NewChainConstraint(chainID, c.predIdx, c.originSlot, c.cumChain, c.cumBranch, c.transitions, c.branches)
		bin := cc.Bytes()
		view, err := lib.ParseChainConstraint(bin)
		require.NoError(t, err)
		require.Equal(t, chainID, view.ChainID)
		require.Equal(t, c.predIdx, view.PredecessorInputIndex)
		require.Equal(t, c.originSlot, view.OriginSlot)
		require.Equal(t, c.cumChain, view.CumulativeChainInflation)
		require.Equal(t, c.cumBranch, view.CumulativeBranchBonus)
		require.Equal(t, c.transitions, view.TransitionCounter)
		require.Equal(t, c.branches, view.BranchCounter)
	}
}

// TestParseDelegationParams_Parity checks the 2-arg wallet parser
// against the server-side ledger.DelegationParamsFromBytes.
func TestParseDelegationParams_Parity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	cases := []struct {
		epochSlots      uint32
		maxFrozenEpochs byte
	}{
		{600, 20},
		{500, 8},
		{2000, 32},
	}
	for _, c := range cases {
		bin := ledger.NewDelegationParams(c.epochSlots, c.maxFrozenEpochs).Bytes()
		view, err := lib.ParseDelegationParams(bin)
		require.NoError(t, err)
		require.Equal(t, c.epochSlots, view.EpochSlots)
		require.Equal(t, c.maxFrozenEpochs, view.MaxFrozenEpochs)
	}
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
