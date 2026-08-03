// Base tests for the fair-launch mine chain (see claude/fairlaunch.md).
//
// The mine chain is a single genesis chained UTXO (index 3) whose open
// `mineLock` mints a fixed amount A per transit against a proof-of-signing-work.
// The test ledger is initialised with a low difficulty (WithMineDifficulty in
// init.go) so a valid nonce is found in a handful of attempts.
package tests

import (
	"crypto/ed25519"
	"encoding/binary"
	mbits "math/bits"
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// trailingZeroBits counts trailing zero bits of the 32-byte hash — the same
// definition the mineLock PoW check enforces (low-K-bits-zero, K < 64).
func trailingZeroBits(h [32]byte) int {
	n := 0
	for i := len(h) - 1; i >= 0; i-- {
		if h[i] == 0 {
			n += 8
			continue
		}
		return n + mbits.TrailingZeros8(h[i])
	}
	return n
}

// mineConst evaluates a named u64 mine constant from the current library.
func mineConst(t *testing.T, name string) uint64 {
	t.Helper()
	res, err := ledger.L(0).EvalFromSource(nil, name)
	require.NoError(t, err)
	v, err := easyfl_util.Uint64FromBytes(res)
	require.NoError(t, err)
	return v
}

// mineTxOpts lets a test deviate from a valid mine transition to exercise a
// specific rejection path.
type mineTxOpts struct {
	fee          uint64          // tag-along fee T (payout A' = A - T)
	payoutHolder *ledger.SigLock // override the payout target (default: the signer)
	mine         bool            // search for a valid PoW nonce (false: leave nonce 0)
	mineExactK   *int            // search a nonce with EXACTLY this many trailing zero bits (overrides mine)
	pace         uint32          // pace M = succ.slot - pred.slot (0 -> P, the minimum)
	succB        *uint64         // override the successor's difficulty (default: the retarget result)
}

// buildMineTransition consumes the current mine chain output and builds a
// transition producing the successor (index 0), the sig-locked payout (index 1)
// and the tag-along (index 2). With opts.mine it searches a nonce so the whole
// signed tx hashes to >= K trailing zero bits, where K = max(B - (M - P), E) is
// the pace-relieved required difficulty (full B at the minimum pace, one bit
// easier per extra slot). opts.mineExactK instead pins the PoW to an exact bit
// count, so a test can present a transit just below the required K.
func buildMineTransition(t *testing.T, u *utxodb.UTXODB, minerPriv ed25519.PrivateKey, opts mineTxOpts) []byte {
	t.Helper()
	lib := ledger.L(0)
	a := mineConst(t, "constMineAmount")
	p := uint32(mineConst(t, "constMineMinPace"))

	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)

	lockBin, err := mineIn.Output.At(int(ledger.ConstraintIndexLock))
	require.NoError(t, err)
	predLock, err := ledger.MineLockFromBytesWithLib(lockBin, lib)
	require.NoError(t, err)

	cc := mineIn.Output.ChainConstraint()
	require.NotNil(t, cc)
	predSlot := mineIn.ID.Timestamp().Slot
	predBalance := mineIn.Output.TokenBalance()

	minerLock := ledger.SigLockFromED25519PrivateKey(minerPriv)
	payoutLock := minerLock
	if opts.payoutHolder != nil {
		payoutLock = *opts.payoutHolder
	}
	payout := a - opts.fee

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(mineIn.Output, mineIn.ID)
	require.NoError(t, err)

	// pace M (default P): successor at slot predSlot+M
	m := opts.pace
	if m == 0 {
		m = p
	}
	succSlot := predSlot + m

	// successor mine output (index 0): balance unchanged, inflation A, R-=A,
	// B retargeted from the single last gap (succSlot - predSlot)
	succB := lib.MineAdjustedB(predLock.B, predSlot, succSlot)
	if opts.succB != nil {
		succB = *opts.succB // deliberately wrong difficulty, to test the rule
	}
	succLock := ledger.NewMineLock(predLock.R-a, succB)
	succChain := ledger.NewChainConstraint(base.MineChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation+a, 0, cc.TransitionCounter+1, 0)
	succ := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(predBalance), int64(a)).WithLock(succLock)
		o.PutConstraint(succChain.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	require.EqualValues(t, 0, succIdx)

	// payout (index 1): sig-locked to the signer, amount A' = A - T
	payoutOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(payout).WithLock(payoutLock)
	})
	_, err = txb.ProduceOutput(payoutOut)
	require.NoError(t, err)

	// tag-along (index 2): fee T to the bootstrap sequencer
	tagAlong := ledger.NewTagAlongOutput(opts.fee, *u.GenesisChainID(), base.HolderID(minerLock))
	_, err = txb.ProduceOutput(tagAlong)
	require.NoError(t, err)

	// chain unlock: point predecessor to successor index 0
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.SetTimestamp(base.T(succSlot, 1))
	txb.ComputeInputCommitment()

	// pace-relieved required difficulty K = max(B - (M - P), E)
	k := int(lib.MineRequiredK(predLock.B, uint64(m)))
	var nonce [8]byte
	for n := uint64(0); ; n++ {
		binary.BigEndian.PutUint64(nonce[:], n)
		// nonce lives in the open lock's unlock params (ignored by mineLock,
		// part of the essence so it perturbs txid -> signature -> tx hash)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, nonce[:])
		txb.SignED25519(minerPriv)
		txBytes := txb.Bytes()
		z := trailingZeroBits(blake2b.Sum256(txBytes))
		switch {
		case opts.mineExactK != nil:
			if z == *opts.mineExactK {
				return txBytes
			}
		case !opts.mine || z >= k:
			return txBytes
		}
	}
}

