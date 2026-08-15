// Reference-unlock scope tests.
//
// `unlockedByReference` (def/lock_signature.easyfl) lets input i be
// unlocked by pointing at an earlier input carrying the SAME lock
// bytecode and the SAME index-value at position 0. Its soundness
// argument — "the referenced input validated, and it has my lock and my
// holder, so my holder signed" — holds only for the plain sigLock,
// whose single consume path IS the holder's signature.
//
// Every conditional lock has several consume paths, so a referenced
// input of that kind may have been unlocked through a branch that says
// nothing about its holder's consent: a sendWithDeadline by public
// cleanup or by its target, a tagAlong by the target sequencer's chain,
// a delegateLock by the target, a dex order by a paying counterparty.
// Those locks reach the shortcut two ways — via `_sigLock`
// (sendWithDeadline, tagAlong, delegateLock, htlc) and via the public
// `sigLock` (the dex orders' reclaim window) — so the guard lives
// inside `unlockedByReference` itself as a lock-type check.
//
// One test per reachable lock: each builds the theft transaction that
// the missing guard used to accept, and requires rejection. Companion
// tests pin the legitimate paths that must keep working.

package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

const (
	refInitAmount = 1_000_000_000_000
	refSWDAmount  = 250_000_000
)

// refEnv holds two sendWithDeadline outputs issued by the SAME master
// with IDENTICAL lock arguments — one to the attacker, one to the
// victim — plus the wallets involved.
type refEnv struct {
	u                                    *utxodb.UTXODB
	privMaster, privVictim, privAttacker ed25519.PrivateKey
	addrMaster, addrVictim, addrAttacker ledger.SigLock
	outToAttacker, outToVictim           *ledger.OutputWithID
	createSlot                           uint32
	accept, cleanup                      uint32
}

// makeRefEnv produces, in one master-signed transaction, two
// sendWithDeadline outputs with the same (targetType, acceptanceSlots,
// cleanupSlots) — hence byte-identical lock bytecode — differing only
// in the target stored in the index-value tuple.
func makeRefEnv(t *testing.T, accept, cleanup uint32) *refEnv {
	t.Helper()
	env := &refEnv{accept: accept, cleanup: cleanup}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(11, 3, refInitAmount)
	env.privMaster, env.privVictim, env.privAttacker = privKeys[0], privKeys[1], privKeys[2]
	env.addrMaster, env.addrVictim, env.addrAttacker = addrs[0], addrs[1], addrs[2]

	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privMaster))
	victimID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privVictim))
	attackerID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privAttacker))

	masterOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrMaster.ControllerID())
	require.NoError(t, err)
	require.True(t, len(masterOuts) > 0)
	ts := masterOuts[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	newSWD := func(target base.HolderID) *ledger.Output {
		return ledger.NewSendWithDeadlineOutput(refSWDAmount, &ledger.SendWithDeadlineLock{
			MasterID:        masterID,
			TargetID:        target,
			TargetType:      ledger.SendWithDeadlineTargetSigLock,
			AcceptanceSlots: accept,
			CleanupSlots:    cleanup,
		})
	}
	outAtt, outVic := newSWD(attackerID), newSWD(victimID)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	idxAtt, err := txb.ProduceOutput(outAtt)
	require.NoError(t, err)
	idxVic, err := txb.ProduceOutput(outVic)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - 2*refSWDAmount).WithLock(env.addrMaster)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privMaster)

	txBytes, txid, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "swd produce tx must validate")
	require.NoError(t, env.u.AddTransaction(txBytes))

	env.outToAttacker = &ledger.OutputWithID{ID: base.MustNewOutputID(txid, idxAtt), Output: outAtt}
	env.outToVictim = &ledger.OutputWithID{ID: base.MustNewOutputID(txid, idxVic), Output: outVic}
	env.createSlot = ts.Slot
	return env
}

// TestRefUnlockSWDCrossTargetTheft: the attacker legitimately accepts
// its OWN sendWithDeadline output as input 0, then points input 1 —
// the victim's output from the same master with identical lock args —
// at input 0 with a reference unlock. `unlockedByReference` compares
// index-value 0, which for sendWithDeadline is the MASTER, not the
// party the lock is checking (the target), so the reference proves
// nothing. This must be rejected.
func TestRefUnlockSWDCrossTargetTheft(t *testing.T) {
	env := makeRefEnv(t, 60, 1100)

	txb := exhelp.New()
	// input 0 — attacker's own SWD, accepted inside the acceptance window
	_, err := txb.ConsumeOutput(env.outToAttacker.Output, env.outToAttacker.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	// input 1 — victim's SWD, reference-unlocked against input 0
	_, err = txb.ConsumeOutput(env.outToVictim.Output, env.outToVictim.ID)
	require.NoError(t, err)
	require.NoError(t, txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0))

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(2 * refSWDAmount).WithLock(env.addrAttacker)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.createSlot+10, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privAttacker)

	err = env.u.AddTransaction(txb.Bytes())
	require.Error(t, err, "reference unlock must not let one sendWithDeadline target claim another's output")
}

