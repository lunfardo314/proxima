package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// Library upgrade 6 end to end (kb/library_upgrade6.md): listed after upgrades 1 to 5 and
// applied by the path a node takes at start, then transactions are validated on both sides
// of its slot. Every output a mine transit produces is pinned element by element, the full
// reward is a strict top-up request, and the top-up guarantor takes a request only into a
// delegation, in the strict form only into a plain one. Every other top-up keeps its meaning.

// upgrade6Slot is where these tests activate upgrade 6: past upgrade 5's slot by at least the
// minimum spacing between upgrades.
const upgrade6Slot = 510

// withUpgrade6 lists upgrades 1 to 6, applies them to the test ledger's store as a node does
// at start, and restores the package ledger afterwards.
func withUpgrade6(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: ledger.BuildUpgrade6Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// The upgraded library requires miner version 6 from its slot on and not before, and the
// upgrade index reaches the wallet constants.
func TestUpgrade6Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6(t, u)

	require.EqualValues(t, 5, constAt(t, upgrade6Slot-1, "constMinerVersion"))
	require.EqualValues(t, 6, constAt(t, upgrade6Slot, "constMinerVersion"))
	require.EqualValues(t, 6, ledger.L(upgrade6Slot).Constants.UpgradeIndex)
}

// mineAt6 mines one transit at slot under the rules of that slot, the miner version taken
// from the library applying there, with the given reward layout.
func mineAt6(t *testing.T, u *utxodb.UTXODB, slot uint32, opts mineTxOpts) error {
	t.Helper()
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)
	v := constAt(t, slot, "constMinerVersion")
	opts.mine, opts.version, opts.tick = true, &v, 100
	opts.pace = slot - mineIn.ID.Slot()
	return u.AddTransaction(buildMineTransition(t, u, genesisPrivateKey, opts))
}

// Each reward layout on both sides of the slot. From the slot the full reward must be a
// strict top-up request, and no reward output or fee may carry an element beyond its plain
// layout; before it, every layout of upgrade 5 stays valid and the strict form is unknown.
func TestUpgrade6MinePayouts(t *testing.T) {
	// a constraint that passes on both sides: it only shows the layout is refused
	harmless := ledger.NewTimelock(1).Bytes()
	delegationID := base.RandomChainID()

	cases := []struct {
		name        string
		opts        mineTxOpts
		before      string // "" valid before the slot, else the reason it is refused
		fromTheSlot string // "" valid from the slot on, else the reason it is refused
	}{
		{"full reward as a plain tag-along", mineTxOpts{},
			"", "mine full payout must be a strict top-up request"},
		{"full reward as a 24-byte top-up request", mineTxOpts{topUpOf: &delegationID},
			"", "mine full payout must be a strict top-up request"},
		{"full reward as a strict top-up request", mineTxOpts{topUpOf: &delegationID, strict: true},
			"wrong chain id", ""},
		{"strict top-up request with a script at element 3", mineTxOpts{topUpOf: &delegationID, strict: true, requestData: harmless},
			"wrong chain id", "not an inline data function"}, // parseInlineData's own refusal
		{"opening", mineTxOpts{option: txbuildercore.MinePayoutOpening},
			"", ""},
		{"opening with an extra element", mineTxOpts{option: txbuildercore.MinePayoutOpening, rewardExtra: [][]byte{harmless}},
			"", "mine opening payout must be a plain sigLock"},
		{"new delegation", mineTxOpts{option: txbuildercore.MinePayoutDelegation},
			"", ""},
		{"new delegation with an extra element", mineTxOpts{option: txbuildercore.MinePayoutDelegation, rewardExtra: [][]byte{harmless}},
			"", "mine delegation payout must be a plain delegation"},
		{"fee with an extra element", mineTxOpts{option: txbuildercore.MinePayoutOpening, feeExtra: [][]byte{harmless}},
			"", "mine inclusion fee must be a plain tag-along"},
	}
	check := func(t *testing.T, err error, why string) {
		if why == "" {
			require.NoError(t, err)
			return
		}
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, why))
	}
	for _, c := range cases {
		t.Run(c.name+", before the slot", func(t *testing.T) {
			u := utxodb.NewUTXODB(genesisPrivateKey, true)
			withUpgrade6(t, u)
			check(t, mineAt6(t, u, upgrade6Slot-5, c.opts), c.before)
		})
		t.Run(c.name+", from the slot", func(t *testing.T) {
			u := utxodb.NewUTXODB(genesisPrivateKey, true)
			withUpgrade6(t, u)
			check(t, mineAt6(t, u, upgrade6Slot+5, c.opts), c.fromTheSlot)
		})
	}
}

