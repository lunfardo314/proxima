package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// Library upgrade 5 end to end (kb/library_upgrade5.md): listed after upgrades 1 to 4 and
// applied by the path a node takes at start, then transactions are validated on both sides
// of its slot. The two rules: a mine transit's successor carries exactly the four elements
// mineLock computes, and a sequencer chain moves to another key only in the first slot of a
// window of constControllerChangeSlots.

// upgrade5Slot is where these tests activate upgrade 5: past upgrade 4's slot by at least the
// minimum spacing between upgrades. Not a multiple of the 60-slot window, so the slot itself is
// not one where the key may change.
const upgrade5Slot = 410

// controllerChangeSlot is the first window slot after upgrade5Slot.
const controllerChangeSlot = 420

// withUpgrade5 lists upgrades 1 to 5, applies them to the test ledger's store as a node does
// at start, and restores the package ledger afterwards.
func withUpgrade5(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// The upgraded library carries upgrade 5 from its slot on and not before, and the upgrade index
// reaches the wallet constants.
func TestUpgrade5Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade5(t, u)

	_, err := ledger.L(upgrade5Slot - 1).FunctionByName("_sequencerKeyFixed")
	require.Error(t, err)
	require.EqualValues(t, 60, constAt(t, upgrade5Slot, "constControllerChangeSlots"))
	require.EqualValues(t, 5, ledger.L(upgrade5Slot).Constants.UpgradeIndex)
	require.True(t, ledger.L(upgrade5Slot).HasUpgrade4())
	require.Zero(t, constDelegationEpochSlots(t)%constAt(t, upgrade5Slot, "constControllerChangeSlots"),
		"the window divides the delegation epoch")
}

func constDelegationEpochSlots(t *testing.T) uint64 {
	return constAt(t, upgrade5Slot, "constDelegationEpochSlots")
}

// mineAt mines one transit at slot, the pace taken from the current mine output, with the miner
// version and the tick the rules of upgrade 1 require, and the given deviations on the successor.
func mineAt(t *testing.T, u *utxodb.UTXODB, slot uint32, opts mineTxOpts) error {
	t.Helper()
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)
	v5 := uint64(5)
	opts.mine, opts.version, opts.tick = true, &v5, 100
	opts.pace = slot - mineIn.ID.Slot()
	return u.AddTransaction(buildMineTransition(t, u, genesisPrivateKey, opts))
}

// From the slot a successor carrying anything mineLock does not compute is refused: an extra
// constraint or an index value. A clean successor stays valid, also over a
// predecessor which carries a harmless extra constraint from before the slot.
func TestUpgrade5MineSuccessorPinned(t *testing.T) {
	// a constraint that passes on both sides: it only shows the layout is refused
	harmless := ledger.NewTimelock(1).Bytes()
	holder := base.HolderID(ledger.SigLockFromED25519PrivateKey(genesisPrivateKey))

	refused := []struct {
		name string
		opts mineTxOpts
		why  string
	}{
		{"extra constraint", mineTxOpts{succExtra: [][]byte{harmless}}, "mine successor must have exactly four elements"},
		{"index value", mineTxOpts{succIndexValues: [][]byte{holder[:]}}, "mine successor index values must equal the predecessor"},
	}
	for _, r := range refused {
		t.Run(r.name+", before the slot", func(t *testing.T) {
			u := utxodb.NewUTXODB(genesisPrivateKey, true)
			withUpgrade5(t, u)
			err := mineAt(t, u, upgrade5Slot-5, r.opts)
			t.Logf("before the slot: %v", err)
		})
		t.Run(r.name+", from the slot", func(t *testing.T) {
			u := utxodb.NewUTXODB(genesisPrivateKey, true)
			withUpgrade5(t, u)
			err := mineAt(t, u, upgrade5Slot+5, r.opts)
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, r.why))
		})
	}
	t.Run("clean successor over a harmless extra from before the slot", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		withUpgrade5(t, u)
		require.NoError(t, mineAt(t, u, upgrade5Slot-5, mineTxOpts{succExtra: [][]byte{harmless}}))
		require.NoError(t, mineAt(t, u, upgrade5Slot+5, mineTxOpts{}))
		require.NoError(t, mineAt(t, u, upgrade5Slot+6, mineTxOpts{}))
	})
}

