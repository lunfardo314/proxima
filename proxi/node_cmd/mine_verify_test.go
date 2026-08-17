package node_cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/stretchr/testify/require"
)

// Verification of streamed transits. The node relays mine transits it has NOT
// constraint-validated, so anything the miner steers on has to be re-derived
// from the raw bytes. These tests mine a genuine transit and then check that
// each rule actually rejects a transit violating it — a verifier that silently
// accepted would hand any attacker every miner's hashrate for free.

// The verifier needs a real EasyFL library. In the miner it comes from the node
// over the API; here the test-ledger singleton stands in for that, converted
// through the same JSON round trip the wallet uses. Difficulty is pinned low so
// the fixture can actually solve a transit.
func init() {
	ledger.InitWithTestingLedgerData(
		ledger.WithMineDifficulty(6, 4, 10, 2),
		ledger.WithMineTargetPace(4),
	)
}

// verifyFixture is a real predecessor plus everything needed to mine on it.
type verifyFixture struct {
	m    *miner
	pred *mineTip
}

// newVerifyFixture builds a wallet library and constants from the ledger
// singleton (the same route proxi takes from the node over the API) and
// synthesises a predecessor mine output to build transits on.
func newVerifyFixture(t *testing.T, predB uint64) *verifyFixture {
	t.Helper()

	l := ledger.L(base.MaxSlot)
	desc, err := easyfl.ReadLibraryFromJSON(easyfl.ToJSON(l.Library, true, false))
	require.NoError(t, err)
	lib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)
	consts := ledger.ConstantsFromLibrary(l.Library)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var seqID base.ChainID
	seqID[0] = 0xAB

	m := &miner{
		consts:        consts,
		lib:           lib,
		holderID:      base.HolderIDFromED25519PrivateKey(priv),
		tagAlongSeqID: seqID,
		fee:           consts.MineAmountBase / 200, // within the 1% cap
		workers:       1,
		wallet:        glb.WalletData{PrivateKey: priv},
	}

	// A predecessor mine output at a real (non-genesis) slot so the retarget is
	// live rather than held. The slot sits in the flat phase of the emission
	// schedule, so A throughout this fixture is exactly MineAmountBase.
	const predSlot = uint32(1000)
	pred := makeMineOutput(t, lib, consts, mineOutputParams{
		r:       consts.MineAmountBase * 1000,
		b:       predB,
		counter: 42,
		balance: 900_000_000_000,
		slot:    predSlot,
	})
	return &verifyFixture{m: m, pred: pred}
}

type mineOutputParams struct {
	r, b    uint64
	counter uint64
	cumInfl uint64
	balance uint64
	slot    uint32
}

// makeMineOutput assembles a mine chain output and wraps it as a tip at a
// synthetic output ID with the given slot.
func makeMineOutput(t *testing.T, lib *txbuildercore.Library[any], consts *txbuildercore.Constants, p mineOutputParams) *mineTip {
	t.Helper()

	lockBin, err := lib.NewMineLock(p.r, p.b)
	require.NoError(t, err)
	chainBin, err := lib.NewChainTransition(base.MineChainID, 0, 0, p.cumInfl, 0, p.counter, 0)
	require.NoError(t, err)

	ob := txbuildercore.NewOutputBuilder()
	ob.PutConstraint(txbuildercore.EncodeAmounts(p.balance, consts.MineAmountBase), txbuildercore.ConstraintIndexAmounts)
	ob.PutConstraint(lockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainBin, txbuildercore.ConstraintIndexChain)
	data := ob.Output().Bytes()

	// a synthetic predecessor output ID carrying the wanted slot
	var txid base.TransactionID
	copy(txid[:base.LedgerTimeByteLength], base.T(p.slot, 1).Bytes())
	txid[base.LedgerTimeByteLength] = 2
	txid[len(txid)-1] = 0x5A
	oid := base.MustNewOutputID(txid, 0)

	tip, err := parseMineTip(lib, oid, data, false)
	require.NoError(t, err)
	return tip
}

