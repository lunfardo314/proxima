package tests

// Security-focused tag-along output handling tests.
// These tests cover attack vectors and edge cases for the tag-along lock constraint.
//
// Tag-along lock has three unlock windows based on slot pace (txSlot - outputSlot):
//   1. Tag-along window (0 to TagAlongSlots=30): only target sequencer can consume via chainLock($0)
//   2. Reclaim window (30 to TagAlongReclaimSlots=390): only sender can reclaim via sigLock($1)
//   3. Purge window (390+): anyone can consume (incentivizes ledger cleanup)
//
// Production-time checks:
//   - Lock must be at constraint index 1
//   - Target chain ID must be non-zero and 32 bytes
//   - Max 4 constraints per tag-along output
//   - Sender ID must equal the transaction signer: $1 == txHolderID(txSignatureData)

import (
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// tagAlongTestEnv holds the common test environment for tag-along tests.
type tagAlongTestEnv struct {
	u              *utxodb.UTXODB
	privKeySender  ed25519.PrivateKey
	privKeyTarget  ed25519.PrivateKey
	privKeyRandom  ed25519.PrivateKey
	addrSender     ledger.SigLock
	addrTarget     ledger.SigLock
	addrRandom     ledger.SigLock
	seqOrigin      *ledger.OutputWithChainID
	targetChainID  base.ChainID
	taTs           base.LedgerTime // timestamp of the tag-along output
	tagAlongOutIdx byte
}

const (
	taInitAmount  = 1_000_000_000_000
	taFee         = 100_000_000 // 100M tokens, above min storage deposit
	taChainAmount = 500_000_000_000
)

// setupTagAlongEnv creates a chain and a tag-along output targeting it.
// Returns the environment for further test steps.
func setupTagAlongEnv(t *testing.T) *tagAlongTestEnv {
	t.Helper()
	env := &tagAlongTestEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(314, 3, taInitAmount)
	env.privKeySender = privKeys[0]
	env.addrSender = addrs[0]
	env.privKeyTarget = privKeys[1]
	env.addrTarget = addrs[1]
	env.privKeyRandom = privKeys[2]
	env.addrRandom = addrs[2]

	// derive timestamp from actual outputs to avoid timing races
	targetOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrTarget.ControllerID())
	require.NoError(t, err)
	require.True(t, len(targetOuts) > 0)

	// create chain
	env.seqOrigin, err = env.u.MakeNewChain(taChainAmount, env.privKeyTarget, env.addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
	require.NoError(t, err)
	env.targetChainID = env.seqOrigin.ChainID

	// create tag-along output from sender to target chain
	txb := exhelp.New()
	outs, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrSender.ControllerID())
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	taOutput := ledger.NewTagAlongOutput(taFee, env.targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privKeySender)))
	env.tagAlongOutIdx, err = txb.ProduceOutput(taOutput)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - taFee).WithLock(env.addrSender)
	}))
	require.NoError(t, err)

	env.taTs = env.seqOrigin.ID.Timestamp().AddSlots(2)
	txb.SetTimestamp(env.taTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeySender)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	return env
}

// TestClaudeTagAlongSpoofedSenderID verifies that a third party cannot create a
// tag-along output claiming someone else's HolderID. The EasyFL constraint checks
// $1 == txHolderID(txSignatureData) on production, so the encoded sender must match
// the actual transaction signer. This prevents an attacker from creating a tag-along
// with a victim's sender ID to confuse the sequencer about who can reclaim.
func TestClaudeTagAlongSpoofedSenderID(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 3, taInitAmount)
	privKeyAlice := privKeys[0]
	addrAlice := addrs[0]
	privKeyBob := privKeys[1]
	privKeyTarget := privKeys[2]
	addrTarget := addrs[2]

	// create chain
	targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
	require.NoError(t, err)
	seqOrigin, err := u.MakeNewChain(taChainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
	require.NoError(t, err)

	// Alice signs a tx but puts Bob's HolderID as the sender
	txb := exhelp.New()
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addrAlice.ControllerID())
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// use Bob's HolderID as sender while Alice signs
	bobSenderID := base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeyBob))
	taOutput := ledger.NewTagAlongOutput(taFee, seqOrigin.ChainID, bobSenderID)
	_, err = txb.ProduceOutput(taOutput)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - taFee).WithLock(addrAlice)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(seqOrigin.ID.Timestamp().AddSlots(2))
	txb.ComputeInputCommitment()
	txb.SignED25519(privKeyAlice)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	// Alice signed but sender ID is Bob's -> must fail
	require.Error(t, err, "spoofed sender ID should be rejected")
	require.NoError(t, util.MustErrorWith(err, "sender hash check failed"))
}

