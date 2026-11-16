package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// Library upgrade 7 end to end (.internal/library_upgrade7.md): listed after upgrades 1 to 6
// and applied by the path a node takes at start, then transactions are validated on both
// sides of its slot. The rules on branches (the chosen bonus, the sealed bid, the two inputs)
// are tested with the sequencer's builder, which makes real branches.

// u7Slot is where these tests activate upgrade 7: at least the minimum distance between upgrades
// (100 slots) past upgrade 6's test slot. upgrade7Slot of the genesis fold test is a placeholder
// listed after a genesis folding upgrade 6, so it can sit closer.
const u7Slot = 610

// withUpgrade7 lists upgrades 1 to 7, applies them to the test ledger's store as a node does at
// start, and restores the package ledger afterwards. Upgrade 6 is patched as withUpgrade6 does
// (test mining floor, no pause), so the other test harnesses keep working.
func withUpgrade7(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: func(prev []byte) ([]byte, error) {
			lib, err := ledger.BuildUpgrade6LibraryAt(upgrade6Slot)(prev)
			if err != nil {
				return nil, err
			}
			return ledger.BuildUpgradeFromSource(lib, "test floor and no pause",
				"func constMineFloorDifficulty : u64/6\nfunc constDisableMiningUntilSlot : u64/0")
		}},
		{Slot: u7Slot, Build: ledger.BuildUpgrade7LibraryAt(u7Slot)},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// pushSeqExtension appends to a sequencer output the sequencer's parameters (empty) at index 5
// and the extension element of upgrade 7 at index 6, element 0 the given bid commitment.
func pushSeqExtension(o *ledger.OutputBuilder, commitment []byte) {
	o.MustPushConstraint(easyfl.InlineDataBytecode([]byte("{}")))
	o.MustPushConstraint(ledger.SeqExtensionBytecode(commitment))
}

// buildWalletSequencerOrigin7 is buildWalletSequencerOrigin with the extension element; ext
// replaces the default one (no bid) when given.
func (e *sequencerTestEnv) buildWalletSequencerOrigin7(t *testing.T, originTs base.LedgerTime, ext ...[]byte) []byte {
	t.Helper()
	outs := getSourceOutputs(t, e.u, e.addr)
	txb := exhelp.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	for i := 1; i < len(outs); i++ {
		require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
	}
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(e.addr)
		o.MustPushConstraint(ledger.NewChainOrigin(originTs.Slot).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
		if len(ext) > 0 {
			o.MustPushConstraint(easyfl.InlineDataBytecode([]byte("{}")))
			o.MustPushConstraint(ext[0])
			return
		}
		pushSeqExtension(o, nil)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(originTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)
	return txb.Bytes()
}

// settleWalletSequencerOrigin7 settles buildWalletSequencerOrigin7 and returns the chain output.
func (e *sequencerTestEnv) settleWalletSequencerOrigin7(t *testing.T, originTs base.LedgerTime) (*ledger.OutputWithID, base.ChainID) {
	t.Helper()
	txBytes := e.buildWalletSequencerOrigin7(t, originTs)
	require.NoError(t, e.u.AddTransaction(txBytes))
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(tx.ID(), 0))
	return e.chainOutput(t, chainID), chainID
}

// chainOutput is the chain's current output in the state.
func (e *sequencerTestEnv) chainOutput(t *testing.T, chainID base.ChainID) *ledger.OutputWithID {
	t.Helper()
	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	chainIn, err := chs.Parse()
	require.NoError(t, err)
	return chainIn
}

// The upgraded library builds on upgrade 6, carries its constants from its slot on and not
// before, and the upgrade index reaches the wallet constants.
func TestUpgrade7Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade7(t, u)

	require.EqualValues(t, 10000, constAt(t, u7Slot-1, "constMinSequencerBalanceProx"))
	require.EqualValues(t, 20000, constAt(t, u7Slot, "constMinSequencerBalanceProx"))
	require.EqualValues(t, 100000, constAt(t, u7Slot, "constMinSequencerCoverageProx"))
	require.EqualValues(t, 8, constAt(t, u7Slot, "constPreBranchQuietTicks"))
	require.EqualValues(t, 7, ledger.L(u7Slot).Constants.UpgradeIndex)
}

