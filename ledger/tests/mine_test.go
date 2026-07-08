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
	pace         uint32          // pace M = succ.slot - pred.slot (0 -> P, the minimum)
}

// buildMineTransition consumes the current mine chain output and builds a
// transition producing the successor (index 0), the sig-locked payout (index 1)
// and the tag-along (index 2). With opts.mine it searches a nonce so the whole
// signed tx hashes to >= K(M) trailing zero bits.
func buildMineTransition(t *testing.T, u *utxodb.UTXODB, minerPriv ed25519.PrivateKey, opts mineTxOpts) []byte {
	t.Helper()
	lib := ledger.L(0)
	a := mineConst(t, "constMineAmount")
	b := mineConst(t, "constMineBaseDifficulty")
	e := mineConst(t, "constMineFloorDifficulty")
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

	// successor mine output (index 0): balance unchanged, inflation A, R-=A,
	// B carried, ring rolled (s1'=predSlot, s2'=old s1, s3'=old s2)
	succLock := ledger.NewMineLock(predLock.R-a, predLock.B, predSlot, predLock.S1, predLock.S2)
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
	// pace M (default P): successor at slot predSlot+M
	m := opts.pace
	if m == 0 {
		m = p
	}
	txb.SetTimestamp(base.T(predSlot+m, 1))
	txb.ComputeInputCommitment()

	// difficulty K(M) = max(B - (M - P), E), mirroring _mineK. For M <= P we
	// use B (M < P is rejected on pace regardless of PoW).
	var k int
	switch {
	case m <= p:
		k = int(b)
	case b-e <= uint64(m-p):
		k = int(e)
	default:
		k = int(b - uint64(m-p))
	}
	var nonce [8]byte
	for n := uint64(0); ; n++ {
		binary.BigEndian.PutUint64(nonce[:], n)
		// nonce lives in the open lock's unlock params (ignored by mineLock,
		// part of the essence so it perturbs txid -> signature -> tx hash)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, nonce[:])
		txb.SignED25519(minerPriv)
		txBytes := txb.Bytes()
		if !opts.mine || trailingZeroBits(blake2b.Sum256(txBytes)) >= k {
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

// TestMineDifficultyDropsWithPace accepts a transition mined at a larger pace
// against a lower difficulty. With B=8, E=4, P=2, K(M=6) = max(8-(6-2),4) = 4,
// so a hash with only 4 trailing zero bits is sufficient — fewer than the B=8
// bits required at the minimum pace. Demonstrates the K(M) curve.
func TestMineDifficultyDropsWithPace(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	txBytes := buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true, pace: 6})
	require.NoError(t, u.AddTransaction(txBytes))
}

// TestMineChainExhausted rejects a transition once the remaining-mintable
// counter is below A. The test ledger seeds R_init == A (one mint): after the
// first transit R == 0 < A, so the mine chain has no valid successor.
func TestMineChainExhausted(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	a := mineConst(t, "constMineAmount")
	// first transit: R A -> 0 (valid)
	require.NoError(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true})))
	// second transit: predecessor R == 0 < A -> exhausted, no valid successor
	require.ErrorContains(t, u.AddTransaction(buildMineTransition(t, u, minerPriv, mineTxOpts{fee: a / 200, mine: true})), "mine chain is exhausted")
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
		o.WithAmounts(int64(100_000_000)).WithLock(ledger.NewMineLock(rInit, b0, 0, 0, 0))
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
