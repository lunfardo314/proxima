package tests

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// mineAmountEasyFL evaluates the on-chain emission schedule (_mineAmountAtSlot
// in def/lock_mine.easyfl) at the given slot.
func mineAmountEasyFL(t *testing.T, slot uint32) uint64 {
	t.Helper()
	res, err := ledger.L(0).EvalFromSource(nil, fmt.Sprintf("_mineAmountAtSlot(u32/%d)", slot))
	require.NoError(t, err)
	v, err := easyfl_util.Uint64FromBytes(res)
	require.NoError(t, err)
	return v
}

// The wallet mirrors the emission schedule in Go (Constants.MineAmountAtSlot) so
// a miner can size a transit without evaluating the constraint. The two must
// agree exactly: if they drift, every transit the miner builds past the drift
// point is rejected by the very lock it is trying to satisfy. Checked either
// side of the ramp start, where a disagreement would first appear.
func TestMineAmountScheduleMatchesWalletMirror(t *testing.T) {
	c := ledger.L(0).Constants
	ramp := c.MineRampStartSlot

	slots := []uint32{0, 1, ramp - 1, ramp, ramp + 1, ramp + 1000, ramp + 1_000_000}
	for _, s := range slots {
		require.EqualValues(t, c.MineAmountAtSlot(s), mineAmountEasyFL(t, s), "slot %d", s)
	}
}

// The schedule itself: flat at the base amount up to and including the ramp
// start, then growing by exactly the per-slot slope. The boundary is inclusive
// on the flat side, which is what makes the ramp start the first slot that pays
// more than the base rather than the last that pays it.
func TestMineAmountScheduleShape(t *testing.T) {
	c := ledger.L(0).Constants
	ramp := c.MineRampStartSlot

	require.EqualValues(t, c.MineAmountBase, mineAmountEasyFL(t, 0))
	require.EqualValues(t, c.MineAmountBase, mineAmountEasyFL(t, ramp-1))
	require.EqualValues(t, c.MineAmountBase, mineAmountEasyFL(t, ramp))
	require.EqualValues(t, c.MineAmountBase+c.MineAmountPerSlot, mineAmountEasyFL(t, ramp+1))

	// linear thereafter, with no ceiling: A grows until R can no longer cover it
	for _, d := range []uint32{2, 100, 50_000} {
		require.EqualValues(t, c.MineAmountBase+uint64(d)*c.MineAmountPerSlot, mineAmountEasyFL(t, ramp+d))
	}
}

// The realized pace the emission schedule is sized for, as a fraction: 4.5
// slots per transit. The retarget aims at MineTargetPace, but the pace-relieved
// difficulty eases one bit per extra slot of gap, so the winning gap settles
// above the target — the testnet mean is ~4.6, measured, not derived. The
// schedule is sized for what is realized, so the milestones below divide by this
// rather than by MineTargetPace.
const (
	realizedPaceNum = 9
	realizedPaceDen = 2
)

// The three schedule constants are not free parameters: they are chosen so the
// flat phase alone carries mined supply to the point where the genesis capital
// can no longer commit healthy branches alone, and so the whole budget is
// exhausted about fourteen months in. This pins both, so a future edit to any of
// the constants that breaks the design fails here rather than at genesis.
//
// Emission is A(slot)/pace motes per slot, so cumulative emission through slot S
// is the running sum of A divided by the pace.
func TestMineAmountScheduleMilestones(t *testing.T) {
	c := ledger.L(0).Constants
	ramp := uint64(c.MineRampStartSlot)

	// cumulative motes emitted through slot s inclusive
	cumulative := func(s uint64) uint64 {
		if s <= ramp {
			return (s + 1) * c.MineAmountBase * realizedPaceDen / realizedPaceNum
		}
		n := s - ramp // slots into the ramp
		// sum of the slope over 1..n
		return ((s+1)*c.MineAmountBase + n*(n+1)/2*c.MineAmountPerSlot) * realizedPaceDen / realizedPaceNum
	}

	// The genesis capital can commit healthy branches alone while it holds more
	// than 7/12 of supply, i.e. until mined M satisfies I/(I+M) = 7/12, so
	// M = 5I/7. The flat phase must deliver that.
	wantAtRamp := 5 * c.InitialSupply / 7
	gotAtRamp := cumulative(ramp)
	require.InEpsilon(t, wantAtRamp, gotAtRamp, 0.01,
		"the flat phase must end where the genesis capital loses the ability to commit alone")

	// The whole mintable budget is exhausted at roughly 429 days. R_init is taken
	// from the ceiling identity T = I + R_init rather than from MineRemainingInit,
	// which tests deliberately shrink to a handful of transits.
	rInit := c.TargetBaseSupply - c.InitialSupply
	slotsPerDay := uint64(86400) * 1e9 / uint64(c.SlotDuration().Nanoseconds())
	var endSlot uint64
	for endSlot = ramp; cumulative(endSlot) < rInit; endSlot += 1000 {
	}
	days := endSlot / slotsPerDay
	require.Greater(t, days, uint64(400), "emission ends too early")
	require.Less(t, days, uint64(460), "emission runs too long")

	// The flat phase is ~46 days, so the reward is constant for exactly as long
	// as one party can still stop the network.
	require.InDelta(t, 46, ramp/slotsPerDay, 1)
}