// TestRefUnlockSigLockSameHolder pins the legitimate use: two plain
// sigLock outputs of the same holder, the second reference-unlocked
// against the first. This is the case `unlockedByReference` exists for
// and must keep working.
func TestRefUnlockSigLockSameHolder(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(13, 2, refInitAmount)
	priv, addr := privKeys[0], addrs[0]

	// The faucet funds one output per address; split it in two so the
	// sweep below has a genuine reference-unlock pair.
	seed, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.True(t, len(seed) > 0)
	splitTxb := exhelp.New()
	_, err = splitTxb.ConsumeOutput(seed[0].Output, seed[0].ID)
	require.NoError(t, err)
	splitTxb.PutSignatureUnlock(0)
	half := seed[0].Output.TokenBalance() / 2
	for _, amount := range []uint64{half, seed[0].Output.TokenBalance() - half} {
		_, err = splitTxb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(amount).WithLock(addr)
		}))
		require.NoError(t, err)
	}
	splitTs := seed[0].ID.Timestamp().AddSlots(1)
	if splitTs.IsSlotBoundary() {
		splitTs = splitTs.AddTicks(1)
	}
	splitTxb.SetTimestamp(splitTs)
	splitTxb.ComputeInputCommitment()
	splitTxb.SignED25519(priv)
	require.NoError(t, u.AddTransaction(splitTxb.Bytes()))

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.True(t, len(outs) >= 2, "wallet must hold at least two sigLock outputs")

	txb := exhelp.New()
	total := uint64(0)
	for i, o := range outs[:2] {
		_, err = txb.ConsumeOutput(o.Output, o.ID)
		require.NoError(t, err)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
		total += o.Output.TokenBalance()
	}
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(total).WithLock(addr)
	}))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)

	require.NoError(t, u.AddTransaction(txb.Bytes()), "sigLock reference unlock between same-holder inputs must validate")
}

// =============================================================================
// tagAlong — sender-reclaim window vs. the public window
// =============================================================================

// tagAlong bytecode is 0-arg, so EVERY tagAlong output in existence carries
// byte-identical lock bytes; only the index-value tuple (sender, target)
// differs. Its consume windows, from def/lock_tag_along.easyfl:
//
//	Δ <  constTagAlongSlots        (30)  → target sequencer's chain unlocks
//	30 ≤ Δ < constTagAlongReclaimSlots (390) → sender unlocks (delegates to _sigLock)
//	Δ ≥ 390                              → anyone unlocks (public)
const (
	refTagAlongSlots        = 30
	refTagAlongReclaimSlots = 390
	refTagAlongFee          = 500
)

