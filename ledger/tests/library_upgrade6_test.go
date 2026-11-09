package tests

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	mbits "math/bits"
	"testing"

	"github.com/lunfardo314/easyfl"

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

// Library upgrade 6 end to end (kb/library_upgrade6_mining.md): listed after upgrades 1 to 5
// and applied by the path a node takes at start, then transactions are validated on both
// sides of its slot.

// upgrade6Slot is where these tests activate upgrade 6: past upgrade 5's slot by at least the
// minimum spacing between upgrades.
const upgrade6Slot = 510

// withUpgrade6 lists upgrades 1 to 6, applies them to the test ledger's store as a node does
// at start, and restores the package ledger afterwards. Upgrade 6 raises the mine difficulty
// floor to 24, above the test ledger's whole band [6, 10]; the tests here mine at the test
// floor, put back by a patch on top of upgrade 6, and TestUpgrade6DifficultyFloor checks
// the real library. The patch also lifts the mining pause after the activation, which
// TestUpgrade6MiningPause checks on the real library.
func withUpgrade6(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	withUpgrade6Built(t, u, func(prev []byte) ([]byte, error) {
		lib, err := ledger.BuildUpgrade6LibraryAt(upgrade6Slot)(prev)
		if err != nil {
			return nil, err
		}
		return ledger.BuildUpgradeFromSource(lib, "test floor and no pause",
			"func constMineFloorDifficulty : u64/6\nfunc constDisableMiningUntilSlot : u64/0")
	})
}

// withUpgrade6Built is withUpgrade6 with upgrade 6 built by build.
func withUpgrade6Built(t *testing.T, u *utxodb.UTXODB, build func([]byte) ([]byte, error)) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: build},
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

// newTopUpEnv is the delegation harness on a ledger listing upgrades 1 to 6, with an
// unfrozen delegation created at slot at.
func newTopUpEnv(t *testing.T, at uint32) *testData {
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

// The route the guarantor fix closes, on both sides of the slot. A sequencer
// takes a top-up request into a chain of its own: locked by sigLock to its controller,
// which ignores its unlock parameters, and carrying a delegateLockState at the end, which
// is valid on any output. The guarantor's frozen mark and 3-byte unlock are met, the
// delegate lock and its master check never run, and the request's tokens end in the
// sequencer's chain. From the slot the guarantor requires a delegateLock predecessor.
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
			td := newTopUpEnv(t, c.slot-4)
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

			// a request naming C, sent by the sequencer's own side
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

// transferAt6 builds a transfer of the whole of addr's first plain output back to
// addr, stamped at slot by the library of that slot, and signs it with sign.
func transferAt6(t *testing.T, u *utxodb.UTXODB, addr ledger.SigLock, slot uint32, sign func(*exhelp.Builder)) []byte {
	outsData, err := u.StateReader().GetUTXOsForController(addr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(_ *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil
	})
	require.NoError(t, err)
	txb := exhelp.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(outs[0])
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(addr)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(slot, 10))
	txb.ComputeInputCommitment()
	sign(txb)
	return txb.Bytes()
}

// From the slot the transaction ID commits to the signer (kb/library_upgrade6_mining.md):
// a transfer signed by the builder is valid on both sides of the slot, and one signed
// over the ID of the old rule, as a wallet not upgraded would sign it, is refused from
// the slot.
func TestUpgrade6SignerBoundTxID(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6(t, u)
	priv, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, 1_000_000_000))

	builderSigned := func(txb *exhelp.Builder) { txb.SignED25519(priv) }
	oldRuleSigned := func(txb *exhelp.Builder) {
		// the ID of the old rule: the same timestamp and output count, the essence
		// without the holder ID and the lengths
		txb.SignED25519(priv)
		tree := txb.ToTuple().AsTree()
		id, err := txbuildercore.TxIDFromTree(tree)
		require.NoError(t, err)
		ess, err := txbuildercore.HashEssence(tree)
		require.NoError(t, err)
		copy(id[base.LedgerTimeByteLength+1:], ess[base.LedgerTimeByteLength+1:])
		txb.TxData.SignatureData = base.SignatureDataED25519(priv, id[:])
	}

	require.NoError(t, u.AddTransaction(transferAt6(t, u, addr, upgrade6Slot-10, builderSigned)), "version 5 before the slot")
	txBytes := transferAt6(t, u, addr, upgrade6Slot+10, builderSigned)
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	require.EqualValues(t, txbuildercore.TxVersionSignerBoundID, tx.Version())
	require.NoError(t, u.AddTransaction(txBytes), "version 6 signed by the builder from the slot")

	err = u.AddTransaction(transferAt6(t, u, addr, upgrade6Slot+20, oldRuleSigned))
	require.Error(t, err, "a signature over the old ID is refused from the slot")
	require.NoError(t, util.MustErrorWith(err, "invalid signature of the transaction"))
}

