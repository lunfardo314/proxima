// UTXODB tests for the returnToSender additive constraint — see
// claude/return_to_sender.md for the design.
//
// returnToSender(amount) rides on a sendWithDeadline (SWD) output and forces
// whoever ACCEPTS the sent tokens to pay `amount` base tokens back to the
// master (sender) in the same tx, via a "return receipt" output. The master
// reclaiming its own funds is unaffected (signer-based discrimination).
//
// Coverage:
//   - produce: valid on an SWD output; rejected on a non-SWD lock; rejected
//     with a zero amount.
//   - consume (master reclaim): master signs, no receipt needed → noop.
//   - consume (target accept): valid receipt settles; rejections for underpaid
//     receipt, wrong payee, non-sigLock receipt, and the fold attack (two
//     inputs sharing one receipt).
package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

const (
	rtsInitAmount = 1_000_000_000_000 // faucet per wallet
	rtsSwdAmount  = 1_000_000_000     // base balance of each SWD output
	rtsReturn     = 200_000_000       // returnToSender amount (well above min storage deposit)
	rtsAccept     = uint32(60)        // acceptanceSlots
	rtsCleanup    = uint32(1100)      // cleanupSlots
	// returnToSender constraint index on the SWD output (first free slot
	// after amounts/indexValues/lock). Also the index where the consumer
	// supplies the 1-byte receipt-output index as unlock params.
	rtsConstraintIndex = byte(3)
)

type rtsEnv struct {
	u                                  *utxodb.UTXODB
	privMaster, privTarget, privThird  ed25519.PrivateKey
	addrMaster, addrTarget, addrThird  ledger.SigLock
	masterID                           base.HolderID
	swd                                []*ledger.OutputWithID // produced SWD+returnToSender outputs
	createSlot                         uint32                 // slot of swd outputs (== setup tx ts)
}

// makeRTSEnv funds three wallets and produces n sendWithDeadline (sigLock
// target == privTarget) outputs, each carrying returnToSender(returnAmount)
// at slot 3. The setup tx itself exercises the produce-side check.
func makeRTSEnv(t *testing.T, n int, returnAmount uint64) *rtsEnv {
	t.Helper()
	env := &rtsEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(7, 3, rtsInitAmount)
	env.privMaster, env.privTarget, env.privThird = privKeys[0], privKeys[1], privKeys[2]
	env.addrMaster, env.addrTarget, env.addrThird = addrs[0], addrs[1], addrs[2]
	env.masterID = base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privMaster))
	targetID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privTarget))

	masterOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrMaster.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, masterOuts)
	in := masterOuts[0]
	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	rtsBin, err := ledger.ReturnToSenderBytecode(returnAmount)
	require.NoError(t, err)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(in.Output, in.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	idxs := make([]byte, 0, n)
	outs := make([]*ledger.Output, 0, n)
	for i := 0; i < n; i++ {
		lock := &ledger.SendWithDeadlineLock{
			MasterID:        env.masterID,
			TargetID:        targetID,
			TargetType:      ledger.SendWithDeadlineTargetSigLock,
			AcceptanceSlots: rtsAccept,
			CleanupSlots:    rtsCleanup,
		}
		// amounts(0) + indexValues(1) + SWD lock(2) via WithLock, then
		// returnToSender(amount) appended at slot 3.
		swd := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(uint64(rtsSwdAmount)).WithLock(lock)
			o.PutConstraint(rtsBin, rtsConstraintIndex)
		})
		idx, perr := txb.ProduceOutput(swd)
		require.NoError(t, perr)
		idxs = append(idxs, idx)
		outs = append(outs, swd)
	}
	change := in.Output.TokenBalance() - uint64(n)*uint64(rtsSwdAmount)
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), env.addrMaster))
	require.NoError(t, err)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privMaster)

	txBytes, txid, failed, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "returnToSender produce tx must validate:\n%s", failed)
	require.NoError(t, env.u.AddTransaction(txBytes))

	env.createSlot = ts.Slot
	for i, idx := range idxs {
		env.swd = append(env.swd, &ledger.OutputWithID{
			ID:     base.MustNewOutputID(txid, idx),
			Output: outs[i],
		})
	}
	return env
}