// TestMineHappyPath mines one valid transition and checks its effects.
func TestMineHappyPath(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)

	a := mineConst(t, "constMineAmount")
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	before, err := md.Parse()
	require.NoError(t, err)
	beforeLock, err := ledger.MineLockFromBytesWithLib(mustLockBin(t, before), ledger.L(0))
	require.NoError(t, err)

	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true})
	require.NoError(t, u.AddTransaction(txBytes))

	// mine chain advanced: R decreased by A, balance unchanged
	md, err = u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	after, err := md.Parse()
	require.NoError(t, err)
	afterLock, err := ledger.MineLockFromBytesWithLib(mustLockBin(t, after), ledger.L(0))
	require.NoError(t, err)
	require.EqualValues(t, beforeLock.R-a, afterLock.R)
	require.EqualValues(t, before.Output.TokenBalance(), after.Output.TokenBalance())

	// The miner controls the whole minted A: the payout (A - T) is sig-locked to
	// it and the tag-along (T) is reclaimable by it (the sender).
	minerLock := ledger.SigLockFromED25519PrivateKey(minerPriv)
	require.EqualValues(t, a, u.Balance(minerLock))
}

// TestMineTransactionRecognized checks the structural recognizer used by the
// spam-filter exemption: a mine transition is flagged, an ordinary send is not.
func TestMineTransactionRecognized(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, minerAddr := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")

	mineBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true})
	mineTx, err := transaction.Parse(mineBytes)
	require.NoError(t, err)
	require.True(t, mineTx.IsMiningTransaction())

	// an ordinary transfer (not consuming the mine chain) must not be recognized
	_, _, otherAddr := u.GenerateAddress(8)
	require.NoError(t, u.TokensFromFaucet(minerAddr, 50_000_000))
	sendTx, err := u.TransferTokensReturnTx(minerPriv, otherAddr, 20_000_000)
	require.NoError(t, err)
	require.False(t, sendTx.IsMiningTransaction())
}

// TestMineInsufficientPoW rejects a structurally valid tx whose hash does not
// meet the difficulty (nonce not searched).
func TestMineInsufficientPoW(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	// mine=false: keep nonce 0; overwhelmingly likely < 8 trailing zero bits
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: false})
	err := u.AddTransaction(txBytes)
	require.Error(t, err)
}

// TestMineFeeCapExceeded rejects a transition whose tag-along fee exceeds 1% of A.
func TestMineFeeCapExceeded(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a/50, mine: true}) // 2% > 1%
	err := u.AddTransaction(txBytes)
	require.Error(t, err)
}

// TestMinePayoutWrongHolder rejects a transition paying the reward to someone
// other than the transaction signer — the rule that makes mining
// non-outsourceable.
func TestMinePayoutWrongHolder(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	otherLock := ledger.SigLockRandom()
	a := mineConst(t, "constMineAmount")
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, payoutHolder: &otherLock, mine: true})
	err := u.AddTransaction(txBytes)
	require.Error(t, err)
}

// TestMinePaceBelowMinimum rejects a transition whose pace M < P. The test
// ledger uses P=2, so M=1 (successor one slot after the predecessor) is below
// the minimum. PoW is satisfied (mine:true) to isolate the pace rule.
func TestMinePaceBelowMinimum(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, pace: 1})
	require.ErrorContains(t, u.AddTransaction(txBytes), "mine pace below minimum")
}