// newTopUpEnv is the delegation harness on a ledger listing upgrades 1 to 6, with an
// unfrozen delegation created at slot at, carrying extras between the chain constraint and
// the state, as a delegated foundry chain carries its foundry.
func newTopUpEnv(t *testing.T, at uint32, extras [][]byte) *testData {
	td := &testData{T: t}
	td.init()
	withUpgrade6(t, td.u)

	ts := base.T(at, 10)
	outs, total := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, delegatedTokens)
	require.True(t, total >= delegatedTokens)
	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsUnlock(outs...)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(delegatedTokens))
		o.WithLock(ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 900))
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		for _, c := range extras {
			o.MustPushConstraint(c)
		}
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	if total > delegatedTokens {
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - delegatedTokens)).WithLock(td.masterAddr)
		}))
		require.NoError(t, err)
	}
	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	require.NoError(t, td.u.AddTransaction(txBytes))

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	dc, err := tx.MustProducedOutputWithIDAt(0).AsChainOutput()
	require.NoError(t, err)
	var ok bool
	td.delegatedOutput, ok = ledger.DelegationOutputFromOutputWithChainID(dc)
	require.True(t, ok)
	return td
}

// keepExtras puts the extras back between the chain constraint and the state of a successor
// the freeze builder made plain: the target must keep every element the master put on the
// chain, and the delegate lock checks it does.
func keepExtras(extras [][]byte) func(*ledger.Output) *ledger.Output {
	return func(succ *ledger.Output) *ledger.Output {
		if len(extras) == 0 {
			return succ
		}
		return succ.Clone(func(o *ledger.OutputBuilder) {
			last := succ.NumElements() - 1
			state, err := succ.At(last)
			util.AssertNoError(err)
			o.PutConstraint(extras[0], byte(last))
			for _, c := range extras[1:] {
				o.MustPushConstraint(c)
			}
			o.MustPushConstraint(state)
		})
	}
}

// freshFreezeTopUp sends a top-up request in the given form at slot and lets the target
// freeze the delegation with it one slot later.
func (td *testData) freshFreezeTopUp(slot uint32, strict bool, extras [][]byte) error {
	ens := &ledger.EnsureTopUpDelegation{ChainID: td.delegatedOutput.ChainID, Strict: strict}
	req := td.makeTopUpRequestOutputWith(base.T(slot, 10), td.masterPrivateKey, td.masterAddr, ens, topUpAmount)
	ts := base.T(slot+1, 20)
	return td.topUpDelegation(topUpParams{ts: ts, request: req, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts), tamper: keepExtras(extras)})
}

// The existing top-ups keep their meaning across the upgrade: a 24-byte request goes into
// a plain delegation and into one carrying extra elements, on both sides of the slot. The
// strict form, unknown before the slot, goes from it on only into a plain delegation.
func TestUpgrade6TopUpForms(t *testing.T) {
	extras := [][]byte{ledger.NewTimelock(1).Bytes()}
	cases := []struct {
		name   string
		extras [][]byte
		strict bool
		slot   uint32
		why    string // "" valid, else the reason it is refused
	}{
		{"24-byte, plain, before the slot", nil, false, upgrade6Slot - 10, ""},
		{"24-byte, plain, from the slot", nil, false, upgrade6Slot + 10, ""},
		{"24-byte, extra element, before the slot", extras, false, upgrade6Slot - 10, ""},
		{"24-byte, extra element, from the slot", extras, false, upgrade6Slot + 10, ""},
		{"strict, plain, from the slot", nil, true, upgrade6Slot + 10, ""},
		{"strict, extra element, from the slot", extras, true, upgrade6Slot + 10, "strict top-up needs a plain delegation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			td := newTopUpEnv(t, c.slot-2, c.extras)
			before := td.delegatedOutput.Output.TokenBalance()
			err := td.freshFreezeTopUp(c.slot, c.strict, c.extras)
			if c.why != "" {
				require.Error(t, err)
				require.NoError(t, util.MustErrorWith(err, c.why))
				return
			}
			require.NoError(t, err)
			require.True(t, td.delegatedOutput.IsMarkedFrozen())
			require.Greater(t, td.delegatedOutput.Output.TokenBalance(), before+topUpAmount)
			require.Equal(t, 5+len(c.extras), td.delegatedOutput.Output.NumElements())
		})
	}
	t.Run("strict, before the slot", func(t *testing.T) {
		td := newTopUpEnv(t, upgrade6Slot-12, nil)
		par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, base.T(upgrade6Slot-10, 10))
		require.NoError(t, err)
		txBytes, err := utxodb.MakeSimpleTransferTransaction(par.WithAmount(topUpAmount).
			WithTargetLock(&ledger.TagAlongLock{TargetSequencerID: td.target, SenderID: base.HolderID(td.masterAddr)}).
			WithConstraintBinary(ledger.NewTimelock(1).Bytes()).
			WithConstraint(&ledger.EnsureTopUpDelegation{ChainID: td.delegatedOutput.ChainID, Strict: true}))
		require.NoError(t, err)
		err = td.u.AddTransaction(txBytes)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "wrong chain id"))
	})
}