// ---- the transaction-hash mine transit (kb/library_upgrade6_mining.md) ----

// hashTransitOpts varies a transit built by buildHashTransit, each field a deviation from
// the reference template: the mine input alone, the successor at output 0, one tag-along
// of A from the signer, an 8-byte nonce as the only transaction-level constraint.
type hashTransitOpts struct {
	payout        func(a uint64, signer ledger.SigLock, chain base.ChainID) []*ledger.Output // the outputs after the successor
	extraInput    *ledger.OutputWithID                                 // a second input, unlocked by the signer
	nonceInUnlock bool                                                 // the nonce in the mine input's unlock parameters
	varyPayout    bool                                                 // no nonce: the search varies the last output's amount
	succLock      func(r, b, c uint64) []byte                          // another successor lock bytecode
	tick          byte                                                 // timestamp tick (default 90)
	failWork      bool                                                 // stop at a nonce that does NOT meet K
	afterSolved   func(txb *exhelp.Builder)                            // a change after the search, before signing again
	resignWith    ed25519.PrivateKey                                   // sign the solution with another key
}

// workMeets reports whether the transaction ID's low 64 bits end in at least k zero bits.
func workMeets(id base.TransactionID, k int) bool {
	return mbits.TrailingZeros64(binary.BigEndian.Uint64(id[24:])) >= k
}

