// UTXODB tests for the graduated DEX order locks (sellOrder / buyOrder /
// randomizeConsumption) registered in def/lock_dex_orders.easyfl.
//
// Mirrors the scenarios validated for the local-script PoC in
// examples/dex/dex_test.go, exercised against the base-library locks
// directly (no callRedeemer / redeemScript overhead).
package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// env / helpers
// =============================================================================

type dexEnv struct {
	u          *utxodb.UTXODB
	sellerPriv ed25519.PrivateKey
	sellerLock ledger.SigLock
	buyerPriv  ed25519.PrivateKey
	buyerLock  ledger.SigLock
}

func newDexEnv(t *testing.T) *dexEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	sPriv, _, sLock := u.GenerateAddress(1)
	bPriv, _, bLock := u.GenerateAddress(2)
	require.NoError(t, u.TokensFromFaucet(sLock, 5_000_000_000))
	require.NoError(t, u.TokensFromFaucet(bLock, 5_000_000_000))
	return &dexEnv{u: u,
		sellerPriv: sPriv, sellerLock: sLock,
		buyerPriv: bPriv, buyerLock: bLock,
	}
}

func dexOutputsOf(t *testing.T, e *dexEnv, lock ledger.SigLock) []*ledger.OutputWithID {
	t.Helper()
	outsData, err := e.u.StateReader().GetUTXOsForController(lock.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(_ *base.OutputID, o *ledger.Output) bool {
		return o.Lock() != nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	return outs
}

// pureSigLockOnly filters to plain 3-element sigLock UTXOs (amounts,
// indexValues, lock) — no chain/foundry/tokenAmount extras, so
// ConsumeOutputsUnlock can take them.
func pureSigLockOnly(outs []*ledger.OutputWithID) []*ledger.OutputWithID {
	ret := make([]*ledger.OutputWithID, 0, len(outs))
	for _, o := range outs {
		if o.Output.NumElements() == 3 {
			ret = append(ret, o)
		}
	}
	return ret
}

func dexNextTs(after base.LedgerTime) base.LedgerTime {
	lib := ledger.L(after.Slot)
	ts := after.AddTicks(int(lib.TransactionPace))
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}

func dexSubmit(t *testing.T, e *dexEnv, txb *txbuilder.TxBuilder) *transaction.Transaction {
	t.Helper()
	txBytes, _, failed, err := txb.BytesWithValidation()
	require.NoError(t, err, "build/validate failed:\n%s", failed)
	var captured *transaction.Transaction
	require.NoError(t, e.u.AddTransaction(txBytes, func(tx *transaction.Transaction, e error) error {
		captured = tx
		return e
	}))
	return captured
}

func dexLoadOutput(t *testing.T, e *dexEnv, oid base.OutputID) *ledger.OutputWithID {
	t.Helper()
	data, ok := e.u.StateReader().GetUTXO(oid)
	require.True(t, ok, "expected UTXO %s in state", oid.StringShort())
	o, err := ledger.OutputFromBytes(data)
	require.NoError(t, err)
	return &ledger.OutputWithID{ID: oid, Output: o}
}

func dexOidFromTx(t *testing.T, tx *transaction.Transaction, idx byte) base.OutputID {
	t.Helper()
	oid, err := base.NewOutputID(tx.ID(), idx)
	require.NoError(t, err)
	return oid
}

// dexMintTokensFor: foundry origin + first mint to `recipient`. Returns the
// new chain ID (= tag) and the produced tokenAmount UTXO.
func dexMintTokensFor(t *testing.T, e *dexEnv, signer ed25519.PrivateKey, signerLock ledger.SigLock, recipient ledger.SigLock, amount uint64) (base.ChainID, *ledger.OutputWithID) {
	t.Helper()

	// 1) foundry origin
	originOuts := pureSigLockOnly(dexOutputsOf(t, e, signerLock))
	require.NotEmpty(t, originOuts)
	ts := dexNextTs(originOuts[0].ID.Timestamp())

	txb := txbuilder.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(originOuts...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs.AddTicks(int(ledger.L(inTs.Slot).TransactionPace)), ts)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	for i := range originOuts {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}
	foundryOut := txbuilder.MakeFoundryOriginOutput(200_000_000, signerLock, ts.Slot, 0, nil)
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)

	consumedTotal := uint64(0)
	for _, o := range originOuts {
		consumedTotal += o.Output.TokenBalance()
	}
	if change := consumedTotal - 200_000_000; change > 0 {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), signerLock))
		require.NoError(t, err)
	}
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(signer)
	originTx := dexSubmit(t, e, txb)

	foundryOid, err := base.NewOutputID(originTx.ID(), foundryIdx)
	require.NoError(t, err)
	tag := base.MakeOriginChainID(foundryOid)

	// 2) first mint = foundry transit
	foundryData := &ledger.OutputDataWithChainID{
		OutputDataWithID: ledger.OutputDataWithID{ID: foundryOid, Data: foundryOut.Bytes()},
		ChainID:          tag,
	}
	txb2 := txbuilder.New()
	_, err = txb2.TransitFoundry(foundryData, amount)
	require.NoError(t, err)
	txb2.PutSignatureUnlock(0)

	ts2 := ts.AddSlots(1)
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}
	for _, o := range pureSigLockOnly(dexOutputsOf(t, e, signerLock)) {
		idx, err := txb2.ConsumeOutput(o.Output, o.ID)
		require.NoError(t, err)
		require.NoError(t, txb2.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0))
		ts2 = base.MaximumTime(ts2, o.Timestamp().AddSlots(1))
	}
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}

	tokenOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(200_000_000).WithLock(recipient).WithTokenAmount(tag, amount)
	})
	tokenIdx, err := txb2.ProduceOutput(tokenOut)
	require.NoError(t, err)
	consumed2 := txb2.ConsumedAmount()
	produced2, _ := txb2.ProducedAmount()
	if consumed2 > produced2 {
		_, err = txb2.ProduceOutput(ledger.OutputBasic(int64(consumed2-produced2), signerLock))
		require.NoError(t, err)
	}
	txb2.TransactionData.Timestamp = ts2
	txb2.TransactionData.InputCommitment = ledger.HashOutputs(txb2.ConsumedOutputs...)
	txb2.SignED25519(signer)
	mintTx := dexSubmit(t, e, txb2)

	tokenOid, err := base.NewOutputID(mintTx.ID(), tokenIdx)
	require.NoError(t, err)
	return tag, dexLoadOutput(t, e, tokenOid)
}

