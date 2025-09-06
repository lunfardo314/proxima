package tests

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// Validating and making sense of inflation-related constants

func TestScaleBytesAsBigInt(t *testing.T) {
	r := ledger.RandomFromSeed([]byte("abc"), 3)
	require.True(t, r < 3)
	h := blake2b.Sum256([]byte("abc"))
	r = ledger.RandomFromSeed(h[:], 1337)
	require.True(t, r < 1337)

	for i := 0; i < 1000; i++ {
		h = blake2b.Sum256([]byte(fmt.Sprintf("%d%d", i, i)))
		scale := rand.Int31n(500)
		if scale <= 0 {
			scale = 1 - scale
		}
		r = ledger.RandomFromSeed(h[:], uint64(scale))
		require.True(t, r < uint64(scale))
	}
}

func TestInflationFun(t *testing.T) {
	// TODO --
	//t.Run("chain inflation", func(t *testing.T) {
	//	runTest := func(tsIn, tsOut base.LedgerTime, inAmount uint64) {
	//		c := ledger.L().CalcChainInflationAmount(tsIn, tsOut, inAmount)
	//		d := ledger.L().CalcChainInflationAmountDirect(tsIn, tsOut, inAmount)
	//		if c != d {
	//			t.Fatalf("failed with tsIn=%s, tsOut=%s, inAmount=%d", tsIn.String(), tsOut.String(), inAmount)
	//		} else {
	//			t.Logf("tsIn=%s, tsOut=%s, inAmount=%d -> %d", tsIn.String(), tsOut.String(), inAmount, c)
	//		}
	//	}
	//	tsIn := base.NewLedgerTime(100, 5)
	//
	//	tsOut := base.NewLedgerTime(101, 5)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//
	//	tsOut = base.NewLedgerTime(102, 5)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//
	//	tsOut = base.NewLedgerTime(103, 5)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//
	//	tsOut = base.NewLedgerTime(104, 5)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//
	//	tsOut = base.NewLedgerTime(200, 5)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//
	//	tsOut = base.NewLedgerTime(200, 0)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//
	//	tsOut = base.NewLedgerTime(100, 100)
	//	runTest(tsIn, tsOut, 1_000_000_000)
	//})
	//t.Run("branch inflation", func(t *testing.T) {
	//	runTest := func(proof []byte) {
	//		c := ledger.L().BranchInflationBonusDirect(proof)
	//		d := ledger.L().BranchInflationBonusFromRandomnessProof(proof)
	//		if c != d {
	//			t.Fatalf("failed: c = %d, d = %d", c, d)
	//		} else {
	//			t.Logf("ok: c = %d, d = %d", c, d)
	//		}
	//	}
	//	h := blake2b.Sum256([]byte("abc"))
	//	runTest(h[:])
	//	h = blake2b.Sum256(h[:])
	//	runTest(h[:])
	//	proof, err := hex.DecodeString("50a9c10f0deb2bf0f527a24c17e6a10c874c97b6a0a627e2a164600bd6a24bde17ac5870b6b64f1dc6f3162b8f333b921af0ef2af3561c7490fd807f5a18af0a")
	//	require.NoError(t, err)
	//	runTest(proof)
	//})
}

func TestInflation(t *testing.T) {
	t.Logf("slotInflationBase: %s", util.Th(ledger.L().ID.SlotInflationBase))
	r, err := ledger.L().EvalFromSource(nil, "div(constInitialSupply, constSlotInflationBase)")
	require.NoError(t, err)
	minAmountOnSlot := func(n int) uint64 {
		return binary.BigEndian.Uint64(r) + uint64(n)
	}
	t.Logf("div(constInitialSupply, constSlotInflationBase): %s", util.Th(minAmountOnSlot(0)))

	t.Run("1", func(t *testing.T) {
		ledger.L().MustEqual("constGenesisTimeUnix", fmt.Sprintf("u64/%d", ledger.L().ID.GenesisTimeUnix))
	})
}

func TestInflationConst(t *testing.T) {
	maxSlot := math.MaxUint32
	slotsPerDay := 6 * 60 * 24
	slotsPerYear := slotsPerDay * 365

	t.Run("minimum inflatable", func(t *testing.T) {
		const slot = base.Slot(0)
		var calculated uint64
		for inAmount := uint64(1_000_000); inAmount < 500_000_000; inAmount += 1 {
			i := ledger.L().ChainInflationOneSlot(inAmount, uint32(slot))
			if i > 0 {
				t.Logf("slot: %d, minimum inflatable amount: %s  --> inflation = %d", slot, util.Th(inAmount), i)
				calculated = inAmount
				break
			}
		}
		constant := ledger.L().ID.MinimumInflatableAmount0
		t.Logf("slot inflation fraction: %s", util.Th(constant))
		require.EqualValues(t, int(constant), int(calculated))
	})
	t.Run("max slot", func(t *testing.T) {
		t.Logf("slotsPerYear: %d", slotsPerYear)
		t.Logf("slotsPerDay: %d", slotsPerDay)
		t.Logf("max slot = %s --> years %s", util.Th(maxSlot), util.Th(maxSlot/slotsPerYear))
	})
	t.Run("inflation yearly", func(t *testing.T) {
		t.Logf("max uint64: %s", util.Th(uint64(math.MaxUint64)))
		t.Logf("max int64: %s", util.Th(int64(math.MaxInt64)))
		amount := uint64(ledger.DefaultInitialSupply)
		for year := 0; year < 10; year++ {
			amountStart := amount
			slot := year * slotsPerYear
			for i := 0; i < slotsPerYear; i++ {
				infl := ledger.L().ChainInflationOneSlot(amount, uint32(slot)) + ledger.L().ID.BranchInflationBonusBase
				amount += infl
				slot += 1
			}
			b := bits(int64(amount))
			t.Logf("year %2d   final supply: %s      annual inflation: %.2f%%  occupied bits: %d, remaining: %d",
				year, util.Th(amount), float32(amount-amountStart)*100/float32(amountStart), b, 64-b)
		}
	})
}

func bits(v int64) (ret int) {
	for v > 0 {
		ret++
		v >>= 1
	}
	return
}