// buildHashTransit builds a transit of the mine output at slot under the rules of that
// slot, signed by priv, and searches for a solution at the required difficulty.
func buildHashTransit(t *testing.T, u *utxodb.UTXODB, priv ed25519.PrivateKey, slot uint32, opts hashTransitOpts) []byte {
	t.Helper()
	lib := ledger.L(slot)
	a := mineConst(t, "constMineAmountBase")
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)
	lockBin, err := mineIn.Output.At(int(ledger.ConstraintIndexLock))
	require.NoError(t, err)
	predLock, err := ledger.MineLockFromBytesWithLib(lockBin, lib)
	require.NoError(t, err)
	cc := mineIn.Output.ChainConstraint()
	predSlot := mineIn.ID.Timestamp().Slot
	signer := ledger.SigLockFromED25519PrivateKey(priv)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(mineIn.Output, mineIn.ID)
	require.NoError(t, err)
	extraIdx := byte(0xff)
	if opts.extraInput != nil {
		extraIdx, err = txb.ConsumeOutput(opts.extraInput.Output, opts.extraInput.ID)
		require.NoError(t, err)
		a += opts.extraInput.Output.TokenBalance() // what the other outputs receive
	}

	succB, succC := lib.MineRetarget(predLock.B, predLock.C, predSlot, slot)
	r := predLock.R - mineConst(t, "constMineAmountBase")
	succLockBin := ledger.NewMineLock(r, succB, succC, constAt(t, slot, "constMinerVersion"), 1).Bytes()
	if opts.succLock != nil {
		succLockBin = opts.succLock(r, succB, succC)
	}
	minted := mineConst(t, "constMineAmountBase")
	succ := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(mineIn.Output.TokenBalance()), int64(minted))
		o.PutConstraint(succLockBin, ledger.ConstraintIndexLock)
		o.PutConstraint(ledger.NewChainConstraint(base.MineChainID, predIdx, cc.OriginSlot,
			cc.CumulativeChainInflation+minted, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(succ)
	require.NoError(t, err)
	payout := []*ledger.Output{ledger.NewTagAlongOutput(a, *u.GenesisChainID(), base.HolderID(signer))}
	if opts.payout != nil {
		payout = opts.payout(a, signer, *u.GenesisChainID())
	}
	for _, o := range payout {
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)
	}
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	if extraIdx != 0xff {
		txb.PutSignatureUnlock(extraIdx)
	}
	tick := opts.tick
	if tick == 0 {
		tick = 90
	}
	txb.SetTimestamp(base.T(slot, tick))
	txb.ComputeInputCommitment()

	k := int(lib.MineRequiredK(predLock.B, uint64(slot-predSlot)))
	for n := uint64(0); ; n++ {
		var nonce [8]byte
		binary.BigEndian.PutUint64(nonce[:], n)
		switch {
		case opts.varyPayout:
			last := len(txb.TxData.OutputBytes) - 1
			prev := len(txb.TxData.OutputBytes) - 2 // the amount moves between the last two outputs
			o1 := payout[len(payout)-2].Clone(func(o *ledger.OutputBuilder) { o.WithTokenBalance(payout[len(payout)-2].TokenBalance() - n) })
			o2 := payout[len(payout)-1].Clone(func(o *ledger.OutputBuilder) { o.WithTokenBalance(payout[len(payout)-1].TokenBalance() + n) })
			txb.TxData.OutputBytes[prev], txb.TxData.OutputBytes[last] = o1.Bytes(), o2.Bytes()
		case opts.nonceInUnlock:
			txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, nonce[:])
		default:
			txb.TxData.TxConstraints = [][]byte{easyfl.InlineDataBytecode(nonce[:])}
		}
		txb.SignED25519(priv)
		id, err := txbuildercore.TxIDFromBytes(txb.Bytes())
		require.NoError(t, err)
		if workMeets(id, k) != opts.failWork {
			break
		}
	}
	if opts.afterSolved != nil {
		opts.afterSolved(txb)
		txb.SignED25519(priv)
	}
	if opts.resignWith != nil {
		txb.SignED25519(opts.resignWith)
	}
	return txb.Bytes()
}