func dexSumTokenAmountByTag(o *ledger.Output, tag base.ChainID) uint64 {
	var total uint64
	o.ForEach(func(i int, data []byte) bool {
		if i < 2 || len(data) == 0 {
			return true
		}
		ta, err := ledger.TokenAmountFromBytes(data)
		if err != nil {
			return true
		}
		if ta.Tag == tag {
			total += ta.Amount
		}
		return true
	})
	return total
}

// =============================================================================
// Receipt-output builders (4 constraints for sell receipts, 5 for buy receipts)
// =============================================================================

func dexSellReceipt(receiptBase uint64, recipient ledger.SigLock, orderInputIdx byte) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(receiptBase))
		o.WithLock(recipient)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{orderInputIdx}))
	})
}

func dexBuyReceipt(receiptBase uint64, recipient ledger.SigLock, orderInputIdx byte, tag base.ChainID, amount uint64) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(receiptBase))
		o.WithLock(recipient)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{orderInputIdx}))
		o.WithTokenAmount(tag, amount)
	})
}

// =============================================================================
// Order UTXO builders using the typed Lock wrappers
// =============================================================================

// dexBuildSellOrder consumes the seller's tokenUTXO + pure funding inputs and
// produces a sell-order UTXO at output index 0. Returns the consuming tx.
func dexBuildSellOrder(t *testing.T, e *dexEnv, tag base.ChainID, tokenUTXO *ledger.OutputWithID, amount, price uint64, timeoutSlots uint32, deposit uint64) *transaction.Transaction {
	t.Helper()
	pure := pureSigLockOnly(dexOutputsOf(t, e, e.sellerLock))
	require.NotEmpty(t, pure)
	ts := dexNextTs(base.MaximumTime(tokenUTXO.Timestamp(), pure[0].Timestamp()))

	inputs := append([]*ledger.OutputWithID{tokenUTXO}, pure...)
	txb := txbuilder.New()
	totalBase, _, err := txb.ConsumeOutputsUnlock(inputs...)
	require.NoError(t, err)
	require.GreaterOrEqual(t, totalBase, deposit)

	sellerHolder := base.HolderID(ledger.SigLockFromED25519PrivateKey(e.sellerPriv))
	lock := &ledger.SellOrderLock{
		SellerHolderID: sellerHolder,
		Tag:            tag,
		Price:          price,
		TimeoutSlots:   timeoutSlots,
	}
	order := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(deposit))
		o.WithLock(lock)
		o.WithTokenAmount(tag, amount)
	})
	_, err = txb.ProduceOutput(order)
	require.NoError(t, err)
	if change := totalBase - deposit; change > 0 {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), e.sellerLock))
		require.NoError(t, err)
	}
	txb.DeclareTokenConservation(tag)
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.sellerPriv)
	return dexSubmit(t, e, txb)
}