// refMakeTagAlong produces one tag-along output from `sender` to
// `targetChainID` in its own transaction stamped at slot `slot`, funding it
// from the sender's fattest pure sigLock UTXO and returning the change.
func refMakeTagAlong(t *testing.T, u *utxodb.UTXODB, priv ed25519.PrivateKey, addr ledger.SigLock, targetChainID base.ChainID, slot uint32) *ledger.OutputWithID {
	t.Helper()
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	var funding *ledger.OutputWithID
	for _, o := range outs {
		if o.Output.NumElements() == 3 && (funding == nil || o.Output.TokenBalance() > funding.Output.TokenBalance()) {
			funding = o
		}
	}
	require.NotNil(t, funding, "sender must hold a plain sigLock UTXO to fund the tag-along")

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(funding.Output, funding.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	taOut := ledger.NewTagAlongOutput(refTagAlongFee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(priv)))
	taIdx, err := txb.ProduceOutput(taOut)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(funding.Output.TokenBalance() - refTagAlongFee).WithLock(addr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(base.T(slot, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)

	txBytes, txid, failed, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "tag-along produce tx must validate:\n%s", failed)
	require.NoError(t, u.AddTransaction(txBytes))
	return &ledger.OutputWithID{ID: base.MustNewOutputID(txid, taIdx), Output: taOut}
}

// refTagAlongEnv sets up a target sequencer chain plus two tag-along outputs
// from the same sender, spaced so that at one transaction slot the older one
// sits in the public window and the newer one in the sender-reclaim window.
type refTagAlongEnv struct {
	u                        *utxodb.UTXODB
	privSender, privAttacker ed25519.PrivateKey
	addrSender, addrAttacker ledger.SigLock
	expired, victim          *ledger.OutputWithID
	spendSlot                uint32
}

func makeRefTagAlongEnv(t *testing.T) *refTagAlongEnv {
	t.Helper()
	env := &refTagAlongEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(17, 3, refInitAmount)
	env.privSender, env.privAttacker = privKeys[0], privKeys[2]
	env.addrSender, env.addrAttacker = addrs[0], addrs[2]
	privTarget, addrTarget := privKeys[1], addrs[1]

	targetOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
	require.NoError(t, err)
	require.True(t, len(targetOuts) > 0)
	chain, err := env.u.MakeNewChain(refInitAmount/2, privTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
	require.NoError(t, err)

	base0 := chain.ID.Timestamp().Slot + 1
	// The older output goes public (Δ ≥ 390) by the spend slot; the newer one
	// is still inside the sender's reclaim window (30 ≤ Δ < 390).
	env.expired = refMakeTagAlong(t, env.u, env.privSender, env.addrSender, chain.ChainID, base0)
	env.victim = refMakeTagAlong(t, env.u, env.privSender, env.addrSender, chain.ChainID, base0+refTagAlongReclaimSlots+10)
	env.spendSlot = base0 + refTagAlongReclaimSlots + 10 + refTagAlongSlots + 20

	require.GreaterOrEqual(t, int(env.spendSlot-env.expired.ID.Slot()), refTagAlongReclaimSlots, "input 0 must be publicly claimable")
	delta := env.spendSlot - env.victim.ID.Slot()
	require.GreaterOrEqual(t, int(delta), refTagAlongSlots, "victim must be past the target's window")
	require.Less(t, int(delta), refTagAlongReclaimSlots, "victim must still be inside the sender's reclaim window")
	return env
}

// TestRefUnlockTagAlongPublicToReclaimTheft: an expired, publicly-claimable
// tag-along of sender S is consumed at input 0 by an arbitrary third party
// (legitimate — the public window asks for no unlock at all). Input 1 is a
// second tag-along of the same S that is still in S's own reclaim window, and
// is pointed at input 0. Since every tagAlong shares one bytecode and both
// carry S at index-value 0, the reference check used to pass and hand the
// attacker funds only S may reclaim.
func TestRefUnlockTagAlongPublicToReclaimTheft(t *testing.T) {
	env := makeRefTagAlongEnv(t)

	txb := exhelp.New()
	_, err := txb.ConsumeOutput(env.expired.Output, env.expired.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	_, err = txb.ConsumeOutput(env.victim.Output, env.victim.ID)
	require.NoError(t, err)
	require.NoError(t, txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0))
	// tag-along outputs are dust-exempt but the sweep output is not, so the
	// attacker also funds the transaction from its own wallet
	funding := refFattestSigLockOutput(t, env.u, env.addrAttacker)
	_, err = txb.ConsumeOutput(funding.Output, funding.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(2)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(2*refTagAlongFee + funding.Output.TokenBalance()).WithLock(env.addrAttacker)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.spendSlot, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privAttacker)

	require.Error(t, env.u.AddTransaction(txb.Bytes()),
		"a publicly-claimable tag-along must not reference-unlock another tag-along in its sender's reclaim window")
}

// TestRefUnlockTagAlongSenderReclaims pins the legitimate mass-reclaim the
// wallet performs: the sender sweeps both of its own tag-along outputs with a
// signature unlock on each. This is the path `proxi node compact` uses, and it
// must not depend on reference unlock.
func TestRefUnlockTagAlongSenderReclaims(t *testing.T) {
	env := makeRefTagAlongEnv(t)

	txb := exhelp.New()
	for i, in := range []*ledger.OutputWithID{env.expired, env.victim} {
		_, err := txb.ConsumeOutput(in.Output, in.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(byte(i))
	}
	funding := refFattestSigLockOutput(t, env.u, env.addrSender)
	_, err := txb.ConsumeOutput(funding.Output, funding.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(2)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(2*refTagAlongFee + funding.Output.TokenBalance()).WithLock(env.addrSender)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.spendSlot, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privSender)

	require.NoError(t, env.u.AddTransaction(txb.Bytes()),
		"the sender must be able to reclaim its own tag-along outputs with signature unlocks")
}

// refFattestSigLockOutput returns the account's largest plain 3-element
// sigLock UTXO — used to fund transactions whose other inputs are dust.
func refFattestSigLockOutput(t *testing.T, u *utxodb.UTXODB, addr ledger.SigLock) *ledger.OutputWithID {
	t.Helper()
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	var ret *ledger.OutputWithID
	for _, o := range outs {
		if o.Output.NumElements() == 3 && (ret == nil || o.Output.TokenBalance() > ret.Output.TokenBalance()) {
			ret = o
		}
	}
	require.NotNil(t, ret, "account must hold a plain sigLock UTXO")
	return ret
}

// =============================================================================
// dex buyOrder — counterparty window vs. the issuer's reclaim window
// =============================================================================

// The dex order locks reach the shortcut differently from the locks above:
// their reclaim window calls the PUBLIC `sigLock` wrapper rather than
// `_sigLock`. That is why the guard cannot live at the wrapper boundary and
// has to sit inside `unlockedByReference` itself.
//
// A buyOrder's bytecode is sellOrder-free of any per-order identity — it is
// just buyOrder(amount, price, timeoutSlots) — so two orders from one buyer
// with the same terms are byte-identical, and both carry the buyer at
// index-value 0.

// refBuildBuyOrderAt builds one buy order stamped at an explicit slot, so two
// orders can be placed far enough apart that a single transaction sees one
// inside its counterparty window and the other past its timeout.
func refBuildBuyOrderAt(t *testing.T, e *dexEnv, tag base.ChainID, amount, price uint64, timeoutSlots uint32, deposit uint64, slot uint32) *ledger.OutputWithID {
	t.Helper()
	pure := pureSigLockOnly(dexOutputsOf(t, e, e.buyerLock))
	require.NotEmpty(t, pure)

	txb := exhelp.New()
	totalBase, _, err := txb.ConsumeOutputsUnlock(pure...)
	require.NoError(t, err)
	require.GreaterOrEqual(t, totalBase, deposit)

	order := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(deposit)).WithLock(&ledger.BuyOrderLock{
			BuyerHolderID: base.HolderID(ledger.SigLockFromED25519PrivateKey(e.buyerPriv)),
			Tag:           tag,
			Amount:        amount,
			Price:         price,
			TimeoutSlots:  timeoutSlots,
		})
	})
	idx, err := txb.ProduceOutput(order)
	require.NoError(t, err)
	if change := totalBase - deposit; change > 0 {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), e.buyerLock))
		require.NoError(t, err)
	}
	txb.SetTimestamp(base.T(slot, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(e.buyerPriv)
	return dexLoadOutput(t, e, dexOidFromTx(t, dexSubmit(t, e, txb), byte(idx)))
}

// TestRefUnlockBuyOrderFillToReclaimTheft: the seller legitimately fills the
// buyer's fresh order at input 0 — delivering tokens through the receipt — and
// in the same transaction points input 1, an EXPIRED order of the same buyer,
// back at input 0. The expired order is in the buyer's reclaim window, where
// only the buyer's signature should open it, but both orders share bytecode
// and buyer, so the reference used to carry it and hand the seller the whole
// second deposit for free.
func TestRefUnlockBuyOrderFillToReclaimTheft(t *testing.T) {
	e := newDexEnv(t)
	const (
		amount       = uint64(10)
		price        = uint64(50_000_000)
		timeoutSlots = uint32(50)
		deposit      = uint64(2_000_000_000)
	)
	tag, tokenUTXO := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amount)

	baseSlot := tokenUTXO.Timestamp().Slot + 1
	expired := refBuildBuyOrderAt(t, e, tag, amount, price, timeoutSlots, deposit, baseSlot)
	fresh := refBuildBuyOrderAt(t, e, tag, amount, price, timeoutSlots, deposit, baseSlot+timeoutSlots+5)
	fillSlot := baseSlot + timeoutSlots + 6
	require.Less(t, int(fillSlot-fresh.ID.Slot()), int(timeoutSlots), "fresh order must be inside its counterparty window")
	require.GreaterOrEqual(t, int(fillSlot-expired.ID.Slot()), int(timeoutSlots), "expired order must be past its timeout")

	txb := exhelp.New()
	freshIdx, err := txb.ConsumeOutput(fresh.Output, fresh.ID)
	require.NoError(t, err)
	expiredIdx, err := txb.ConsumeOutput(expired.Output, expired.ID)
	require.NoError(t, err)
	require.NoError(t, txb.PutUnlockReference(expiredIdx, ledger.ConstraintIndexLock, freshIdx))
	tokIdx, err := txb.ConsumeOutput(tokenUTXO.Output, tokenUTXO.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(tokIdx)

	buyerSigLock := ledger.SigLockFromED25519PrivateKey(e.buyerPriv)
	receiptIdx, err := txb.ProduceOutput(dexBuyReceipt(deposit-amount*price, buyerSigLock, byte(freshIdx), tag, amount))
	require.NoError(t, err)
	txb.PutUnlockParams(freshIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	// the seller pockets the legitimate payment PLUS the whole expired deposit
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(amount*price+tokenUTXO.Output.TokenBalance()+deposit), e.sellerLock))
	require.NoError(t, err)

	txb.DeclareTokenConservation(tag)
	txb.SetTimestamp(base.T(fillSlot, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(e.sellerPriv)

	require.Error(t, e.u.AddTransaction(txb.Bytes()),
		"filling one buy order must not reference-unlock another expired order of the same buyer")
}

// TestRefUnlockBuyOrderIssuerReclaims pins the legitimate reclaim: after the
// timeout the buyer takes its own order back with a signature unlock.
func TestRefUnlockBuyOrderIssuerReclaims(t *testing.T) {
	e := newDexEnv(t)
	const (
		amount       = uint64(10)
		price        = uint64(50_000_000)
		timeoutSlots = uint32(50)
		deposit      = uint64(2_000_000_000)
	)
	tag, tokenUTXO := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amount)
	baseSlot := tokenUTXO.Timestamp().Slot + 1
	order := refBuildBuyOrderAt(t, e, tag, amount, price, timeoutSlots, deposit, baseSlot)

	txb := exhelp.New()
	idx, err := txb.ConsumeOutput(order.Output, order.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(idx)
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(deposit), e.buyerLock))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(baseSlot+timeoutSlots+1, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(e.buyerPriv)

	require.NoError(t, e.u.AddTransaction(txb.Bytes()),
		"the buyer must be able to reclaim its own expired order with a signature unlock")
}

// =============================================================================
// delegateLock — the target's transit vs. the master's withdrawal
// =============================================================================

// delegateLock picks its consume path from the SECOND unlock byte: 0xff means
// "unlocked by the master" (a withdrawal, which may discontinue the chain and
// take the whole balance), anything else means "unlocked by the target". The
// FIRST byte is what `_sigLock` reads as a reference index, so a delegation can
// name an earlier consumed input on the master path.
//
// The theft: a sequencer legitimately transits one delegation it is the target
// of, and in the same transaction claims a SECOND delegation of the same master
// as if the master had signed, by pointing it at the first. Two delegations to
// the same target with the same terms carry identical lock bytecode and the
// same master at index-value 0, which is all the reference check looked at.
func TestRefUnlockDelegationTargetStealsSiblingDelegation(t *testing.T) {
	td := &testData{T: t}
	td.init()

	pace := int(ledger.L(0).TransactionPace)
	ts1 := td.seqChainOrigin.Timestamp().AddTicks(pace)
	_, _, err := td.initDelegationUTXOMake(ts1, 2, 0)
	require.NoError(t, err)
	d1 := td.delegatedOutput
	_, _, err = td.initDelegationUTXOMake(ts1.AddSlots(1), 2, 0)
	require.NoError(t, err)
	d2 := td.delegatedOutput

	require.NotEqual(t, d1.ChainID, d2.ChainID, "two distinct delegations expected")
	require.Equal(t,
		d1.Output.MustAt(int(ledger.ConstraintIndexLock)),
		d2.Output.MustAt(int(ledger.ConstraintIndexLock)),
		"same master, target and terms must yield byte-identical delegateLock bytecode — the precondition of the attack")

	ts := d2.Timestamp().AddSlots(1)
	frozen := int64(d1.Output.TokenBalance())

	txb := exhelp.New()
	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID, &d1.OutputWithID, &d2.OutputWithID)
	require.NoError(t, err)

	// produced 0 — the sequencer chain successor
	succSeqChain := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	predSeq, predSeqIdx := td.seqChainOrigin.Output.SequencerConstraint()
	require.NotEqualValues(t, 0xff, predSeqIdx)
	succSeq := ledger.NewSequencerConstraint(predSeq.CoverageDelta+1)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance()), 0, frozen)
		o.WithLock(td.seqChainOrigin.Output.Lock())
		o.PutConstraint(succSeqChain.Bytes(), ledger.ConstraintIndexChain)
		require.EqualValues(t, ledger.SequencerConstraintFixedIndex, o.MustPushConstraint(succSeq.Bytes()))
	}))
	require.NoError(t, err)

	// produced 1 — the legitimately transited delegation d1
	succD1 := ledger.NewChainConstraint(d1.ChainID, 1, d1.OriginSlot, 0, 0, d1.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(d1.Output.TokenBalance()), 0, frozen)
		o.WithLock(d1.Output.Lock())
		o.PutConstraint(succD1.Bytes(), ledger.ConstraintIndexChain)
		txEpoch := ledger.L(0).EpochFromSlotDirect(d1.Target, ts.Slot, d1.EpochSlots())
		o.MustPushConstraint(ledger.DelegateLockState{LastFrozenEpoch: txEpoch, State: ledger.DelegateLockStateFrozen}.Bytes())
	}))
	require.NoError(t, err)

	// produced 2 — d2's balance, taken by the sequencer's own controller
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(d2.Output.TokenBalance()), ledger.SigLockFromED25519PrivateKey(td.seqPrivateKey)))
	require.NoError(t, err)

	// input 0 — the sequencer's own chain, signed
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	// input 1 — d1 on the target path: chain-unlock reference + a non-0xff second byte
	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))
	// input 2 — d2 on the MASTER path (second byte 0xff), first byte pointing at
	// input 1 as a reference unlock instead of the master's own signature. The
	// delegation chain is discontinued so the whole balance walks free.
	txb.PutUnlockParams(2, ledger.ConstraintIndexLock, []byte{1, 0xff})
	txb.PutUnlockParams(2, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))
	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SetSequencerData(0, txbuildercore.SequencerOutputIndexNone)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	require.Error(t, err,
		"the delegation target must not reference-unlock a sibling delegation as its master:\n%s", txString)
	_ = txBytes
}