// rtsReceipt builds a return-receipt output paying `holder`:
//
//	slot 0 amounts   = base
//	slot 1 indexVals = [holder]
//	slot 2 lock      = sigLock
//	slot 3 literal   = inlineData(consumedInputIndex)   (anti-fold binding)
func rtsReceipt(base_ uint64, holder ledger.SigLock, consumedInputIndex byte) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(base_).WithLock(holder)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{consumedInputIndex}))
	})
}

// =============================================================================
// Produce side
// =============================================================================

// The setup tx validates a real SWD+returnToSender produce; if makeRTSEnv
// returns, the produce side accepted it.
func TestRTSProduceHappy(t *testing.T) {
	env := makeRTSEnv(t, 1, rtsReturn)
	require.Len(t, env.swd, 1)
	require.Equal(t, 4, env.swd[0].Output.NumElements(),
		"SWD+returnToSender output must have amounts/indexValues/lock/returnToSender")
}

// returnToSender on a plain sigLock output (lock at slot 2 is NOT
// sendWithDeadline) must be rejected at produce time.
func TestRTSProduceRejectNonSWDLock(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 1, rtsInitAmount)
	priv, addr := privKeys[0], addrs[0]
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	rtsBin, err := ledger.ReturnToSenderBytecode(rtsReturn)
	require.NoError(t, err)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	// plain sigLock output + returnToSender at slot 3
	bad := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(uint64(rtsSwdAmount)).WithLock(addr)
		o.PutConstraint(rtsBin, rtsConstraintIndex)
	})
	_, err = txb.ProduceOutput(bad)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(outs[0].Output.TokenBalance()-uint64(rtsSwdAmount)), addr))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "lock must be sendWithDeadline")
}

// A zero returnToSender amount must be rejected at produce time. The Go
// helper refuses zero, so we compile the bad bytecode directly.
func TestRTSProduceRejectZeroAmount(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 1, rtsInitAmount)
	priv, addr := privKeys[0], addrs[0]
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	_, _, zeroBin, err := ledger.L(base.MaxSlot).CompileExpression("returnToSender(z64/0)")
	require.NoError(t, err)

	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(priv))
	var targetID base.HolderID
	for i := range targetID {
		targetID[i] = 0x42
	}
	lock := &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: rtsAccept,
		CleanupSlots:    rtsCleanup,
	}

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	swd := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(uint64(rtsSwdAmount)).WithLock(lock)
		o.PutConstraint(zeroBin, rtsConstraintIndex)
	})
	_, err = txb.ProduceOutput(swd)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(outs[0].Output.TokenBalance()-uint64(rtsSwdAmount)), addr))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "amount must be positive")
}

// =============================================================================
// Consume side — master reclaim (branch a: noop)
// =============================================================================

// Master signs inside the reclaim window (Δ ≥ acceptanceSlots) and takes the
// funds back with NO receipt. returnToSender is a noop because the signer is
// the master.
func TestRTSMasterReclaimNoop(t *testing.T) {
	env := makeRTSEnv(t, 1, rtsReturn)
	sw := env.swd[0]

	txb := exhelp.New()
	_, err := txb.ConsumeOutput(sw.Output, sw.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0) // SWD lock master-reclaim: sigLock(master)
	// no returnToSender unlock params needed — branch (a) reads none.
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(rtsSwdAmount), env.addrMaster))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.createSlot+100, 1)) // Δ=100 ∈ [60,1100): master window
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privMaster)
	require.NoError(t, env.u.AddTransaction(txb.Bytes()),
		"master reclaim with no receipt must validate (returnToSender noop)")
}

// =============================================================================
// Consume side — target accept (branch b: receipt required)
// =============================================================================

// spendRTSTargetAccept consumes env.swd[0] as input 0 inside the acceptance
// window, signed by `signer`. It produces a receipt of `receiptBase` to
// `receiptHolder` carrying `literal` at slot 3, plus the target's remainder.
func spendRTSTargetAccept(t *testing.T, env *rtsEnv, receiptBase uint64, receiptHolder ledger.SigLock, literal byte, signer ed25519.PrivateKey) error {
	t.Helper()
	sw := env.swd[0]
	txb := exhelp.New()
	_, err := txb.ConsumeOutput(sw.Output, sw.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0) // SWD lock target-accept: sigLock(target)

	receiptIdx, err := txb.ProduceOutput(rtsReceipt(receiptBase, receiptHolder, literal))
	require.NoError(t, err)
	// returnToSender at slot 3 reads its 1-byte receipt-index unlock param.
	txb.PutUnlockParams(0, rtsConstraintIndex, []byte{byte(receiptIdx)})

	if remainder := uint64(rtsSwdAmount) - receiptBase; remainder > 0 {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(remainder), env.addrTarget))
		require.NoError(t, err)
	}
	txb.SetTimestamp(base.T(env.createSlot+10, 1)) // Δ=10 < 60: target window
	txb.ComputeInputCommitment()
	txb.SignED25519(signer)
	return env.u.AddTransaction(txb.Bytes())
}