// dexBuildBuyOrder builds a buy order UTXO at output index 0.
func dexBuildBuyOrder(t *testing.T, e *dexEnv, tag base.ChainID, amount, price uint64, timeoutSlots uint32, deposit uint64) *transaction.Transaction {
	t.Helper()
	pure := pureSigLockOnly(dexOutputsOf(t, e, e.buyerLock))
	require.NotEmpty(t, pure)
	ts := dexNextTs(pure[0].Timestamp())

	txb := txbuilder.New()
	totalBase, _, err := txb.ConsumeOutputsUnlock(pure...)
	require.NoError(t, err)
	require.GreaterOrEqual(t, totalBase, deposit)

	buyerHolder := base.HolderID(ledger.SigLockFromED25519PrivateKey(e.buyerPriv))
	lock := &ledger.BuyOrderLock{
		BuyerHolderID: buyerHolder,
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
	}
	order := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(deposit))
		o.WithLock(lock)
	})
	_, err = txb.ProduceOutput(order)
	require.NoError(t, err)
	if change := totalBase - deposit; change > 0 {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), e.buyerLock))
		require.NoError(t, err)
	}
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.buyerPriv)
	return dexSubmit(t, e, txb)
}

// =============================================================================
// TestDex_LockBytecodeRoundtrip — typed-Lock round-trip via
// SellOrderLockFromOutputElements / BuyOrderLockFromOutputElements.
// =============================================================================

func TestDex_LockBytecodeRoundtrip(t *testing.T) {
	holder := base.HolderID(ledger.SigLockRandom())
	tag := base.RandomChainID()

	sell := &ledger.SellOrderLock{
		SellerHolderID: holder, Tag: tag, Price: 42_000_000, TimeoutSlots: 50,
	}
	ivs := ledger.IndexValuesTupleBytes(sell.IndexValues())
	parsedSell, err := ledger.SellOrderLockFromOutputElements(ivs, sell.LockBytecode(), ledger.L(base.MaxSlot))
	require.NoError(t, err)
	require.Equal(t, sell.SellerHolderID, parsedSell.SellerHolderID)
	require.Equal(t, sell.Tag, parsedSell.Tag)
	require.Equal(t, sell.Price, parsedSell.Price)
	require.Equal(t, sell.TimeoutSlots, parsedSell.TimeoutSlots)

	buy := &ledger.BuyOrderLock{
		BuyerHolderID: holder, Tag: tag, Amount: 7, Price: 60_000_000, TimeoutSlots: 50,
	}
	ivs = ledger.IndexValuesTupleBytes(buy.IndexValues())
	parsedBuy, err := ledger.BuyOrderLockFromOutputElements(ivs, buy.LockBytecode(), ledger.L(base.MaxSlot))
	require.NoError(t, err)
	require.Equal(t, buy.BuyerHolderID, parsedBuy.BuyerHolderID)
	require.Equal(t, buy.Tag, parsedBuy.Tag)
	require.Equal(t, buy.Amount, parsedBuy.Amount)
	require.Equal(t, buy.Price, parsedBuy.Price)
	require.Equal(t, buy.TimeoutSlots, parsedBuy.TimeoutSlots)
}

// =============================================================================
// TestDex_SellOrderHappyPath — buyer lifts a sell order; seller's receipt
// carries deposit + amount*price; buyer's UTXO carries amount tokens.
// =============================================================================