// A transit of the reference template, and others the free layout allows, are valid from
// the slot; every rule the covenant keeps refuses what breaks it.
func TestUpgrade6HashTransit(t *testing.T) {
	newEnv := func(t *testing.T) (*utxodb.UTXODB, ed25519.PrivateKey) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		withUpgrade6(t, u)
		priv, _, addr := u.GenerateAddress(3)
		require.NoError(t, u.TokensFromFaucet(addr, 100*base.PROX))
		return u, priv
	}
	slot := uint32(upgrade6Slot + 5)
	deposit := ledger.L(slot).MinimumInflatableAmount0
	newHolder := func() ledger.SigLock {
		_, priv, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		return ledger.SigLockFromED25519PrivateKey(priv)
	}
	sigLockOf := func(amount uint64, holder ledger.SigLock) *ledger.Output {
		return ledger.NewOutput(func(o *ledger.OutputBuilder) { o.WithAmounts(int64(amount)).WithLock(holder) })
	}

	valid := map[string]hashTransitOpts{
		"the reference template": {},
		"one sigLock of A":       {payout: func(a uint64, s ledger.SigLock, chain base.ChainID) []*ledger.Output { return []*ledger.Output{sigLockOf(a, s)} }},
		"a tag-along and a sigLock to another holder": {payout: func(a uint64, s ledger.SigLock, chain base.ChainID) []*ledger.Output {
			other := newHolder()
			return []*ledger.Output{ledger.NewTagAlongOutput(a-deposit, chain, base.HolderID(s)), sigLockOf(deposit, other)}
		}},
		"six tag-alongs": {payout: func(a uint64, s ledger.SigLock, chain base.ChainID) []*ledger.Output {
			ret := make([]*ledger.Output, 6)
			for i := range ret {
				ret[i] = ledger.NewTagAlongOutput(a/6, chain, base.HolderID(s))
			}
			ret[5] = ledger.NewTagAlongOutput(a-5*(a/6), chain, base.HolderID(s))
			return ret
		}},
		"the nonce in the mine input's unlock parameters": {nonceInUnlock: true},
		"no nonce, the search varying an amount": {varyPayout: true, payout: func(a uint64, s ledger.SigLock, chain base.ChainID) []*ledger.Output {
			return []*ledger.Output{sigLockOf(a-deposit, s), sigLockOf(deposit, s)}
		}},
	}
	for name, opts := range valid {
		t.Run("valid: "+name, func(t *testing.T) {
			u, priv := newEnv(t)
			require.NoError(t, u.AddTransaction(buildHashTransit(t, u, priv, slot, opts)))
		})
	}

	t.Run("refused: a second input", func(t *testing.T) {
		u, priv := newEnv(t)
		addr := ledger.SigLockFromED25519PrivateKey(priv)
		outs, _ := u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(addr, 1)
		require.NotEmpty(t, outs)
		err := u.AddTransaction(buildHashTransit(t, u, priv, slot, hashTransitOpts{extraInput: outs[0]}))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "must have exactly one input"))
	})
	t.Run("refused: too few zero bits", func(t *testing.T) {
		u, priv := newEnv(t)
		err := u.AddTransaction(buildHashTransit(t, u, priv, slot, hashTransitOpts{failWork: true}))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "insufficient mine proof of work"))
	})
	t.Run("refused: the payout changed after solving", func(t *testing.T) {
		u, priv := newEnv(t)
		var changed []byte
		for n := uint64(1); changed == nil; n++ {
			txBytes := buildHashTransit(t, u, priv, slot, hashTransitOpts{afterSolved: func(txb *exhelp.Builder) {
				o := ledger.NewTagAlongOutput(mineConst(t, "constMineAmountBase"), *u.GenesisChainID(), base.HolderID(newHolder()))
				txb.TxData.OutputBytes[1] = o.Bytes()
			}})
			id, err := txbuildercore.TxIDFromBytes(txBytes)
			require.NoError(t, err)
			if !workMeets(id, 6) { // the rare redirection that keeps the work is not the case under test
				changed = txBytes
			}
		}
		require.Error(t, u.AddTransaction(changed))
	})
	t.Run("refused: re-signed by another key", func(t *testing.T) {
		u, priv := newEnv(t)
		other, _, _ := u.GenerateAddress(4)
		for {
			txBytes := buildHashTransit(t, u, priv, slot, hashTransitOpts{resignWith: other})
			id, err := txbuildercore.TxIDFromBytes(txBytes)
			require.NoError(t, err)
			if workMeets(id, 6) { // the other key's ID happened to carry work: not the case under test
				continue
			}
			err = u.AddTransaction(txBytes)
			require.Error(t, err, "the solution belongs to the key that signed it")
			require.NoError(t, util.MustErrorWith(err, "insufficient mine proof of work"))
			return
		}
	})
	t.Run("refused: stamped before the settlement tick minus the sequencer pace", func(t *testing.T) {
		u, priv := newEnv(t)
		err := u.AddTransaction(buildHashTransit(t, u, priv, slot, hashTransitOpts{tick: 20}))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "stamped before the settlement tick"))
	})
	t.Run("refused: V written as an expression on the successor", func(t *testing.T) {
		u, priv := newEnv(t)
		err := u.AddTransaction(buildHashTransit(t, u, priv, slot, hashTransitOpts{succLock: func(r, b, c uint64) []byte {
			return mustCompileExpr(t, fmt.Sprintf("mineLock(z64/%d, z64/%d, z64/%d, constMinerVersion, z64/1)", r, b, c))
		}}))
		require.Error(t, err)
		// only the pinned successor check reads V as inline data, and an expression is not
		require.NoError(t, util.MustErrorWith(err, "not an inline data function"))
	})
	t.Run("refused: a transaction-hash transit before the slot", func(t *testing.T) {
		u, priv := newEnv(t)
		require.Error(t, u.AddTransaction(buildHashTransit(t, u, priv, upgrade6Slot-5, hashTransitOpts{})))
	})
}