// mineOne builds and solves a transit on the fixture's predecessor.
func (f *verifyFixture) mineOne(t *testing.T) []byte {
	t.Helper()

	predSlot := f.pred.oid.Timestamp().Slot
	succSlot := predSlot + uint32(f.m.consts.MineMinPace)
	succB := f.m.consts.MineAdjustedB(f.pred.ml.B, predSlot, succSlot)
	tmpl := f.m.buildTemplate(f.pred, succSlot, succB)

	w := tmpl.newWorker()
	for n := uint64(1); n < 1<<24; n++ {
		if w.attempt(n) >= int(f.pred.ml.B) {
			return append([]byte(nil), w.full...)
		}
	}
	t.Fatal("no solution found; lower the fixture difficulty")
	return nil
}

// mineUnsolved builds a fully correct transit whose nonce does NOT satisfy the
// difficulty — every field is honest except the proof of work itself.
func (f *verifyFixture) mineUnsolved(t *testing.T) []byte {
	t.Helper()

	predSlot := f.pred.oid.Timestamp().Slot
	succSlot := predSlot + uint32(f.m.consts.MineMinPace)
	succB := f.m.consts.MineAdjustedB(f.pred.ml.B, predSlot, succSlot)
	tmpl := f.m.buildTemplate(f.pred, succSlot, succB)

	w := tmpl.newWorker()
	for n := uint64(1); n < 1<<24; n++ {
		if w.attempt(n) < int(f.pred.ml.B) {
			return append([]byte(nil), w.full...)
		}
	}
	t.Fatal("every nonce solved; raise the fixture difficulty")
	return nil
}

// a genuinely mined transit verifies, and yields the tip to build on next
func TestVerifyMineTransitAcceptsGenuine(t *testing.T) {
	f := newVerifyFixture(t, 6)
	txBytes := f.mineOne(t)

	succ, err := verifyMineTransit(f.m.lib, f.m.consts, f.pred, txBytes)
	require.NoError(t, err)
	require.EqualValues(t, f.pred.cc.TransitionCounter+1, succ.cc.TransitionCounter)
	require.EqualValues(t, f.pred.ml.R-f.m.consts.MineAmountBase, succ.ml.R)
	require.Equal(t, f.pred.balance, succ.balance)
}

// Insufficient proof of work is rejected. This is the check that matters most:
// the node does not verify PoW before streaming, so without it anyone could
// flood every miner with costless fabricated transits. The transit here is
// correct in every other respect, so only the PoW rule can reject it.
func TestVerifyMineTransitRejectsWeakPoW(t *testing.T) {
	f := newVerifyFixture(t, 6)
	txBytes := f.mineUnsolved(t)

	_, err := verifyMineTransit(f.m.lib, f.m.consts, f.pred, txBytes)
	require.ErrorContains(t, err, "insufficient proof of work")
}

// a transit that does not spend the predecessor we track is rejected, which is
// what defeats a forged transit imitating the mine-transit shape
func TestVerifyMineTransitRejectsWrongPredecessor(t *testing.T) {
	f := newVerifyFixture(t, 6)
	txBytes := f.mineOne(t)

	other := makeMineOutput(t, f.m.lib, f.m.consts, mineOutputParams{
		r: f.m.consts.MineAmountBase * 1000, b: 6,
		counter: 42, balance: 900_000_000_000, slot: 1000,
	})
	other.oid = base.MustNewOutputID(randTxID(0x11, 0x22), 0)

	_, err := verifyMineTransit(f.m.lib, f.m.consts, other, txBytes)
	require.ErrorContains(t, err, "is not the predecessor")
}