// TestClaudeTagAlongWrongSequencerConsumes verifies that a different sequencer
// (chain B) cannot consume a tag-along output targeted at chain A. The chainLock($0)
// validation on consumption checks that the referenced chain ID in the unlock params
// matches $0 (the target sequencer ID). A different chain cannot satisfy this.
func TestClaudeTagAlongWrongSequencerConsumes(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 3, taInitAmount)
	privKeySender := privKeys[0]
	addrSender := addrs[0]
	privKeyTargetA := privKeys[1]
	addrTargetA := addrs[1]
	privKeyTargetB := privKeys[2]
	addrTargetB := addrs[2]

	// create chain A (the intended target)
	outsA, err := u.SugaredStateReader().GetOutputsForAccount(addrTargetA.ControllerID())
	require.NoError(t, err)
	seqOriginA, err := u.MakeNewChain(taChainAmount, privKeyTargetA, addrTargetA, outsA[0].ID.Timestamp().AddSlots(1))
	require.NoError(t, err)
	chainIDA := seqOriginA.ChainID

	// create chain B (the attacker)
	outsB, err := u.SugaredStateReader().GetOutputsForAccount(addrTargetB.ControllerID())
	require.NoError(t, err)
	seqOriginB, err := u.MakeNewChain(taChainAmount, privKeyTargetB, addrTargetB, outsB[0].ID.Timestamp().AddSlots(1))
	require.NoError(t, err)

	// create tag-along targeting chain A
	txb := exhelp.New()
	senderOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(senderOuts[0].Output, senderOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	taOutput := ledger.NewTagAlongOutput(taFee, chainIDA, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
	_, err = txb.ProduceOutput(taOutput)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(senderOuts[0].Output.TokenBalance() - taFee).WithLock(addrSender)
	}))
	require.NoError(t, err)

	taTs := seqOriginA.ID.Timestamp()
	if seqOriginB.ID.Timestamp().After(taTs) {
		taTs = seqOriginB.ID.Timestamp()
	}
	taTs = taTs.AddSlots(2)
	txb.SetTimestamp(taTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(privKeySender)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	// verify tag-along is in chain A's backlog
	taOuts := u.SugaredStateReader().GetTagAlongBacklog(chainIDA)
	require.EqualValues(t, 1, len(taOuts))

	// chain B tries to consume the tag-along targeted at chain A
	txb2 := exhelp.New()
	// consume chain B's origin
	_, err = txb2.ConsumeOutput(seqOriginB.Output, seqOriginB.ID)
	require.NoError(t, err)
	txb2.PutSignatureUnlock(0)
	txb2.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	// consume the tag-along (targeted at chain A)
	_, err = txb2.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
	require.NoError(t, err)
	// provide unlock params referencing chain B (input 0, constraint 2)
	// but the tag-along's $0 is chain A's ID -> mismatch
	txb2.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

	// produce chain B successor with stolen fee
	next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(seqOriginB.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
		o.WithLock(seqOriginB.Output.Lock())
		cc := ledger.NewChainConstraint(seqOriginB.ChainID, 0, seqOriginB.OriginSlot, 0, 0, seqOriginB.TransitionCounter+1, 0)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb2.ProduceOutput(next)
	require.NoError(t, err)

	txb2.SetTimestamp(taTs.AddSlots(1))
	txb2.ComputeInputCommitment()
	txb2.SignED25519(privKeyTargetB)
	_, _, _, err = txbtest.BuildAndValidate(txb2)
	// chain B references its own chain constraint, but tag-along $0 = chain A ID
	// -> _validChainUnlock checks equal(chainA_ID, chainB_ID) -> fails
	require.Error(t, err, "wrong sequencer should not consume tag-along for another chain")
}

// TestClaudeTagAlongManipulatedUnlockParams verifies that providing wrong unlock
// parameters when consuming a tag-along output is rejected. Specifically:
// - Wrong chain constraint index (pointing to amount constraint instead of chain)
// - Self-referencing output index (tag-along references itself)
func TestClaudeTagAlongManipulatedUnlockParams(t *testing.T) {
	// NOTE: "wrong chain constraint index" subtest removed — chain constraint index
	// is now always implicit (ConstraintIndexChain=2), so that attack vector no longer exists.

	t.Run("self-referencing unlock params", func(t *testing.T) {
		env := setupTagAlongEnv(t)
		taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb := exhelp.New()
		_, err := txb.ConsumeOutput(env.seqOrigin.Output, env.seqOrigin.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		taIdx, err := txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		// self-reference: tag-along unlock params point to itself
		// EasyFL chainLock checks: not(equal(selfOutputIndex, byte(selfUnlockParameters,0)))
		txb.PutUnlockParams(taIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(taIdx))

		next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(env.seqOrigin.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
			o.WithLock(env.seqOrigin.Output.Lock())
			cc := ledger.NewChainConstraint(env.targetChainID, 0, env.seqOrigin.OriginSlot, 0, 0, env.seqOrigin.TransitionCounter+1, 0)
			o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		})
		_, err = txb.ProduceOutput(next)
		require.NoError(t, err)

		txb.SetTimestamp(env.taTs.AddSlots(1))
		txb.ComputeInputCommitment()
		txb.SignED25519(env.privKeyTarget)
		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.Error(t, err, "self-referencing unlock params should be rejected")
	})
}

// TestClaudeTagAlongPurgeWindowSettle verifies that in the purge window (slot pace
// >= TagAlongReclaimSlots), any party can consume the tag-along output and the
// transaction settles in UTXODB. This incentivizes ledger cleanup of abandoned
// tag-along outputs. The test also verifies the funds are correctly transferred.
func TestClaudeTagAlongPurgeWindowSettle(t *testing.T) {
	env := setupTagAlongEnv(t)
	taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 1, len(taOuts))

	// get random party's outputs
	randomOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrRandom.ControllerID())
	require.NoError(t, err)
	randomOuts = util.PurgeSlice(randomOuts, func(o *ledger.OutputWithID) bool {
		return o.Output.Lock().Name() == ledger.SigLockName
	})
	require.True(t, len(randomOuts) > 0)

	maxOut := slices.MaxFunc(randomOuts, func(a, b *ledger.OutputWithID) int {
		if a.Output.TokenBalance() < b.Output.TokenBalance() {
			return -1
		}
		if a.Output.TokenBalance() > b.Output.TokenBalance() {
			return 1
		}
		return 0
	})

	initialRandomBalance := maxOut.Output.TokenBalance()

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
	require.NoError(t, err)
	err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
	require.NoError(t, err)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
		o.WithLock(env.addrRandom)
	}))
	require.NoError(t, err)

	// purge window: slot pace >= TagAlongReclaimSlots (390)
	txb.SetTimestamp(env.taTs.AddSlots(ledger.L(0).TagAlongReclaimSlots))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeyRandom)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "random party should consume tag-along in purge window")

	// settle in UTXODB
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err, "purge window tx should settle")

	// verify backlog is cleared
	taOutsAfter := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 0, len(taOutsAfter), "backlog should be empty after purge")

	// verify random party received the funds
	randomOutsFinal, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrRandom.ControllerID())
	require.NoError(t, err)
	finalBalance := uint64(0)
	for _, o := range randomOutsFinal {
		if o.Output.Lock().Name() == ledger.SigLockName {
			finalBalance += o.Output.TokenBalance()
		}
	}
	// random party should have gained the tag-along fee amount
	require.EqualValues(t, initialRandomBalance+taFee, finalBalance,
		"random party should gain the tag-along fee amount")
}