// EasyFL accepts exactly the signature data the Go ID derivation accepts from the slot:
// signaturePublicKey, through which every signature check and holder ID reads the key,
// refuses anything but the 97-byte ED25519 encoding from the slot, and reads the key at
// its offset before it.
func TestUpgrade6CanonicalSignatureData(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6(t, u)
	sig := base.SignatureDataED25519(genesisPrivateKey, make([]byte, 32))
	padded := append(append([]byte{}, sig...), 0xee)
	pub := fmt.Sprintf("%x", sig[65:])
	eval := func(slot uint32, data []byte) (string, error) {
		res, err := ledger.L(slot).EvalFromSource(nil, fmt.Sprintf("signaturePublicKey(0x%x)", data))
		return fmt.Sprintf("%x", res), err
	}
	for _, slot := range []uint32{upgrade6Slot - 1, upgrade6Slot} {
		key, err := eval(slot, sig)
		require.NoError(t, err)
		require.Equal(t, pub, key, "the canonical encoding is read on both sides of the slot")
	}
	key, err := eval(upgrade6Slot-1, padded)
	require.NoError(t, err)
	require.Equal(t, pub, key, "before the slot the key is read at its offset whatever follows")
	_, err = eval(upgrade6Slot, padded)
	require.Error(t, err, "from the slot padded signature data is refused")
	_, err = eval(upgrade6Slot, sig[:96])
	require.Error(t, err, "from the slot short signature data is refused")
}

// Today's transit, the VRF proof and the nonce in the mine input's unlock parameters, is
// valid before the slot and refused from it: from the slot the work is the transaction ID,
// which the VRF search never looked at.
func TestUpgrade6VRFTransitRefusedFromTheSlot(t *testing.T) {
	vrfTransitAt := func(t *testing.T, u *utxodb.UTXODB, slot uint32) []byte {
		md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
		require.NoError(t, err)
		mineIn, err := md.Parse()
		require.NoError(t, err)
		v := constAt(t, slot, "constMinerVersion")
		return buildMineTransition(t, u, genesisPrivateKey, mineTxOpts{mine: true, version: &v, tick: 100, pace: slot - mineIn.ID.Slot()})
	}
	t.Run("before the slot", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		withUpgrade6(t, u)
		require.NoError(t, u.AddTransaction(vrfTransitAt(t, u, upgrade6Slot-5)))
	})
	t.Run("from the slot", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		withUpgrade6(t, u)
		txBytes := vrfTransitAt(t, u, upgrade6Slot+5)
		id, err := txbuildercore.TxIDFromBytes(txBytes)
		require.NoError(t, err)
		if workMeets(id, 6) {
			t.Skip("the VRF transit's ID happened to carry the work")
		}
		err = u.AddTransaction(txBytes)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "insufficient mine proof of work"))
	})
}

// The real upgrade 6 library raises the difficulty floor to 24, and the required difficulty
// and the easing stay defined when B is below the raised floor, as it can be when the floor
// rises after empty slots: K is then the floor, where subtracting the floor from B would
// have failed every transit and halted the mine chain.
func TestUpgrade6DifficultyFloor(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6Built(t, u, ledger.BuildUpgrade6LibraryAt(upgrade6Slot))
	require.EqualValues(t, 6, constAt(t, upgrade6Slot-1, "constMineFloorDifficulty"))
	require.EqualValues(t, 24, constAt(t, upgrade6Slot, "constMineFloorDifficulty"))
	require.EqualValues(t, 24, ledger.L(upgrade6Slot).Constants.MineFloorDifficulty)

	requiredK := func(b, m uint64) uint64 {
		return constAt(t, upgrade6Slot, fmt.Sprintf("_mineRequiredK(u64/%d, u64/%d)", b, m))
	}
	lib := ledger.L(upgrade6Slot)
	for _, c := range []struct{ b, m, want uint64 }{
		{8, 1, 24},  // B below the floor at the minimum pace
		{20, 3, 24}, // and with relief
		{24, 1, 24}, // at the floor
		{37, 1, 37}, // above it, the full B at the minimum pace
		{37, 5, 33}, // one bit off per extra slot
		{37, 20, 24},
	} {
		require.EqualValues(t, c.want, requiredK(c.b, c.m), "B=%d M=%d", c.b, c.m)
		require.EqualValues(t, c.want, lib.MineRequiredK(c.b, c.m), "Go mirror, B=%d M=%d", c.b, c.m)
	}
}