// TestMinePaceRequiresFullBAtMinimum: at the minimum pace M = P the required
// difficulty is the full B (no relief). A transit whose PoW has exactly B-1
// trailing zero bits is rejected. mineExactK pins the PoW so the check is
// deterministic rather than relying on a search overshoot.
func TestMinePaceRequiresFullBAtMinimum(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	b0 := int(mineConst(t, "constMineBaseDifficulty"))
	weak := b0 - 1 // one bit short of the full B required at the minimum pace
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mineExactK: &weak})
	require.ErrorContains(t, u.AddTransaction(txBytes), "insufficient mine proof of work")
}

// TestMinePaceRelievesRequiredK: at a pace above the minimum the required K drops
// one bit per extra slot below B. At the test target pace 4 (P=2) the relief is 2
// bits, so K = B-2: a PoW of exactly B-2 bits is accepted while B-3 is rejected —
// the relief is real and its boundary is enforced. Fresh ledgers so each case
// mines against the same genesis predecessor (which holds B at the seed).
func TestMinePaceRelievesRequiredK(t *testing.T) {
	a := mineConst(t, "constMineAmount")
	b0 := int(mineConst(t, "constMineBaseDifficulty"))
	p := int(mineConst(t, "constMineMinPace"))
	const pace = uint32(4)
	required := b0 - (int(pace) - p) // K = B - (M - P)

	// exactly the relieved K is accepted
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	ok := required
	require.NoError(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, pace: pace, mineExactK: &ok})))

	// one bit below the relieved K is rejected
	u2 := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv2, _, _ := u2.GenerateAddress(7)
	weak := required - 1
	require.ErrorContains(t, u2.AddTransaction(buildMineTransition(t, u2, minerPriv2, mineTxOpts{fee: a / 200, pace: pace, mineExactK: &weak})),
		"insufficient mine proof of work")
}

// mineNTransits settles n valid transits at the given pace and returns the
// resulting mine chain lock, so a test can assert the retargeted difficulty.
func mineNTransits(t *testing.T, u *utxodb.UTXODB, minerPriv ed25519.PrivateKey, n int, pace uint32) *ledger.MineLock {
	t.Helper()
	a := mineConst(t, "constMineAmount")
	for i := 0; i < n; i++ {
		require.NoError(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, pace: pace})))
	}
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	out, err := md.Parse()
	require.NoError(t, err)
	lock, err := ledger.MineLockFromBytesWithLib(mustLockBin(t, out), ledger.L(0))
	require.NoError(t, err)
	return lock
}

// TestMineRetargetHoldsFirstTransit: the genesis mine output is at slot 0, so
// the first transit's gap (txSlot - 0) is meaningless. The retarget must hold B
// on that transit rather than crater it to the floor. Pace 5 would ease once the
// predecessor is a real transit; on the first it must not.
func TestMineRetargetHoldsFirstTransit(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	b0 := mineConst(t, "constMineBaseDifficulty")
	lock := mineNTransits(t, u, minerPriv, 1, 5)
	require.EqualValues(t, b0, lock.B)
}

// TestMineRetargetWrongSuccessorDifficultyRejected pins the rule itself: on the
// first transit the successor must carry exactly B (the gap is against genesis
// slot 0, so B holds), and B+1 is rejected.
func TestMineRetargetWrongSuccessorDifficultyRejected(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	wrong := mineConst(t, "constMineBaseDifficulty") + 1
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, succB: &wrong})
	require.ErrorContains(t, u.AddTransaction(txBytes), "wrong difficulty on mine successor")
}

// TestMineRetargetHardensWhenFast: with target pace 4, a gap of 2 (< 4) means
// mining is faster than target, so the retarget hardens B by one bit. The first
// transit holds (genesis predecessor), the second one hardens: 8 -> 9.
func TestMineRetargetHardensWhenFast(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	b0 := mineConst(t, "constMineBaseDifficulty")
	lock := mineNTransits(t, u, minerPriv, 2, 2)
	require.EqualValues(t, b0+1, lock.B)
}

// TestMineRetargetEasesWhenSlow: a gap of 5 (> 4) means mining is slower than
// target, so the second transit eases B: 8 -> 7.
func TestMineRetargetEasesWhenSlow(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	b0 := mineConst(t, "constMineBaseDifficulty")
	lock := mineNTransits(t, u, minerPriv, 2, 5)
	require.EqualValues(t, b0-1, lock.B)
}