// The extension element is valid on a sequencer output before the slot, so a sequencer can
// write it ahead of the activation; from the slot every sequencer output must carry it, with
// a commitment of 20 bytes or none, and further tuple elements are accepted.
func TestUpgrade7ExtensionElement(t *testing.T) {
	const funds = 20_000_000_000
	const missing = "sequencer output must carry the extension element"
	const badSize = "bid commitment must be 20 bytes or empty"

	t.Run("written before the slot, carried after it", func(t *testing.T) {
		e := newSequencerTestEnv(t, funds)
		withUpgrade7(t, e.u)
		chainIn, chainID := e.settleWalletSequencerOrigin7(t, base.T(u7Slot-5, 20))
		require.NoError(t, e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(u7Slot+5, 20), e.privKey, e.addr)))
	})
	t.Run("missing: valid before the slot, refused from it", func(t *testing.T) {
		e := newSequencerTestEnv(t, funds)
		withUpgrade7(t, e.u)
		chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(u7Slot-5, 20))
		require.NoError(t, e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(u7Slot-4, 20), e.privKey, e.addr)))
		err := e.u.AddTransaction(e.buildMilestone(t, e.chainOutput(t, chainID), chainID, base.T(u7Slot+5, 20), e.privKey, e.addr))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, missing))
	})

	extraElement := tuples.EmptyTupleEditable(256)
	extraElement.MustPush(make([]byte, 20))
	extraElement.MustPush([]byte{1, 2, 3})
	for _, c := range []struct {
		name    string
		ext     []byte
		refused string
	}{
		{"a 20-byte commitment", ledger.SeqExtensionBytecode(ledger.BidCommitment(1_000_000, [8]byte{1})), ""},
		{"a 21-byte commitment", ledger.SeqExtensionBytecode(make([]byte, 21)), badSize},
		{"a later element after the commitment", easyfl.InlineDataBytecode(extraElement.Tuple().Bytes()), ""},
		{"an empty tuple", easyfl.InlineDataBytecode(tuples.EmptyTupleEditable(256).Tuple().Bytes()), "sequencer extension must hold the bid commitment"},
	} {
		t.Run("origin from the slot with "+c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, funds)
			withUpgrade7(t, e.u)
			err := e.u.AddTransaction(e.buildWalletSequencerOrigin7(t, base.T(u7Slot+5, 20), c.ext))
			if c.refused == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, c.refused))
		})
	}
}

// From the slot no sequencer transaction other than a branch is stamped after tick 119; before
// it, tick 125 of the pre-branch zone is still open. The chain holds 20,000 PROX of its own,
// so the zone's soft bound is met.
func TestUpgrade7QuietTicks(t *testing.T) {
	const refused = "sequencer transaction stamped in the quiet end of the slot"
	for _, c := range []struct {
		name    string
		slot    uint32
		tick    byte
		refused bool
	}{
		{"tick 125, before the slot", u7Slot - 3, 125, false},
		{"tick 119, from the slot", u7Slot + 5, 119, false},
		{"tick 120, from the slot", u7Slot + 5, 120, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, 20_000_000_000)
			withUpgrade7(t, e.u)
			chainIn, chainID := e.settleWalletSequencerOrigin7(t, base.T(u7Slot-5, 20))
			// a milestone in the pre-branch zone needs a chain predecessor of its own slot
			require.NoError(t, e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(c.slot, 60), e.privKey, e.addr)))
			err := e.u.AddTransaction(e.buildMilestone(t, e.chainOutput(t, chainID), chainID, base.T(c.slot, c.tick), e.privKey, e.addr))
			if !c.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, refused))
		})
	}
}