func TestDex_SellOrderHappyPath(t *testing.T) {
	e := newDexEnv(t)
	const (
		amount       = uint64(10)
		price        = uint64(50_000_000)
		timeoutSlots = uint32(50)
		deposit      = uint64(200_000_000)
	)
	tag, tokenUTXO := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amount)
	orderTx := dexBuildSellOrder(t, e, tag, tokenUTXO, amount, price, timeoutSlots, deposit)
	orderUTXO := dexLoadOutput(t, e, dexOidFromTx(t, orderTx, 0))

	buyerPure := pureSigLockOnly(dexOutputsOf(t, e, e.buyerLock))
	require.NotEmpty(t, buyerPure)
	fillTs := dexNextTs(base.MaximumTime(orderUTXO.Timestamp(), buyerPure[0].Timestamp()))

	txb := txbuilder.New()
	orderInIdx, err := txb.ConsumeOutput(orderUTXO.Output, orderUTXO.ID)
	require.NoError(t, err)
	fundingTotal := uint64(0)
	for i, in := range buyerPure {
		idx, err := txb.ConsumeOutput(in.Output, in.ID)
		require.NoError(t, err)
		if i == 0 {
			txb.PutSignatureUnlock(idx)
		} else {
			require.NoError(t, txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, byte(int(orderInIdx)+1)))
		}
		fundingTotal += in.Output.TokenBalance()
	}
	require.GreaterOrEqual(t, fundingTotal, amount*price)

	sellerSigLock := ledger.SigLockFromED25519PrivateKey(e.sellerPriv)
	receipt := dexSellReceipt(deposit+amount*price, sellerSigLock, byte(orderInIdx))
	receiptIdx, err := txb.ProduceOutput(receipt)
	require.NoError(t, err)
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	const dust = uint64(100_000_000)
	buyerOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(dust)).WithLock(e.buyerLock).WithTokenAmount(tag, amount)
	})
	_, err = txb.ProduceOutput(buyerOut)
	require.NoError(t, err)

	if change := fundingTotal - amount*price - dust; change > 0 {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), e.buyerLock))
		require.NoError(t, err)
	}
	txb.DeclareTokenConservation(tag)
	txb.TransactionData.Timestamp = fillTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.buyerPriv)
	tx := dexSubmit(t, e, txb)

	r := dexLoadOutput(t, e, dexOidFromTx(t, tx, byte(receiptIdx)))
	require.Equal(t, deposit+amount*price, r.Output.TokenBalance())
}

// =============================================================================
// TestDex_BuyOrderHappyPath — symmetric path. Seller fills the buy order.
// =============================================================================

func TestDex_BuyOrderHappyPath(t *testing.T) {
	e := newDexEnv(t)
	const (
		amount       = uint64(10)
		price        = uint64(50_000_000)
		timeoutSlots = uint32(50)
		deposit      = uint64(2_000_000_000)
	)
	tag, tokenUTXO := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amount)
	orderTx := dexBuildBuyOrder(t, e, tag, amount, price, timeoutSlots, deposit)
	orderUTXO := dexLoadOutput(t, e, dexOidFromTx(t, orderTx, 0))

	fillTs := dexNextTs(base.MaximumTime(orderUTXO.Timestamp(), tokenUTXO.Timestamp()))

	txb := txbuilder.New()
	orderInIdx, err := txb.ConsumeOutput(orderUTXO.Output, orderUTXO.ID)
	require.NoError(t, err)
	tokInIdx, err := txb.ConsumeOutput(tokenUTXO.Output, tokenUTXO.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(tokInIdx)

	buyerSigLock := ledger.SigLockFromED25519PrivateKey(e.buyerPriv)
	receipt := dexBuyReceipt(deposit-amount*price, buyerSigLock, byte(orderInIdx), tag, amount)
	receiptIdx, err := txb.ProduceOutput(receipt)
	require.NoError(t, err)
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	// Seller takes payment as their own sigLock output.
	sellerBase := amount*price + tokenUTXO.Output.TokenBalance()
	_, err = txb.ProduceOutput(ledger.OutputBasic(int64(sellerBase), e.sellerLock))
	require.NoError(t, err)

	txb.DeclareTokenConservation(tag)
	txb.TransactionData.Timestamp = fillTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.sellerPriv)
	tx := dexSubmit(t, e, txb)

	r := dexLoadOutput(t, e, dexOidFromTx(t, tx, byte(receiptIdx)))
	require.Equal(t, deposit-amount*price, r.Output.TokenBalance())
	require.Equal(t, amount, dexSumTokenAmountByTag(r.Output, tag))
}

// =============================================================================
// TestDex_SellOrderReclaim — after timeout, seller reclaims via sigLock unlock.
// =============================================================================