// Happy path: target accepts, returns exactly `rtsReturn` to the master.
func TestRTSTargetAcceptHappy(t *testing.T) {
	env := makeRTSEnv(t, 1, rtsReturn)
	err := spendRTSTargetAccept(t, env, rtsReturn, env.addrMaster, 0, env.privTarget)
	require.NoError(t, err, "target accept with a valid return receipt must validate")
}

// Receipt base below the required amount → underpaid.
func TestRTSTargetAcceptUnderpaid(t *testing.T) {
	env := makeRTSEnv(t, 1, rtsReturn)
	err := spendRTSTargetAccept(t, env, rtsReturn-1, env.addrMaster, 0, env.privTarget)
	require.Error(t, err)
	require.Contains(t, err.Error(), "receipt underpaid")
}

// Receipt pays a third party, not the master → must pay master.
func TestRTSTargetAcceptWrongHolder(t *testing.T) {
	env := makeRTSEnv(t, 1, rtsReturn)
	err := spendRTSTargetAccept(t, env, rtsReturn, env.addrThird, 0, env.privTarget)
	require.Error(t, err)
	require.Contains(t, err.Error(), "receipt must pay master")
}

// Receipt's lock at slot 2 is not a sigLock → rejected.
func TestRTSTargetAcceptNonSigLockReceipt(t *testing.T) {
	env := makeRTSEnv(t, 1, rtsReturn)
	sw := env.swd[0]

	txb := exhelp.New()
	_, err := txb.ConsumeOutput(sw.Output, sw.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Receipt with an opaque (truthy, non-sigLock) lock `or(0x01)` at slot 2.
	badReceipt := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(uint64(rtsReturn))
		o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{env.masterID[:]}), ledger.ConstraintIndexIndexValues)
		o.PutConstraint([]byte{0x44, 0x42, 0x81, 0x01}, ledger.ConstraintIndexLock) // or(0x01)
		o.PutConstraint(easyfl.InlineDataBytecode([]byte{0}), rtsConstraintIndex)
	})
	receiptIdx, err := txb.ProduceOutput(badReceipt)
	require.NoError(t, err)
	txb.PutUnlockParams(0, rtsConstraintIndex, []byte{byte(receiptIdx)})

	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(uint64(rtsSwdAmount)-uint64(rtsReturn)), env.addrTarget))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.createSlot+10, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privTarget)
	err = env.u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "receipt lock must be sigLock")
}

// Fold attack: two SWD+returnToSender inputs both point at a single fat
// receipt. The receipt's 1-byte literal can equal only one input index, so
// the other input's anti-fold check fails.
func TestRTSFoldAttack(t *testing.T) {
	env := makeRTSEnv(t, 2, rtsReturn)

	txb := exhelp.New()
	_, err := txb.ConsumeOutput(env.swd[0].Output, env.swd[0].ID) // input 0
	require.NoError(t, err)
	_, err = txb.ConsumeOutput(env.swd[1].Output, env.swd[1].ID) // input 1
	require.NoError(t, err)
	txb.PutSignatureUnlock(0) // target signs; satisfies both SWD sigLock(target) locks
	txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0)

	// One fat receipt carrying literal == 0; both inputs reference it.
	receiptIdx, err := txb.ProduceOutput(rtsReceipt(uint64(rtsReturn), env.addrMaster, 0))
	require.NoError(t, err)
	txb.PutUnlockParams(0, rtsConstraintIndex, []byte{byte(receiptIdx)})
	txb.PutUnlockParams(1, rtsConstraintIndex, []byte{byte(receiptIdx)})

	// remainder back to target keeps amounts balanced so the tx fails on the
	// fold check, not on conservation.
	remainder := 2*uint64(rtsSwdAmount) - uint64(rtsReturn)
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(remainder), env.addrTarget))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.createSlot+10, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privTarget)
	err = env.u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "literal must equal input index")
}