// =============================================================================
// htlc — why it was never exposed, and that it still works
// =============================================================================

// htlc reaches `_sigLock` too, but its two consume paths sit on OPPOSITE sides
// of the deadline: the preimage path needs txSlot < deadline, the signature
// path needs txSlot ≥ deadline. A referenced sibling with identical bytecode
// necessarily carries the same deadline, so at any slot where this output can
// reach `_sigLock` at all, the sibling could only have been opened by the same
// holder's signature. The hash lives in the index-value tuple rather than the
// bytecode, so two htlc outputs of one holder do share bytecode — but that buys
// an attacker nothing for the reason above. Narrowing the shortcut therefore
// changed nothing for htlc; this test pins that the sweep still works.
func TestRefUnlockHTLCHolderSweepsAfterDeadline(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 10_000_000_000)
	holderKey, _, holderAddr := u.GenerateAddress(1)

	srcOuts, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	parsed, err := ledger.ParseAndSortOutputData(srcOuts, nil)
	require.NoError(t, err)
	deadline := parsed[0].ID.Slot() + 5

	// Two htlc outputs, same holder and deadline (hence identical bytecode),
	// different hashes — the shape that would matter if the shortcut applied.
	mk := func(secret string) *ledger.OutputWithID {
		hash := blake2b.Sum256([]byte(secret))
		return loadOutput(t, u, depositToLock(t, u, srcKey, srcAddr, &ledger.HTLC{
			HolderID: base.HolderID(holderAddr),
			Hash:     hash,
			Deadline: deadline,
		}, 500_000_000))
	}
	first, second := mk("secret-one"), mk("secret-two")
	require.Equal(t,
		first.Output.MustAt(int(ledger.ConstraintIndexLock)),
		second.Output.MustAt(int(ledger.ConstraintIndexLock)),
		"same holder and deadline must yield identical htlc bytecode")

	txb := exhelp.New()
	total := uint64(0)
	for i, in := range []*ledger.OutputWithID{first, second} {
		_, err = txb.ConsumeOutput(in.Output, in.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(byte(i))
		total += in.Output.TokenBalance()
	}
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(total), holderAddr))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(deadline+1, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(holderKey)

	require.NoError(t, u.AddTransaction(txb.Bytes()),
		"the holder must be able to sweep its own htlc outputs after the deadline")
}