// buildMilestone builds a sequencer transition of chainIn at ts, signed by signer, its output
// locked by lock, endorsing synthetic sequencer transaction IDs of its own slot one tick earlier
// (the ledger checks only their IDs). chainIn need not carry the sequencer constraint.
func (e *sequencerTestEnv) buildMilestone(t *testing.T, chainIn *ledger.OutputWithID, chainID base.ChainID, ts base.LedgerTime, signer ed25519.PrivateKey, lock ledger.Lock) []byte {
	t.Helper()
	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)
	coverage := uint64(1)
	if predSeq, _ := chainIn.Output.SequencerConstraint(); predSeq != nil {
		coverage = predSeq.CoverageDelta + 1
	}

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)
	succ := chainIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutLock(lock)
		o.PutConstraint(ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
		seq := ledger.NewSequencerConstraint(coverage).Bytes()
		if o.NumConstraints() > int(ledger.SequencerConstraintFixedIndex) {
			o.PutConstraint(seq, ledger.SequencerConstraintFixedIndex)
		} else {
			o.MustPushConstraint(seq)
		}
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.SetSequencerData(succIdx, txbuildercore.SequencerOutputIndexNone)
	txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-1), base.TransactionIDShort{1}, true))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(signer)
	return txb.Bytes()
}

// moveAsPlainChain is a wallet transaction, signed by the chain's key, which transits the
// sequencer chain into a plain chain output locked to another key, without the sequencer
// constraint, and returns that output.
func (e *sequencerTestEnv) moveAsPlainChain(t *testing.T, chainIn *ledger.OutputWithID, chainID base.ChainID, ts base.LedgerTime, lock ledger.Lock) *ledger.OutputWithID {
	t.Helper()
	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)
	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)
	succIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(chainIn.Output.TokenBalance())).WithLock(lock)
		o.PutConstraint(ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)
	require.NoError(t, e.u.AddTransaction(txb.Bytes()))

	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	ret, err := chs.Parse()
	require.NoError(t, err)
	return ret
}

// A milestone moving its chain to another key is valid before the slot, refused from it except
// in the first slot of a window; keeping the key stays valid everywhere.
func TestUpgrade5SequencerKeyChange(t *testing.T) {
	const refused = "sequencer chain can move to another key only in the first slot of a window"
	cases := []struct {
		name    string
		slot    uint32
		newKey  bool
		refused bool
	}{
		{"new key, before the slot", upgrade5Slot - 5, true, false},
		{"new key, from the slot", upgrade5Slot + 5, true, true},
		{"new key, window slot", controllerChangeSlot, true, false},
		{"new key, after the window slot", controllerChangeSlot + 1, true, true},
		{"same key, from the slot", upgrade5Slot + 5, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, 10_000_000_000)
			withUpgrade5(t, e.u)
			_, _, other := e.u.GenerateAddress(2)
			lock := e.addr
			if c.newKey {
				lock = other
			}
			chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(c.slot-1, 20))
			err := e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(c.slot, 30), e.privKey, lock))
			if !c.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, refused))
		})
	}
}

// From the slot a sequencer output is locked by sigLock, also in a window slot: a chainLock, whose
// controller can move freely, would let it pick the signer.
func TestUpgrade5SequencerOutputSigLocked(t *testing.T) {
	e := newSequencerTestEnv(t, 10_000_000_000)
	withUpgrade5(t, e.u)
	chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(controllerChangeSlot-1, 20))
	lock := ledger.ChainLockFromChainID(base.RandomChainID())
	err := e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(controllerChangeSlot, 30), e.privKey, lock))
	require.Error(t, err)
	require.NoError(t, util.MustErrorWith(err, "sequencer output must be sig locked"))
}

// A sequencer chain moved to another key as a plain chain output by a wallet transaction can be
// extended by a milestone under the new key before the slot, not from it.
func TestUpgrade5SequencerPredecessorIsSequencer(t *testing.T) {
	cases := []struct {
		name    string
		slot    uint32
		refused bool
	}{
		{"before the slot", upgrade5Slot - 5, false},
		{"from the slot", upgrade5Slot + 5, true},
		{"window slot", controllerChangeSlot, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, 10_000_000_000)
			withUpgrade5(t, e.u)
			otherPriv, _, other := e.u.GenerateAddress(2)
			chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(c.slot-2, 20))
			plain := e.moveAsPlainChain(t, chainIn, chainID, base.T(c.slot-1, 20), other)
			err := e.u.AddTransaction(e.buildMilestone(t, plain, chainID, base.T(c.slot, 30), otherPriv, other))
			if !c.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, "sequencer chain predecessor must be a sequencer output"))
		})
	}
}

// The node policy that ships with upgrade 5 (Transaction.MineSuccessorIsPlain): the node relays
// and includes only a transit whose successor has the four elements mineLock computes, also
// before the slot.
func TestMineSuccessorIsPlain(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	clean, err := transaction.Parse(buildMineTransition(t, u, genesisPrivateKey, mineTxOpts{mine: true}))
	require.NoError(t, err)
	require.True(t, clean.MineSuccessorIsPlain())

	extra := ledger.NewTimelock(1).Bytes()
	extended, err := transaction.Parse(buildMineTransition(t, u, genesisPrivateKey, mineTxOpts{mine: true, succExtra: [][]byte{extra}}))
	require.NoError(t, err)
	require.True(t, extended.IsMiningTransaction())
	require.False(t, extended.MineSuccessorIsPlain())
}