// a transit built against different predecessor BYTES is rejected even when it
// names the right predecessor ID
func TestVerifyMineTransitRejectsWrongPredecessorBytes(t *testing.T) {
	f := newVerifyFixture(t, 6)
	txBytes := f.mineOne(t)

	// same output ID, different content
	tampered := *f.pred
	other := makeMineOutput(t, f.m.lib, f.m.consts, mineOutputParams{
		r: f.m.consts.MineAmountBase * 999, b: 6,
		counter: 42, balance: 900_000_000_000, slot: 1000,
	})
	tampered.data = other.data

	_, err := verifyMineTransit(f.m.lib, f.m.consts, &tampered, txBytes)
	require.ErrorContains(t, err, "input commitment")
}

// the successor must carry the difficulty the retarget dictates, so a miner
// cannot ease the chain for itself
func TestVerifyMineTransitRejectsWrongDifficulty(t *testing.T) {
	f := newVerifyFixture(t, 6)

	// build a transit whose successor carries a difficulty of its own choosing
	predSlot := f.pred.oid.Timestamp().Slot
	succSlot := predSlot + uint32(f.m.consts.MineMinPace)
	honest := f.m.consts.MineAdjustedB(f.pred.ml.B, predSlot, succSlot)
	tmpl := f.m.buildTemplate(f.pred, succSlot, honest-1) // one bit easier

	w := tmpl.newWorker()
	var txBytes []byte
	for n := uint64(1); n < 1<<24; n++ {
		if w.attempt(n) >= int(f.pred.ml.B) {
			txBytes = append([]byte(nil), w.full...)
			break
		}
	}
	require.NotNil(t, txBytes)

	_, err := verifyMineTransit(f.m.lib, f.m.consts, f.pred, txBytes)
	require.ErrorContains(t, err, "difficulty")
}

// stamping closer than the minimum pace is rejected
func TestVerifyMineTransitRejectsPaceBelowMinimum(t *testing.T) {
	f := newVerifyFixture(t, 6)

	predSlot := f.pred.oid.Timestamp().Slot
	tooSoon := predSlot + uint32(f.m.consts.MineMinPace) - 1
	succB := f.m.consts.MineAdjustedB(f.pred.ml.B, predSlot, tooSoon)
	tmpl := f.m.buildTemplate(f.pred, tooSoon, succB)

	w := tmpl.newWorker()
	var txBytes []byte
	for n := uint64(1); n < 1<<24; n++ {
		if w.attempt(n) >= int(f.pred.ml.B) {
			txBytes = append([]byte(nil), w.full...)
			break
		}
	}
	require.NotNil(t, txBytes)

	_, err := verifyMineTransit(f.m.lib, f.m.consts, f.pred, txBytes)
	require.ErrorContains(t, err, "pace")
}

// any bit flipped after signing breaks either the PoW or the signature
func TestVerifyMineTransitRejectsTamperedBytes(t *testing.T) {
	f := newVerifyFixture(t, 6)
	txBytes := f.mineOne(t)

	for _, pos := range []int{0, len(txBytes) / 3, len(txBytes) / 2, len(txBytes) - 1} {
		tampered := append([]byte(nil), txBytes...)
		tampered[pos] ^= 0x01
		_, err := verifyMineTransit(f.m.lib, f.m.consts, f.pred, tampered)
		require.Errorf(t, err, "flipping a bit at %d must not verify", pos)
	}
}

// a transit on an exhausted chain is rejected
func TestVerifyMineTransitRejectsExhaustedChain(t *testing.T) {
	f := newVerifyFixture(t, 6)
	txBytes := f.mineOne(t)

	drained := *f.pred
	drainedML := *f.pred.ml
	drainedML.R = f.m.consts.MineAmountBase - 1
	drained.ml = &drainedML

	_, err := verifyMineTransit(f.m.lib, f.m.consts, &drained, txBytes)
	require.ErrorContains(t, err, "exhausted")
}

// garbage is rejected without panicking
func TestVerifyMineTransitRejectsGarbage(t *testing.T) {
	f := newVerifyFixture(t, 6)

	for _, b := range [][]byte{nil, {}, {0x00}, []byte("not a transaction at all")} {
		_, err := verifyMineTransit(f.m.lib, f.m.consts, f.pred, b)
		require.Error(t, err)
	}
}