// The route the strict top-up exists to close, on both sides of the slot. A sequencer
// takes a top-up request into a chain of its own: locked by sigLock to its controller,
// which ignores its unlock parameters, and carrying a delegateLockState at the end, which
// is valid on any output. The guarantor's frozen mark and 3-byte unlock are met, the
// delegate lock and its master check never run, and the request's tokens end in the
// sequencer's chain. From the slot the guarantor requires a delegateLock predecessor, so
// even a 24-byte request can no longer be taken this way.
func TestUpgrade6FakeDelegation(t *testing.T) {
	for _, c := range []struct {
		name string
		slot uint32
		why  string
	}{
		{"before the slot", upgrade6Slot - 10, ""},
		{"from the slot", upgrade6Slot + 10, "predecessor must be a delegation"},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := newTopUpEnv(t, c.slot-4, nil)
			seqCtrl := ledger.SigLockFromED25519PrivateKey(td.seqPrivateKey)

			// the sequencer controller's chain C, dressed as a delegation; funded from the
			// master's outputs, since the controller's own include the sequencer chain
			const cAmount = 100_000_000
			outs, total := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, cAmount)
			require.True(t, total >= cAmount)
			txb := exhelp.New()
			_, _, err := txb.ConsumeOutputsUnlock(outs...)
			require.NoError(t, err)
			cTs := base.T(c.slot-3, 10)
			_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithAmounts(cAmount).WithLock(seqCtrl)
				o.PutConstraint(ledger.NewChainOrigin(cTs.Slot).Bytes(), ledger.ConstraintIndexChain)
				o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
			}))
			require.NoError(t, err)
			if total > cAmount {
				_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
					o.WithAmounts(int64(total - cAmount)).WithLock(td.masterAddr)
				}))
				require.NoError(t, err)
			}
			txb.ComputeInputCommitment()
			txb.SetTimestamp(cTs)
			txb.SignED25519(td.masterPrivateKey)
			txBytes, _, _, err := txbtest.BuildAndValidate(txb)
			require.NoError(t, err)
			require.NoError(t, td.u.AddTransaction(txBytes))
			tx, err := transaction.Parse(txBytes)
			require.NoError(t, err)
			cOut, err := tx.MustProducedOutputWithIDAt(0).AsChainOutput()
			require.NoError(t, err)

			// a request naming C, as a miner with a weak key would send to its own sequencer
			req := td.makeTopUpRequestOutput(base.T(c.slot-2, 10), td.masterPrivateKey, td.masterAddr, cOut.ChainID, topUpAmount)

			// the sequencer's transaction: seq chain at 0, C at 1, the request at 2
			ts := base.T(c.slot, 20)
			txb = exhelp.New()
			_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
			require.NoError(t, err)
			cIdx, err := txb.ConsumeOutput(cOut.Output, cOut.ID)
			require.NoError(t, err)
			reqIdx, err := txb.ConsumeOutput(req.Output, req.ID)
			require.NoError(t, err)
			seqIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
				o.PutConstraint(ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
			}))
			require.NoError(t, err)
			cSucc := cOut.Output.Clone(func(o *ledger.OutputBuilder) {
				o.WithAmounts(int64(cAmount + topUpAmount))
				o.PutConstraint(ledger.NewChainConstraint(cOut.ChainID, cIdx, cTs.Slot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
				o.PutConstraint(ledger.DelegateLockState{LastFrozenEpoch: 1, State: ledger.DelegateLockStateFrozen}.Bytes(), byte(cOut.Output.NumElements()-1))
			})
			cSuccIdx, err := txb.ProduceOutput(cSucc)
			require.NoError(t, err)
			txb.PutSignatureUnlock(0)
			txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(seqIdx))
			// three bytes on C's sigLock: no reference (0xff), anything, the request
			txb.PutUnlockParams(cIdx, ledger.ConstraintIndexLock, []byte{0xff, 0, reqIdx})
			txb.PutUnlockParams(cIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(cSuccIdx))
			txb.PutUnlockParams(reqIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
			txb.PutUnlockParams(reqIdx, 4, []byte{cSuccIdx})
			fcDelta, err := txb.CalcFrozenCoverageDelta()
			require.NoError(t, err)
			txb.MustPutFrozenCoverage(seqIdx, fcDelta, ts)
			txb.SetSequencerData(seqIdx, txbuildercore.SequencerOutputIndexNone)
			txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))
			txb.ComputeInputCommitment()
			txb.SetTimestamp(ts)
			txb.SignED25519(td.seqPrivateKey)
			txBytes, _, _, err = txbtest.BuildAndValidate(txb)
			if err == nil {
				err = td.u.AddTransaction(txBytes)
			}
			if c.why == "" {
				require.NoError(t, err, "the route is open before the slot")
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, c.why))
		})
	}
}