// TestClaudeTagAlongTargetBalanceTampering verifies that the target sequencer
// cannot claim more tokens than available when consuming a tag-along output.
// The amount conservation invariant (consumed = produced) prevents the target
// from inflating its balance beyond chain_amount + fee.
func TestClaudeTagAlongTargetBalanceTampering(t *testing.T) {
	env := setupTagAlongEnv(t)
	taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 1, len(taOuts))

	txb := exhelp.New()
	_, err := txb.ConsumeOutput(env.seqOrigin.Output, env.seqOrigin.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
	require.NoError(t, err)
	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

	// produce chain output with inflated balance: chain_amount + fee + extra
	extra := uint64(1_000_000)
	inflatedBalance := env.seqOrigin.Output.TokenBalance() + taOuts[0].Output.TokenBalance() + extra
	next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(inflatedBalance)
		o.WithLock(env.seqOrigin.Output.Lock())
		cc := ledger.NewChainConstraint(env.targetChainID, 0, env.seqOrigin.OriginSlot, 0, 0, env.seqOrigin.TransitionCounter+1, 0)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(next)
	require.NoError(t, err)

	txb.SetTimestamp(env.taTs.AddSlots(1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeyTarget)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	// consumed = chain_amount + fee, produced = chain_amount + fee + extra -> mismatch
	require.Error(t, err, "inflated balance should be rejected by amount conservation")
	require.NoError(t, util.MustErrorWith(err, "mismatch between token amounts"))
}

// TestClaudeTagAlongSenderHashForgeryOnReclaim verifies that during the reclaim
// window, only the actual sender (whose HolderID was embedded at production time)
// can reclaim. A third party cannot forge the sender's identity because sigLock($1)
// requires the transaction signature to match the stored HolderID.
func TestClaudeTagAlongSenderHashForgeryOnReclaim(t *testing.T) {
	env := setupTagAlongEnv(t)
	taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 1, len(taOuts))

	// random party tries to reclaim in the reclaim window
	randomOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrRandom.ControllerID())
	require.NoError(t, err)
	randomOuts = util.PurgeSlice(randomOuts, func(o *ledger.OutputWithID) bool {
		return o.Output.Lock().Name() == ledger.SigLockName
	})
	require.True(t, len(randomOuts) > 0)

	maxOut := slices.MaxFunc(randomOuts, func(a, b *ledger.OutputWithID) int {
		if a.Output.TokenBalance() < b.Output.TokenBalance() {
			return -1
		}
		if a.Output.TokenBalance() > b.Output.TokenBalance() {
			return 1
		}
		return 0
	})

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
	require.NoError(t, err)
	err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
	require.NoError(t, err)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
		o.WithLock(env.addrRandom)
	}))
	require.NoError(t, err)

	// reclaim window: TagAlongSlots <= pace < TagAlongReclaimSlots
	txb.SetTimestamp(env.taTs.AddSlots(ledger.L(0).TagAlongSlots + 10))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeyRandom)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	// random party signed, but sigLock($1) checks against sender's HolderID -> mismatch
	require.Error(t, err, "random party should not be able to reclaim in reclaim window")
	require.NoError(t, util.MustErrorWith(err, "inside reclaim slots must be unlocked by the sender"))
}