// From the slot a milestone in the pre-branch zone needs 20,000 PROX of the sequencer's own or
// 100,000 of coverage; 15,000 of its own and nothing frozen is refused, though upgrade 6's
// 10,000 let it through before the slot.
func TestUpgrade7SoftBoundZone(t *testing.T) {
	const refused = "sequencer below both bounds for a branch or the pre-branch zone"
	for _, c := range []struct {
		name    string
		funds   uint64
		slot    uint32
		refused bool
	}{
		{"15,000 own, before the slot", 15_000_000_000, u7Slot - 3, false},
		{"15,000 own, from the slot", 15_000_000_000, u7Slot + 5, true},
		{"20,000 own, from the slot", 20_000_000_000, u7Slot + 5, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, c.funds)
			withUpgrade7(t, e.u)
			chainIn, chainID := e.settleWalletSequencerOrigin7(t, base.T(u7Slot-5, 20))
			require.NoError(t, e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(c.slot, 60), e.privKey, e.addr)))
			err := e.u.AddTransaction(e.buildMilestone(t, e.chainOutput(t, chainID), chainID, base.T(c.slot, 110), e.privKey, e.addr))
			if !c.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, refused))
		})
	}
}

// From the slot a fresh freeze needs a target with 20,000 PROX of its own when the transaction
// begins, or 100,000 of coverage after it, the delegation being frozen counted: a target with
// 15,000 of its own freezes a delegation of 90,000 but not one of 1,000.
func TestUpgrade7FreezeBounds(t *testing.T) {
	for _, c := range []struct {
		name       string
		seqBalance uint64
		delegated  uint64
		slot       uint32
		refused    bool
	}{
		{"15,000 own, 1,000 delegated, before the slot", 15_000_000_000, 1_000_000_000, u7Slot - 20, false},
		{"15,000 own, 1,000 delegated, from the slot", 15_000_000_000, 1_000_000_000, u7Slot + 10, true},
		{"20,000 own, 1,000 delegated, from the slot", 20_000_000_000, 1_000_000_000, u7Slot + 10, false},
		{"15,000 own, 90,000 delegated, from the slot", 15_000_000_000, 90_000_000_000, u7Slot + 10, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := &testData{T: t, seqBalance: c.seqBalance, seqExtension: true, delegatedAmount: c.delegated}
			td.init()
			withUpgrade7(t, td.u)
			_, _, err := td.initDelegationUTXOMake(base.T(c.slot, 10), 4, 900)
			require.NoError(t, err)
			ts := td.timestampSlotsForward(1)
			err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
				ts:                       ts,
				freezeUntilEpoch:         td.delegatedOutput.FreezeUntilMax(ts),
				inflate:                  true,
				disableConsistencyChecks: true,
			})
			if !c.refused {
				require.NoError(t, err)
				require.True(t, td.delegatedOutput.IsMarkedFrozen())
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, "target below both bounds for freezing"))
		})
	}
}

// From the slot anyone may top up a delegation: a request signed by a payer other than the
// master is taken into a frozen delegation, continuing its freeze. Before the slot the same
// request is refused as not the master's.
func TestUpgrade7TopUpFromAnySender(t *testing.T) {
	for _, c := range []struct {
		name    string
		at      uint32 // the slot the top-up is made in
		refused bool
	}{
		{"before the slot", u7Slot - 2, true},
		{"from the slot", u7Slot + 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := &testData{T: t, seqBalance: 20_000_000_000, seqExtension: true}
			td.init()
			withUpgrade7(t, td.u)
			payerKey, _, payer := td.u.GenerateAddress(7)
			require.NoError(t, td.u.TokensFromFaucet(payer, 10*topUpAmount))

			_, _, err := td.initDelegationUTXOMake(base.T(c.at-4, 10), 4, 900)
			require.NoError(t, err)
			ts := base.T(c.at-3, 10)
			require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
				ts:                       ts,
				freezeUntilEpoch:         td.delegatedOutput.FreezeUntilMax(ts),
				inflate:                  true,
				disableConsistencyChecks: true,
			}))
			require.True(t, td.delegatedOutput.IsMarkedFrozen())

			topUpTs := base.T(c.at, 10)
			require.True(t, td.delegatedOutput.IsInFrozenSlot(topUpTs.Slot), "the freeze must still run at the top-up")
			req := td.makeTopUpRequestOutput(base.T(c.at-1, 20), payerKey, payer, td.delegatedOutput.ChainID, topUpAmount)
			err = td.topUpDelegation(topUpParams{ts: topUpTs, request: req})
			if !c.refused {
				require.NoError(t, err)
				require.True(t, td.delegatedOutput.IsMarkedFrozen())
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, "top-up not authorised by the delegation master"))
		})
	}
}
