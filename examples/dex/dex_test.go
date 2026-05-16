// UTXODB tests for the DEX order locks PoC. Covers the four core flows
// (sell match, buy match, sell reclaim, buy reclaim) and one structural
// negative (fold attack on a sell-order fill).
package dex

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
// dexEnv — shared scaffold
// =============================================================================

type dexEnv struct {
	u          *utxodb.UTXODB
	sellerPriv ed25519.PrivateKey
	sellerLock ledger.SigLock
	buyerPriv  ed25519.PrivateKey
	buyerLock  ledger.SigLock
}

// newDexEnv creates a utxodb with two funded addresses (seller, buyer).
func newDexEnv(t *testing.T) *dexEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	sPriv, _, sLock := u.GenerateAddress(1)
	bPriv, _, bLock := u.GenerateAddress(2)
	require.NoError(t, u.TokensFromFaucet(sLock, 5_000_000_000))
	require.NoError(t, u.TokensFromFaucet(bLock, 5_000_000_000))
	return &dexEnv{
		u:          u,
		sellerPriv: sPriv, sellerLock: sLock,
		buyerPriv: bPriv, buyerLock: bLock,
	}
}

func (e *dexEnv) outputsOf(t *testing.T, lock ledger.SigLock) []*ledger.OutputWithID {
	t.Helper()
	outsData, err := e.u.StateReader().GetUTXOsForController(lock.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(_ *base.OutputID, o *ledger.Output) bool {
		return o.Lock() != nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	return outs
}

func (e *dexEnv) submit(t *testing.T, txb *txbuilder.TxBuilder) *transaction.Transaction {
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

// nextTs picks a tx timestamp ≥ `after` + transaction pace, snapping off the
// slot boundary.
func nextTs(after base.LedgerTime) base.LedgerTime {
	lib := ledger.L(after.Slot)
	ts := after.AddTicks(int(lib.TransactionPace))
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}

// =============================================================================
// Foundry + mint helpers — give the seller a tokenAmount UTXO for the tag.
// =============================================================================

// mintTokensFor creates a foundry, mints `amount` of the new tag to
// `recipient`, and returns the new tag (chainID) plus the mint output.
func (e *dexEnv) mintTokensFor(t *testing.T, signer ed25519.PrivateKey, signerLock ledger.SigLock, recipient ledger.SigLock, amount uint64) (base.ChainID, *ledger.OutputWithID) {
	t.Helper()

	// 1) foundry origin (no policy).
	originOuts := e.outputsOf(t, signerLock)
	require.NotEmpty(t, originOuts)
	ts := nextTs(originOuts[0].ID.Timestamp())

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
	require.NoError(t, foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)

	consumedTotal := uint64(0)
	for _, o := range originOuts {
		consumedTotal += o.Output.TokenBalance()
	}
	change := consumedTotal - 200_000_000
	if change > 0 {
		ret := ledger.OutputBasic(int64(change), signerLock)
		_, err = txb.ProduceOutput(ret)
		require.NoError(t, err)
	}

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(signer)
	originTx := e.submit(t, txb)

	foundryOid, err := base.NewOutputID(originTx.ID(), foundryIdx)
	require.NoError(t, err)
	tag := base.MakeOriginChainID(foundryOid)

	// 2) mint = first foundry transit. tag turns into the real chain ID.
	foundryData := &ledger.OutputDataWithChainID{
		OutputDataWithID: ledger.OutputDataWithID{
			ID:   foundryOid,
			Data: foundryOut.Bytes(),
		},
		ChainID: tag,
	}
	txb2 := txbuilder.New()
	_, err = txb2.TransitFoundry(foundryData, amount)
	require.NoError(t, err)
	txb2.PutSignatureUnlock(0)

	ts2 := ts.AddSlots(1)
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}
	// Add pure-PRXI funding (filter out the foundry input we just consumed
	// at index 0; pureSigLockOutputs further filters to plain 3-element
	// sigLocks).
	changeOuts := pureSigLockOutputs(e.outputsOf(t, signerLock))
	for _, o := range changeOuts {
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
	require.NoError(t, tokenOut.EnoughAmountForStorageDeposit())
	tokenIdx, err := txb2.ProduceOutput(tokenOut)
	require.NoError(t, err)

	// Remainder back to signer.
	consumed2 := txb2.ConsumedAmount()
	produced2, _ := txb2.ProducedAmount()
	if consumed2 > produced2 {
		rem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(consumed2 - produced2).WithLock(signerLock)
		})
		_, err = txb2.ProduceOutput(rem)
		require.NoError(t, err)
	}

	txb2.TransactionData.Timestamp = ts2
	txb2.TransactionData.InputCommitment = ledger.HashOutputs(txb2.ConsumedOutputs...)
	txb2.SignED25519(signer)
	mintTx := e.submit(t, txb2)

	tokenOid, err := base.NewOutputID(mintTx.ID(), tokenIdx)
	require.NoError(t, err)
	tokenData, ok := e.u.StateReader().GetUTXO(tokenOid)
	require.True(t, ok)
	tokenParsed, err := ledger.OutputFromBytes(tokenData)
	require.NoError(t, err)
	return tag, &ledger.OutputWithID{ID: tokenOid, Output: tokenParsed}
}

// =============================================================================
// Locate an order UTXO after submission.
// =============================================================================

func loadOutput(t *testing.T, u *utxodb.UTXODB, oid base.OutputID) *ledger.OutputWithID {
	t.Helper()
	data, ok := u.StateReader().GetUTXO(oid)
	require.True(t, ok, "expected UTXO %s in state", oid.StringShort())
	o, err := ledger.OutputFromBytes(data)
	require.NoError(t, err)
	return &ledger.OutputWithID{ID: oid, Output: o}
}

func outputIDFromTx(t *testing.T, tx *transaction.Transaction, idx byte) base.OutputID {
	t.Helper()
	oid, err := base.NewOutputID(tx.ID(), idx)
	require.NoError(t, err)
	return oid
}

// pureSigLockOutputs filters to plain sigLock UTXOs: exactly 3 constraint
// positions (amounts, indexValues, lock) — no tokenAmount, no chain, no
// foundry, no extras. Picking these for funding inputs lets
// ConsumeOutputsUnlock unlock them with a single signature + references.
func pureSigLockOutputs(outs []*ledger.OutputWithID) []*ledger.OutputWithID {
	ret := make([]*ledger.OutputWithID, 0, len(outs))
	for _, o := range outs {
		if o.Output.NumElements() == 3 {
			ret = append(ret, o)
		}
	}
	return ret
}

// =============================================================================
// TestSellOrderHappyPath: seller mints tokens, posts a sell order, the buyer
// fills it, and we verify the seller's receipt and the buyer's token UTXO.
// =============================================================================

func TestSellOrderHappyPath(t *testing.T) {
	e := newDexEnv(t)

	const (
		amount       = uint64(10)
		price        = uint64(50_000_000) // 50M base / token
		timeoutSlots = uint32(50)
		deposit      = uint64(200_000_000)
	)

	tag, tokenUTXO := e.mintTokensFor(t, e.sellerPriv, e.sellerLock, e.sellerLock, amount)

	// Seller funds the order with the token UTXO + a fresh pure-PRXI UTXO.
	pure := pureSigLockOutputs(e.outputsOf(t, e.sellerLock))
	require.NotEmpty(t, pure)

	ts := nextTs(base.MaximumTime(tokenUTXO.Timestamp(), pure[0].Timestamp()))
	txb, err := BuildSellOrder(BuildSellOrderParams{
		SellerPrivKey: e.sellerPriv,
		SellerSigLock: e.sellerLock,
		FundingInputs: append([]*ledger.OutputWithID{tokenUTXO}, pure...),
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
		Deposit:       deposit,
		TxTimestamp:   ts,
	})
	require.NoError(t, err)
	orderTx := e.submit(t, txb)
	orderOID := outputIDFromTx(t, orderTx, 0)
	orderUTXO := loadOutput(t, e.u, orderOID)

	// Buyer fills the order.
	buyerPure := pureSigLockOutputs(e.outputsOf(t, e.buyerLock))
	require.NotEmpty(t, buyerPure)
	fillTs := nextTs(base.MaximumTime(orderUTXO.Timestamp(), buyerPure[0].Timestamp()))

	fillTxb, err := BuildFillSellOrder(BuildFillSellOrderParams{
		BuyerPrivKey: e.buyerPriv,
		BuyerSigLock: e.buyerLock,
		OrderUTXO:    orderUTXO,
		FundingInputs: buyerPure,
		TxTimestamp:  fillTs,
	})
	require.NoError(t, err)
	fillTx := e.submit(t, fillTxb)
	require.True(t, fillTx.IsScriptRedeemed(GetBins().Hash))

	// Receipt to seller at output index 0: amounts == deposit + amount*price.
	receipt := loadOutput(t, e.u, outputIDFromTx(t, fillTx, 0))
	require.Equal(t, deposit+amount*price, receipt.Output.TokenBalance(),
		"seller receipt must carry deposit + amount*price")
	require.Equal(t, ledger.SigLockName, receipt.Output.Lock().Name(),
		"receipt must be sigLock-locked")

	// Buyer's token UTXO at output index 1: tokenAmount(tag, amount).
	buyerToken := loadOutput(t, e.u, outputIDFromTx(t, fillTx, 1))
	supplied := sumTokenAmountByTag(buyerToken.Output, tag)
	require.Equal(t, amount, supplied, "buyer must end up with the X native tokens")
}

// =============================================================================
// TestBuyOrderHappyPath: buyer posts a buy order, seller (who already holds
// the tokens) fills it, receipt to buyer carries (originalBase - amount*price)
// and the expected tokenAmount; seller pockets amount*price.
// =============================================================================

func TestBuyOrderHappyPath(t *testing.T) {
	e := newDexEnv(t)

	const (
		amount       = uint64(10)
		price        = uint64(50_000_000)
		timeoutSlots = uint32(50)
		deposit      = uint64(2_000_000_000) // ≥ amount*price + receipt min deposit
	)

	tag, tokenUTXO := e.mintTokensFor(t, e.sellerPriv, e.sellerLock, e.sellerLock, amount)

	// Buyer posts the buy order.
	buyerPure := pureSigLockOutputs(e.outputsOf(t, e.buyerLock))
	require.NotEmpty(t, buyerPure)
	ts := nextTs(buyerPure[0].Timestamp())

	orderTxb, err := BuildBuyOrder(BuildBuyOrderParams{
		BuyerPrivKey:  e.buyerPriv,
		BuyerSigLock:  e.buyerLock,
		FundingInputs: buyerPure,
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
		Deposit:       deposit,
		TxTimestamp:   ts,
	})
	require.NoError(t, err)
	orderTx := e.submit(t, orderTxb)
	orderUTXO := loadOutput(t, e.u, outputIDFromTx(t, orderTx, 0))

	// Seller fills with their token UTXO.
	fillTs := nextTs(base.MaximumTime(orderUTXO.Timestamp(), tokenUTXO.Timestamp()))
	fillTxb, err := BuildFillBuyOrder(BuildFillBuyOrderParams{
		SellerPrivKey: e.sellerPriv,
		SellerSigLock: e.sellerLock,
		OrderUTXO:     orderUTXO,
		TokenInputs:   []*ledger.OutputWithID{tokenUTXO},
		TxTimestamp:   fillTs,
	})
	require.NoError(t, err)
	fillTx := e.submit(t, fillTxb)
	require.True(t, fillTx.IsScriptRedeemed(GetBins().Hash))

	// Receipt to buyer at output index 0: amounts == deposit - amount*price,
	// plus a tokenAmount(tag, amount).
	receipt := loadOutput(t, e.u, outputIDFromTx(t, fillTx, 0))
	require.Equal(t, deposit-amount*price, receipt.Output.TokenBalance())
	require.Equal(t, amount, sumTokenAmountByTag(receipt.Output, tag))

	// Seller pocketed amount*price as their own sigLock output (idx 1).
	sellerOut := loadOutput(t, e.u, outputIDFromTx(t, fillTx, 1))
	require.GreaterOrEqual(t, sellerOut.Output.TokenBalance(), amount*price,
		"seller output must include at least amount*price")
}

// =============================================================================
// TestSellOrderReclaim: seller posts a sell order, no one fills it, after
// the timeout the seller reclaims via sigLock unlock.
// =============================================================================

func TestSellOrderReclaim(t *testing.T) {
	e := newDexEnv(t)

	const (
		amount       = uint64(5)
		price        = uint64(10_000_000)
		timeoutSlots = uint32(20)
		deposit      = uint64(200_000_000)
	)

	tag, tokenUTXO := e.mintTokensFor(t, e.sellerPriv, e.sellerLock, e.sellerLock, amount)
	pure := pureSigLockOutputs(e.outputsOf(t, e.sellerLock))
	require.NotEmpty(t, pure)
	ts := nextTs(base.MaximumTime(tokenUTXO.Timestamp(), pure[0].Timestamp()))

	txb, err := BuildSellOrder(BuildSellOrderParams{
		SellerPrivKey: e.sellerPriv,
		SellerSigLock: e.sellerLock,
		FundingInputs: append([]*ledger.OutputWithID{tokenUTXO}, pure...),
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
		Deposit:       deposit,
		TxTimestamp:   ts,
	})
	require.NoError(t, err)
	orderTx := e.submit(t, txb)
	orderUTXO := loadOutput(t, e.u, outputIDFromTx(t, orderTx, 0))

	// Reclaim AFTER timeout (slot delta ≥ timeoutSlots).
	reclaimTs := base.T(orderUTXO.Timestamp().Slot+timeoutSlots+1, 1)
	reclaimTxb, err := BuildReclaimOrder(BuildReclaimOrderParams{
		IssuerPrivKey: e.sellerPriv,
		IssuerSigLock: e.sellerLock,
		OrderUTXO:     orderUTXO,
		IsSellOrder:   true,
		TxTimestamp:   reclaimTs,
	})
	require.NoError(t, err)
	reclaimTx := e.submit(t, reclaimTxb)

	// Seller's reclaim output (idx 0) carries the deposit + the tokens.
	out := loadOutput(t, e.u, outputIDFromTx(t, reclaimTx, 0))
	require.Equal(t, deposit, out.Output.TokenBalance())
	require.Equal(t, amount, sumTokenAmountByTag(out.Output, tag))
}

// =============================================================================
// TestBuyOrderReclaim: buyer posts a buy order, no one fills it, after
// the timeout the buyer reclaims via sigLock unlock.
// =============================================================================

func TestBuyOrderReclaim(t *testing.T) {
	e := newDexEnv(t)

	const (
		amount       = uint64(7)
		price        = uint64(5_000_000)
		timeoutSlots = uint32(20)
		deposit      = uint64(500_000_000)
	)

	tag, _ := e.mintTokensFor(t, e.sellerPriv, e.sellerLock, e.sellerLock, amount)

	buyerPure := pureSigLockOutputs(e.outputsOf(t, e.buyerLock))
	require.NotEmpty(t, buyerPure)
	ts := nextTs(buyerPure[0].Timestamp())

	orderTxb, err := BuildBuyOrder(BuildBuyOrderParams{
		BuyerPrivKey:  e.buyerPriv,
		BuyerSigLock:  e.buyerLock,
		FundingInputs: buyerPure,
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
		Deposit:       deposit,
		TxTimestamp:   ts,
	})
	require.NoError(t, err)
	orderTx := e.submit(t, orderTxb)
	orderUTXO := loadOutput(t, e.u, outputIDFromTx(t, orderTx, 0))

	reclaimTs := base.T(orderUTXO.Timestamp().Slot+timeoutSlots+1, 1)
	reclaimTxb, err := BuildReclaimOrder(BuildReclaimOrderParams{
		IssuerPrivKey: e.buyerPriv,
		IssuerSigLock: e.buyerLock,
		OrderUTXO:     orderUTXO,
		IsSellOrder:   false,
		TxTimestamp:   reclaimTs,
	})
	require.NoError(t, err)
	reclaimTx := e.submit(t, reclaimTxb)
	out := loadOutput(t, e.u, outputIDFromTx(t, reclaimTx, 0))
	require.Equal(t, deposit, out.Output.TokenBalance())
}

// =============================================================================
// TestSellOrderUnderpaymentRejected: a buyer who tries to pay less than
// (originalBase + amount*price) — by mutating the receipt amount — has the
// tx rejected by the sell-order's consume-side check.
// =============================================================================

func TestSellOrderUnderpaymentRejected(t *testing.T) {
	e := newDexEnv(t)

	const (
		amount       = uint64(10)
		price        = uint64(50_000_000)
		timeoutSlots = uint32(50)
		deposit      = uint64(200_000_000)
	)

	tag, tokenUTXO := e.mintTokensFor(t, e.sellerPriv, e.sellerLock, e.sellerLock, amount)
	pure := pureSigLockOutputs(e.outputsOf(t, e.sellerLock))
	require.NotEmpty(t, pure)
	ts := nextTs(base.MaximumTime(tokenUTXO.Timestamp(), pure[0].Timestamp()))

	orderTxb, err := BuildSellOrder(BuildSellOrderParams{
		SellerPrivKey: e.sellerPriv,
		SellerSigLock: e.sellerLock,
		FundingInputs: append([]*ledger.OutputWithID{tokenUTXO}, pure...),
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
		Deposit:       deposit,
		TxTimestamp:   ts,
	})
	require.NoError(t, err)
	orderTx := e.submit(t, orderTxb)
	orderUTXO := loadOutput(t, e.u, outputIDFromTx(t, orderTx, 0))

	// Manually build a "fill" tx where the receipt's amount is short by 1.
	buyerPure := pureSigLockOutputs(e.outputsOf(t, e.buyerLock))
	require.NotEmpty(t, buyerPure)
	fillTs := nextTs(base.MaximumTime(orderUTXO.Timestamp(), buyerPure[0].Timestamp()))

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

	// Receipt shorted by 1.
	requiredReceipt := deposit + amount*price
	shortReceipt := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(requiredReceipt - 1))
		o.WithLock(e.sellerLock)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{byte(orderInIdx)}))
	})
	receiptIdx, err := txb.ProduceOutput(shortReceipt)
	require.NoError(t, err)
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	// Buyer's token output.
	const dust = uint64(100_000_000)
	buyerTokenOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(dust))
		o.WithLock(e.buyerLock)
		o.WithTokenAmount(tag, amount)
	})
	_, err = txb.ProduceOutput(buyerTokenOut)
	require.NoError(t, err)

	// Change back to buyer (consumed - produced); we under-paid the receipt,
	// so the missing 1 token gets recovered by the buyer here. Conservation
	// still holds at the tx level — the failure must come from the dex lock.
	consumed := txb.ConsumedAmount()
	produced, _ := txb.ProducedAmount()
	if consumed > produced {
		_, err = txb.ProduceOutput(ledger.OutputBasic(int64(consumed-produced), e.buyerLock))
		require.NoError(t, err)
	}

	txb.DeclareTokenConservation(tag)
	require.NoError(t, pushRedeemScript(txb))
	finaliseAndSign(txb, fillTs, e.buyerPriv)

	_, _, failedTx, err := txb.BytesWithValidation()
	require.Error(t, err, "underpayment must be rejected by the dex lock; tx string:\n%s", failedTx)
	// The dex lock's callRedeemer is the failing constraint. Inner !!! error
	// strings don't bubble through the outer trace, so we just assert the
	// constraint failed at the order's lock element (path .out[0].constraint[2]).
	require.Contains(t, err.Error(), "callRedeemer")
}