// TestClaudeTagAlongValidTargetConsumptionSettles is a positive end-to-end test:
// target sequencer correctly consumes a tag-along in the tag-along window and
// the transaction settles in UTXODB. Verifies the chain balance increases by
// exactly the fee amount.
func TestClaudeTagAlongValidTargetConsumptionSettles(t *testing.T) {
	env := setupTagAlongEnv(t)
	taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 1, len(taOuts))

	initialChainBalance := env.seqOrigin.Output.TokenBalance()

	txb := exhelp.New()
	_, err := txb.ConsumeOutput(env.seqOrigin.Output, env.seqOrigin.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
	require.NoError(t, err)
	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

	expectedBalance := initialChainBalance + taFee
	next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(expectedBalance)
		o.WithLock(env.seqOrigin.Output.Lock())
		cc := ledger.NewChainConstraint(env.targetChainID, 0, env.seqOrigin.OriginSlot, 0, 0, env.seqOrigin.TransitionCounter+1, 0)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(next)
	require.NoError(t, err)

	txb.SetTimestamp(env.taTs.AddSlots(1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeyTarget)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "valid target consumption should pass validation")

	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err, "valid target consumption should settle")

	// verify backlog is cleared
	taOutsAfter := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 0, len(taOutsAfter), "backlog should be empty after consumption")
}

// TestClaudeTagAlongSenderReclaimSettles is a positive end-to-end test:
// sender correctly reclaims a tag-along in the reclaim window and the
// transaction settles in UTXODB. Verifies the sender recovers the fee.
func TestClaudeTagAlongSenderReclaimSettles(t *testing.T) {
	env := setupTagAlongEnv(t)
	taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 1, len(taOuts))

	// get sender's current sigLock outputs
	senderOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrSender.ControllerID())
	require.NoError(t, err)
	senderOuts = util.PurgeSlice(senderOuts, func(o *ledger.OutputWithID) bool {
		return o.Output.Lock().Name() == ledger.SigLockName
	})
	require.True(t, len(senderOuts) > 0)
	maxOut := slices.MaxFunc(senderOuts, func(a, b *ledger.OutputWithID) int {
		if a.Output.TokenBalance() < b.Output.TokenBalance() {
			return -1
		}
		if a.Output.TokenBalance() > b.Output.TokenBalance() {
			return 1
		}
		return 0
	})

	preReclaimBalance := maxOut.Output.TokenBalance()

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
	require.NoError(t, err)
	err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
	require.NoError(t, err)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
		o.WithLock(env.addrSender)
	}))
	require.NoError(t, err)

	// reclaim window
	txb.SetTimestamp(env.taTs.AddSlots(ledger.L(0).TagAlongSlots + 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeySender)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "sender reclaim should pass validation")

	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err, "sender reclaim should settle")

	// verify backlog is cleared
	taOutsAfter := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 0, len(taOutsAfter), "backlog should be empty after reclaim")

	// verify sender recovered funds: consolidated output = preReclaimBalance + fee
	senderOutsFinal, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrSender.ControllerID())
	require.NoError(t, err)
	finalBalance := uint64(0)
	for _, o := range senderOutsFinal {
		if o.Output.Lock().Name() == ledger.SigLockName {
			finalBalance += o.Output.TokenBalance()
		}
	}
	require.EqualValues(t, preReclaimBalance+taFee, finalBalance,
		"sender should recover the tag-along fee")
}