// The node recognises a transit by its role, not its output count, and reads its work by
// the transaction version: a transaction-hash transit with any number of outputs is a
// mining transaction whose work is the low 64 bits of its ID, and its MineSolutionKey
// orders it by slot, then work. A VRF transit keeps its VRF work.
func TestUpgrade6TransitRecognition(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6(t, u)
	priv, _, _ := u.GenerateAddress(3)
	slot := uint32(upgrade6Slot + 5)

	eight := hashTransitOpts{payout: func(a uint64, s ledger.SigLock, chain base.ChainID) []*ledger.Output {
		ret := make([]*ledger.Output, 8)
		for i := range ret {
			ret[i] = ledger.NewTagAlongOutput(a/8, chain, base.HolderID(s))
		}
		ret[7] = ledger.NewTagAlongOutput(a-7*(a/8), chain, base.HolderID(s))
		return ret
	}}
	var keys [][]byte
	for _, opts := range []hashTransitOpts{{}, eight} {
		tx, err := transaction.Parse(buildHashTransit(t, u, priv, slot, opts))
		require.NoError(t, err)
		require.True(t, tx.IsMiningTransaction(), "%d outputs", tx.NumProducedOutputs())
		require.True(t, tx.MineWorkIsTxID())
		v, ok := tx.MineProofOfWork64()
		require.True(t, ok)
		id := tx.ID()
		require.Equal(t, binary.BigEndian.Uint64(id[24:]), v)
		key := tx.MineSolutionKey()
		require.Equal(t, binary.BigEndian.AppendUint32(nil, slot), key[:4], "the slot leads the key")
		require.Equal(t, id[24:], key[4:12], "then the work")
		keys = append(keys, key)
	}
	later, err := transaction.Parse(buildHashTransit(t, u, priv, slot+1, hashTransitOpts{}))
	require.NoError(t, err)
	for _, k := range keys {
		require.Negative(t, bytes.Compare(k, later.MineSolutionKey()), "an older slot ranks first whatever the work")
	}

	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)
	v := constAt(t, upgrade6Slot-5, "constMinerVersion")
	vrfTx, err := transaction.Parse(buildMineTransition(t, u, genesisPrivateKey, mineTxOpts{mine: true, version: &v, tick: 100, pace: upgrade6Slot - 5 - mineIn.ID.Slot()}))
	require.NoError(t, err)
	require.True(t, vrfTx.IsMiningTransaction())
	require.False(t, vrfTx.MineWorkIsTxID(), "a transit before the slot keeps its VRF work")
}

// The real upgrade 6 library pauses mining for Upgrade6MiningPauseSlots after its slot: the
// start slot moves past the activation, a transit inside the pause is refused whatever its
// work, and the wallet constants carry the new start slot.
func TestUpgrade6MiningPause(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6Built(t, u, ledger.BuildUpgrade6LibraryAt(upgrade6Slot))
	resume := upgrade6Slot + ledger.Upgrade6MiningPauseSlots
	require.EqualValues(t, resume, constAt(t, upgrade6Slot, "constDisableMiningUntilSlot"))
	require.EqualValues(t, resume, ledger.L(upgrade6Slot).Constants.DisableMiningUntilSlot)
	require.Less(t, constAt(t, upgrade6Slot-1, "constDisableMiningUntilSlot"), uint64(upgrade6Slot))

	priv, _, _ := u.GenerateAddress(7)
	err := u.AddTransaction(buildHashTransit(t, u, priv, upgrade6Slot+5, hashTransitOpts{failWork: true}))
	require.Error(t, err)
	require.NoError(t, util.MustErrorWith(err, "mining is disabled until the start slot"))
}