// buildMineTransit assembles a transit on the genesis mine output stamped in
// succSlot, minting the given amount, entirely through the wallet-side
// (txbuildercore) helpers — the same path proxi/node_cmd/mine.go takes. `a` is
// passed in rather than derived so a test can build a deliberately wrong one.
//
// The predecessor is the genesis mine output at slot 0, so the gap M is huge:
// the retarget holds B (predecessor is genesis) and the pace relief drops the
// required K to the floor, which is what makes a far-future stamp minable here.
func buildMineTransit(t *testing.T, u *utxodb.UTXODB, tlib *txbuildercore.Library[any],
	minerPriv ed25519.PrivateKey, succSlot uint32, a uint64) []byte {
	t.Helper()
	minerHolderID := base.HolderIDFromED25519PrivateKey(minerPriv)
	fee := a / 200

	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	predWithID, err := md.Parse()
	require.NoError(t, err)
	predBytes := predWithID.Output.Bytes()
	predOID := predWithID.ID
	predOut, err := txbuildercore.OutputFromBytes(predBytes)
	require.NoError(t, err)
	predML, err := tlib.ParseMineLock(predOut.MustConstraintAt(txbuildercore.ConstraintIndexLock))
	require.NoError(t, err)
	predCC, err := tlib.ParseChainConstraint(predOut.MustConstraintAt(txbuildercore.ConstraintIndexChain))
	require.NoError(t, err)
	predBalance, err := txbuildercore.DecodeTokenBalance(predBytes)
	require.NoError(t, err)
	predSlot := predOID.Timestamp().Slot

	k := int(ledger.L(0).MineRequiredK(predML.B, uint64(succSlot-predSlot)))
	succB := ledger.L(0).MineAdjustedB(predML.B, predSlot, succSlot)

	succLockBin, err := tlib.NewMineLock(predML.R-a, succB)
	require.NoError(t, err)
	succChainBin, err := tlib.NewChainTransition(base.MineChainID, 0, predCC.OriginSlot,
		predCC.CumulativeChainInflation+a, 0, predCC.TransitionCounter+1, 0)
	require.NoError(t, err)
	sb := txbuildercore.NewOutputBuilder()
	sb.PutConstraint(txbuildercore.EncodeAmounts(predBalance, a), txbuildercore.ConstraintIndexAmounts)
	sb.PutConstraint(succLockBin, txbuildercore.ConstraintIndexLock)
	sb.PutConstraint(succChainBin, txbuildercore.ConstraintIndexChain)

	payoutOut, err := txbuildercore.NewSigLockOutput(tlib, a-fee, minerHolderID)
	require.NoError(t, err)
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(tlib, fee, *u.GenesisChainID(), minerHolderID)
	require.NoError(t, err)

	txb := txbuildercore.New(0)
	predIdx := txb.ConsumeOutput(predBytes, predOID)
	txb.ProduceOutput(sb.Output().Bytes())
	txb.ProduceOutput(payoutOut.Bytes())
	txb.ProduceOutput(tagAlongOut.Bytes())
	txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))
	txb.SetTimestamp(base.T(succSlot, 1))
	txb.ComputeInputCommitment()

	var nonce [8]byte
	for n := uint64(0); ; n++ {
		binary.BigEndian.PutUint64(nonce[:], n)
		txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexLock, nonce[:])
		txb.SignED25519(minerPriv)
		if trailingZeroBits(blake2b.Sum256(txb.Bytes())) >= k {
			return txb.Bytes()
		}
	}
}

// A transit stamped in the ramp phase, sized by the wallet-side mirror, is
// accepted by the real validator and pays the miner the ramped amount. This is
// the end-to-end statement that `proxi node mine` still mines correctly after
// the schedule stops being flat.
func TestMineRampTransitAccepted(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	tlib := walletLibFromGlobal(t)
	c := ledger.L(0).Constants

	succSlot := c.MineRampStartSlot + 20312
	a := c.MineAmountAtSlot(succSlot)
	require.Greater(t, a, c.MineAmountBase, "the chosen slot must be inside the ramp")

	require.NoError(t, u.AddTransaction(buildMineTransit(t, u, tlib, minerPriv, succSlot, a)))
	require.EqualValues(t, a, u.Balance(ledger.SigLockFromED25519PrivateKey(minerPriv)))
}

// A miner that ignored the ramp and minted the flat base amount in a ramp-phase
// slot is rejected. Without this the schedule would be advisory: the constraint
// has to be what enforces it.
func TestMineRampTransitWithFlatAmountRejected(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	tlib := walletLibFromGlobal(t)
	c := ledger.L(0).Constants

	succSlot := c.MineRampStartSlot + 20312
	txBytes := buildMineTransit(t, u, tlib, minerPriv, succSlot, c.MineAmountBase)
	require.ErrorContains(t, u.AddTransaction(txBytes), "mine successor inflation must equal A")
}