func TestDex_SellOrderReclaim(t *testing.T) {
	e := newDexEnv(t)
	const (
		amount       = uint64(5)
		price        = uint64(10_000_000)
		timeoutSlots = uint32(20)
		deposit      = uint64(200_000_000)
	)
	tag, tokenUTXO := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amount)
	orderTx := dexBuildSellOrder(t, e, tag, tokenUTXO, amount, price, timeoutSlots, deposit)
	orderUTXO := dexLoadOutput(t, e, dexOidFromTx(t, orderTx, 0))

	reclaimTs := base.T(orderUTXO.Timestamp().Slot+timeoutSlots+1, 1)
	txb := txbuilder.New()
	orderInIdx, err := txb.ConsumeOutput(orderUTXO.Output, orderUTXO.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{0xff})
	txb.PutSignatureUnlock(orderInIdx)

	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(orderUTXO.Output.TokenBalance())).
			WithLock(e.sellerLock).
			WithTokenAmount(tag, amount)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)
	txb.DeclareTokenConservation(tag)
	txb.TransactionData.Timestamp = reclaimTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.sellerPriv)
	reclaimTx := dexSubmit(t, e, txb)

	r := dexLoadOutput(t, e, dexOidFromTx(t, reclaimTx, 0))
	require.Equal(t, deposit, r.Output.TokenBalance())
	require.Equal(t, amount, dexSumTokenAmountByTag(r.Output, tag))
}

// =============================================================================
// TestDex_FoldAttackRejection — two sell orders cannot share one receipt;
// the 1-byte fold-attack literal can satisfy only one consumed order.
// =============================================================================

func TestDex_FoldAttackRejection(t *testing.T) {
	e := newDexEnv(t)
	const (
		amountA, amountB = uint64(7), uint64(13)
		priceA, priceB   = uint64(40_000_000), uint64(60_000_000)
		timeoutSlots     = uint32(50)
		deposit          = uint64(200_000_000)
	)
	tagA, tokenA := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amountA)
	tagB, tokenB := dexMintTokensFor(t, e, e.sellerPriv, e.sellerLock, e.sellerLock, amountB)
	orderA := dexLoadOutput(t, e, dexOidFromTx(t,
		dexBuildSellOrder(t, e, tagA, tokenA, amountA, priceA, timeoutSlots, deposit), 0))
	orderB := dexLoadOutput(t, e, dexOidFromTx(t,
		dexBuildSellOrder(t, e, tagB, tokenB, amountB, priceB, timeoutSlots, deposit), 0))

	buyerPure := pureSigLockOnly(dexOutputsOf(t, e, e.buyerLock))
	require.NotEmpty(t, buyerPure)
	fillTs := dexNextTs(base.MaximumTime(
		base.MaximumTime(orderA.Timestamp(), orderB.Timestamp()),
		buyerPure[0].Timestamp(),
	))

	txb := txbuilder.New()
	orderAIdx, err := txb.ConsumeOutput(orderA.Output, orderA.ID)
	require.NoError(t, err)
	orderBIdx, err := txb.ConsumeOutput(orderB.Output, orderB.ID)
	require.NoError(t, err)
	for i, in := range buyerPure {
		idx, err := txb.ConsumeOutput(in.Output, in.ID)
		require.NoError(t, err)
		if i == 0 {
			txb.PutSignatureUnlock(idx)
		} else {
			require.NoError(t, txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, byte(int(orderBIdx)+1)))
		}
	}

	sellerSigLock := ledger.SigLockFromED25519PrivateKey(e.sellerPriv)
	fatReceipt := dexSellReceipt(
		2*deposit+amountA*priceA+amountB*priceB,
		sellerSigLock,
		byte(orderAIdx),
	)
	receiptIdx, err := txb.ProduceOutput(fatReceipt)
	require.NoError(t, err)
	// Both orders unlock with K=receiptIdx, pointing to a single receipt.
	txb.PutUnlockParams(orderAIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})
	txb.PutUnlockParams(orderBIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	const dust = uint64(100_000_000)
	for _, ta := range []struct {
		tag    base.ChainID
		amount uint64
	}{{tagA, amountA}, {tagB, amountB}} {
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(dust)).WithLock(e.buyerLock).WithTokenAmount(ta.tag, ta.amount)
		}))
		require.NoError(t, err)
	}

	consumed := txb.ConsumedAmount()
	produced, _ := txb.ProducedAmount()
	if consumed > produced {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(consumed-produced), e.buyerLock))
		require.NoError(t, err)
	}
	txb.DeclareTokenConservation(tagA)
	txb.DeclareTokenConservation(tagB)
	txb.TransactionData.Timestamp = fillTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.buyerPriv)

	_, _, failed, err := txb.BytesWithValidation()
	require.Error(t, err, "fold attack must be rejected; tx string:\n%s", failed)
	require.Contains(t, err.Error(), "sellOrder")
}