// The sequencer pace rises from 3 to 6 ticks at the slot: a milestone consuming an input 4
// ticks older is valid before the slot and refused from it; 6 ticks is enough.
func TestUpgrade6SequencerPace(t *testing.T) {
	cases := []struct {
		name    string
		slot    uint32
		gap     int
		refused bool
	}{
		{"4 ticks, before the slot", upgrade6Slot - 5, 4, false},
		{"4 ticks, from the slot", upgrade6Slot + 5, 4, true},
		{"6 ticks, from the slot", upgrade6Slot + 5, 6, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, 10_000_000_000)
			withUpgrade6(t, e.u)
			require.EqualValues(t, 3, constAt(t, upgrade6Slot-1, "constTransactionPaceSequencer"))
			require.EqualValues(t, 6, constAt(t, upgrade6Slot, "constTransactionPaceSequencer"))
			chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(c.slot, 20))
			err := e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(c.slot, byte(20+c.gap)), e.privKey, e.addr))
			if !c.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

// withdrawMilestone is buildMilestone that also moves amount out of the chain to a sigLock
// output of the same key.
func (e *sequencerTestEnv) withdrawMilestone(t *testing.T, chainIn *ledger.OutputWithID, chainID base.ChainID, ts base.LedgerTime, amount uint64) []byte {
	t.Helper()
	cc := chainIn.Output.ChainConstraint()
	predSeq, _ := chainIn.Output.SequencerConstraint()
	require.NotNil(t, predSeq)
	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)
	succIdx, err := txb.ProduceOutput(chainIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(chainIn.Output.TokenBalance() - amount))
		o.PutConstraint(ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewSequencerConstraint(predSeq.CoverageDelta+1).Bytes(), ledger.SequencerConstraintFixedIndex)
	}))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(e.addr)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.SetSequencerData(succIdx, txbuildercore.SequencerOutputIndexNone)
	txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-1), base.TransactionIDShort{1}, true))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)
	return txb.Bytes()
}

// From the slot a sequencer needs at least 10,000 PROX of its own for a milestone in the
// pre-branch zone; elsewhere in the slot it needs none. Before the slot the zone is open to any
// balance. The chain starts before the slot: one started from it holds the hard bound, which is
// as high, so only an older chain can be under the soft bound.
func TestUpgrade6SequencerMinimumBalanceZone(t *testing.T) {
	const refused = "sequencer balance below minimum for a branch or the pre-branch zone"
	const minimum = 10_000_000_000 // 10,000 PROX in motes
	cases := []struct {
		name    string
		funds   uint64
		slot    uint32
		tick    byte
		refused bool
	}{
		{"below the minimum, in the zone, before the slot", minimum / 2, upgrade6Slot - 5, 110, false},
		{"below the minimum, in the zone, from the slot", minimum / 2, upgrade6Slot + 5, 110, true},
		{"below the minimum, outside the zone, from the slot", minimum / 2, upgrade6Slot + 5, 60, false},
		{"at the minimum, in the zone, from the slot", minimum, upgrade6Slot + 5, 110, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, c.funds)
			withUpgrade6(t, e.u)
			chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(upgrade6Slot-6, 20))
			if c.tick > 60 {
				// a milestone in the zone needs a chain predecessor of its own slot: one outside it first
				require.NoError(t, e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(c.slot, 60), e.privKey, e.addr)))
				chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
				require.NoError(t, err)
				chainIn, err = chs.Parse()
				require.NoError(t, err)
			}
			err := e.u.AddTransaction(e.buildMilestone(t, chainIn, chainID, base.T(c.slot, c.tick), e.privKey, e.addr))
			if !c.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, refused))
		})
	}
}

// From the slot a target sequencer holding under 10,000 PROX of its own cannot freeze a
// delegation; one holding more can, and before the slot a small one can too. The default
// sequencer of the delegation tests holds 3,000 PROX.
func TestUpgrade6FreezeNeedsMinimumBalance(t *testing.T) {
	for _, c := range []struct {
		name       string
		seqBalance uint64
		slot       uint32
		refused    bool
	}{
		{"small target, before the slot", 0, upgrade6Slot - 20, false},
		{"small target, from the slot", 0, upgrade6Slot + 10, true},
		{"target above the minimum, from the slot", 20_000_000_000, upgrade6Slot + 10, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := &testData{T: t, seqBalance: c.seqBalance}
			td.init()
			withUpgrade6(t, td.u)
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
			require.NoError(t, util.MustErrorWith(err, "target balance below minimum for freezing"))
		})
	}
}