// TestMineRetargetHoldsAtTarget: a gap of exactly the target pace (4) holds B.
func TestMineRetargetHoldsAtTarget(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	b0 := mineConst(t, "constMineBaseDifficulty")
	lock := mineNTransits(t, u, minerPriv, 4, 4)
	require.EqualValues(t, b0, lock.B)
}

// TestMineRetargetClampsAtCeiling: sustained fast mining (gap 2) hardens one bit
// per transit and then stops at C. From B0=8 with C=10: hold 8, then 9, 10, 10.
func TestMineRetargetClampsAtCeiling(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	c := mineConst(t, "constMineMaxDifficulty")
	lock := mineNTransits(t, u, minerPriv, 4, 2)
	require.EqualValues(t, c, lock.B)
}

// TestMineRetargetClampsAtFloor: sustained slow mining (gap 5) eases one bit per
// transit and then stops at E. From B0=8 with E=6: hold 8, then 7, 6, 6.
func TestMineRetargetClampsAtFloor(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	e := mineConst(t, "constMineFloorDifficulty")
	lock := mineNTransits(t, u, minerPriv, 4, 5)
	require.EqualValues(t, e, lock.B)
}

// TestMineHugePaceLandsAtFloorK: a transit at a pace far above the target is
// required to meet only the floor difficulty (K relieved down to E) — the
// liveness guarantee, since any hashrate can solve the floor however high B sits.
// The successor then eases a SINGLE bit (a slow gap), not a snap-down to the
// solved K. First transit holds B at the seed (genesis gate); the second, at a
// huge gap, mines at the floor and eases B to B0-1.
func TestMineHugePaceLandsAtFloorK(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	b0 := mineConst(t, "constMineBaseDifficulty")
	// first transit at the target pace: genesis gate holds B at the seed
	require.NoError(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, pace: 4})))
	// second transit at a huge gap: required K relieved to the floor, mined and accepted
	require.NoError(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, pace: 100})))
	// the successor eased one bit from the seed (a slow gap), with no snap-down
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	out, err := md.Parse()
	require.NoError(t, err)
	lock, err := ledger.MineLockFromBytesWithLib(mustLockBin(t, out), ledger.L(0))
	require.NoError(t, err)
	require.EqualValues(t, b0-1, lock.B)
}

// TestMineChainExhausted rejects a transition once the remaining-mintable
// counter is below A. The test ledger seeds R_init == 8A, so the 9th transit has
// no valid successor.
func TestMineChainExhausted(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	rInit := mineConst(t, "constMineRemainingInit")
	n := int(rInit / a)
	// mint the whole R (pace 4 keeps the difficulty in the dead band throughout)
	lock := mineNTransits(t, u, minerPriv, n, 4)
	require.EqualValues(t, 0, lock.R)
	// next transit: predecessor R == 0 < A -> exhausted, no valid successor
	require.ErrorContains(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, pace: 4})), "mine chain is exhausted")
}

// TestMineLockOnlyOnMineChain rejects a mineLock placed on any chain other than
// the genesis mine chain. A fresh chain-origin output carrying mineLock has a
// chain ID != MineChainID, so mineLock's produced arm (_mineProduced) rejects
// it. This is what keeps the mining policy bound to the single mine chain.
func TestMineLockOnlyOnMineChain(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, minerAddr := u.GenerateAddress(7)
	require.NoError(t, u.TokensFromFaucet(minerAddr, 300_000_000))
	b0 := mineConst(t, "constMineBaseDifficulty")
	rInit := mineConst(t, "constMineRemainingInit")

	outs, _ := u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(minerAddr, 200_000_000)
	require.True(t, len(outs) > 0)

	txb := exhelp.New()
	idx, err := txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(idx)

	ts := outs[0].ID.Timestamp().AddSlots(1)
	// chain-origin output locked by mineLock but NOT on the mine chain
	badOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(100_000_000)).WithLock(ledger.NewMineLock(rInit, b0))
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(badOut)
	require.NoError(t, err)
	// remainder back to the miner so the tx balances
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - 100_000_000).WithLock(minerAddr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(minerPriv)
	require.ErrorContains(t, u.AddTransaction(txb.Bytes()), "mineLock is only valid on the mine chain")
}

func mustLockBin(t *testing.T, o *ledger.OutputWithID) []byte {
	t.Helper()
	bin, err := o.Output.At(int(ledger.ConstraintIndexLock))
	require.NoError(t, err)
	return bin
}