// A sequencer chain started from the slot holds at least 10,000 PROX of its own (the hard bound), on its origin
// and on every later sequencer output, so one stake cannot be recycled into many chains. A
// chain started before the slot is not touched, before or after it.
func TestUpgrade6SequencerOriginMinimumBalance(t *testing.T) {
	const refused = "new sequencer balance below minimum"
	const minimum = 10_000_000_000 // 10,000 PROX in motes

	t.Run("origin below the minimum: valid before the slot, refused from it", func(t *testing.T) {
		for _, slot := range []uint32{upgrade6Slot - 5, upgrade6Slot + 5} {
			e := newSequencerTestEnv(t, minimum-1)
			withUpgrade6(t, e.u)
			err := e.u.AddTransaction(e.buildWalletSequencerOrigin(t, base.T(slot, 20)))
			if slot < upgrade6Slot {
				require.NoError(t, err)
				continue
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, refused))
		}
	})
	t.Run("origin at the minimum from the slot, then withdrawing under it is refused", func(t *testing.T) {
		e := newSequencerTestEnv(t, minimum+1_000_000_000)
		withUpgrade6(t, e.u)
		chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(upgrade6Slot+5, 20))
		// down to exactly the minimum: valid
		require.NoError(t, e.u.AddTransaction(e.withdrawMilestone(t, chainIn, chainID, base.T(upgrade6Slot+5, 30), 1_000_000_000)))
		chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
		require.NoError(t, err)
		chainIn, err = chs.Parse()
		require.NoError(t, err)
		// 10 PROX under it (an output must meet the storage deposit): refused
		err = e.u.AddTransaction(e.withdrawMilestone(t, chainIn, chainID, base.T(upgrade6Slot+5, 40), 10_000_000))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, refused))
	})
	t.Run("a chain started before the slot withdraws under the minimum after it", func(t *testing.T) {
		e := newSequencerTestEnv(t, minimum)
		withUpgrade6(t, e.u)
		chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(upgrade6Slot-5, 20))
		require.NoError(t, e.u.AddTransaction(e.withdrawMilestone(t, chainIn, chainID, base.T(upgrade6Slot+5, 30), minimum/2)))
	})
}

// Under the soft bound a target still tops up a delegation it froze before: the top-up
// continues the freeze, it is no fresh freeze. The delegation tests' sequencer holds 3,000
// PROX, under the soft bound; it freezes before the slot and tops up after it.
func TestUpgrade6TopUpOfFrozenBelowMinimumBalance(t *testing.T) {
	td := &testData{T: t}
	td.init()
	withUpgrade6(t, td.u)
	_, _, err := td.initDelegationUTXOMake(base.T(upgrade6Slot-3, 10), 4, 900)
	require.NoError(t, err)
	ts := base.T(upgrade6Slot-2, 10)
	require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts:                       ts,
		freezeUntilEpoch:         td.delegatedOutput.FreezeUntilMax(ts),
		inflate:                  true,
		disableConsistencyChecks: true,
	}))
	require.True(t, td.delegatedOutput.IsMarkedFrozen())

	topUpTs := base.T(upgrade6Slot+1, 10)
	require.True(t, td.delegatedOutput.IsInFrozenSlot(topUpTs.Slot), "the freeze must still run after the slot")
	req := td.makeTopUpRequestOutput(base.T(upgrade6Slot, 20), td.masterPrivateKey, td.masterAddr, td.delegatedOutput.ChainID, topUpAmount)
	require.NoError(t, td.topUpDelegation(topUpParams{ts: topUpTs, request: req}))
	require.True(t, td.delegatedOutput.IsMarkedFrozen())
}
