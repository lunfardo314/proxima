package dex

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// =============================================================================
// TxBuilder helpers — produce the DEX covenant transactions sketched in
// claude/archive/shipped/dex_orders.md.
//
// Convention: each Build* function below takes the inputs explicitly so
// callers control coin selection, and returns the *exhelp.Builder ready
// to sign + serialise (finalisation/signing is done inside).
// =============================================================================

// HolderIDOf mirrors the standard Proxima idiom hash(sigType || pubkey).
func HolderIDOf(priv ed25519.PrivateKey) base.HolderID {
	return base.HolderIDFromPublicKey(base.SignatureTypeED25519, priv.Public().(ed25519.PublicKey))
}

// orderIndexEntry builds the slot-1 position-1 entry "ORDR || tag || sideByte".
func orderIndexEntry(tag base.ChainID, sideByte byte) []byte {
	out := make([]byte, 0, 4+base.ChainIDLength+1)
	out = append(out, 'O', 'R', 'D', 'R')
	out = append(out, tag[:]...)
	out = append(out, sideByte)
	return out
}

// pushRedeemScript attaches the dex local-script binary onto the tx via
// redeemScript(<bin>). Required on every tx that creates, fills, or reclaims
// an order UTXO.
func pushRedeemScript(txb *exhelp.Builder) error {
	bc, err := RedeemScriptConstraint()
	if err != nil {
		return err
	}
	txb.PushTxConstraint(bc)
	return nil
}

// finaliseAndSign sets timestamp, computes input commitment, and signs ed25519.
func finaliseAndSign(txb *exhelp.Builder, ts base.LedgerTime, priv ed25519.PrivateKey) {
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
}

// =============================================================================
// Order UTXO construction
// =============================================================================

// BuildSellOrderParams collects inputs for BuildSellOrder.
type BuildSellOrderParams struct {
	SellerPrivKey ed25519.PrivateKey
	SellerSigLock ledger.SigLock
	// FundingInputs cover the deposit posted on the sell order, plus the
	// inputs holding the native tokens (tag, Amount) being sold. The
	// builder consumes them all in order; the first input becomes the
	// signature-bearing one, the rest unlock by reference.
	FundingInputs []*ledger.OutputWithID
	Tag           base.ChainID
	Amount        uint64 // native tokens for sale (must equal Σ inputs of this tag)
	Price         uint64 // base tokens per one native token
	TimeoutSlots  uint32 // ≥ 12
	Deposit       uint64 // base tokens on the order UTXO (≥ storage deposit)
	TxTimestamp   base.LedgerTime
}

// BuildSellOrder creates a sell-order UTXO and signs the tx.
func BuildSellOrder(p BuildSellOrderParams) (*exhelp.Builder, error) {
	if p.Amount == 0 || p.Price == 0 || p.Deposit == 0 {
		return nil, fmt.Errorf("BuildSellOrder: amount/price/deposit must be positive")
	}
	if p.TimeoutSlots < 12 {
		return nil, fmt.Errorf("BuildSellOrder: TimeoutSlots %d below floor (12)", p.TimeoutSlots)
	}

	txb := exhelp.New()
	totalBase, _, err := txb.ConsumeOutputsUnlock(p.FundingInputs...)
	if err != nil {
		return nil, fmt.Errorf("BuildSellOrder: consume inputs: %w", err)
	}
	if totalBase < p.Deposit {
		return nil, fmt.Errorf("BuildSellOrder: insufficient base tokens: have %d, need %d", totalBase, p.Deposit)
	}

	lockBC, err := SellOrderLockBytecode(p.Price, p.TimeoutSlots)
	if err != nil {
		return nil, err
	}
	seller := HolderIDOf(p.SellerPrivKey)
	indexEntry := orderIndexEntry(p.Tag, 0x01)

	order := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(p.Deposit))
		o.PutConstraint(
			ledger.IndexValuesTupleBytes([][]byte{seller[:], indexEntry}),
			ledger.ConstraintIndexIndexValues,
		)
		o.PutConstraint(lockBC, ledger.ConstraintIndexLock)
		// tokenAmount at the next free position (3); also adds a compound
		// (seller||tag) entry into slot-1 for standard "my UTXOs holding T"
		// indexing.
		o.WithTokenAmount(p.Tag, p.Amount)
	})
	if _, err := txb.ProduceOutput(order); err != nil {
		return nil, fmt.Errorf("BuildSellOrder: produce order: %w", err)
	}

	// Change back to seller.
	if change := totalBase - p.Deposit; change > 0 {
		ret := ledger.OutputBasic(int64(change), p.SellerSigLock)
		if _, err := txb.ProduceOutput(ret); err != nil {
			return nil, fmt.Errorf("BuildSellOrder: produce change: %w", err)
		}
	}

	// Tag conservation: tokens flow from inputs into the order's tokenAmount,
	// no mint / burn.
	txb.DeclareTokenConservation(p.Tag)
	if err := pushRedeemScript(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.SellerPrivKey)
	return txb, nil
}

// BuildBuyOrderParams collects inputs for BuildBuyOrder.
type BuildBuyOrderParams struct {
	BuyerPrivKey ed25519.PrivateKey
	BuyerSigLock ledger.SigLock
	FundingInputs []*ledger.OutputWithID
	Tag           base.ChainID
	Amount        uint64 // native tokens to buy
	Price         uint64 // base tokens per one native token
	TimeoutSlots  uint32 // ≥ 12
	// Deposit is the total base-token balance posted on the buy order UTXO.
	// Must be ≥ Amount*Price + storageDepositOfSellerReceipt; the seller
	// pulls Amount*Price out and leaves the remainder back to the buyer.
	Deposit     uint64
	TxTimestamp base.LedgerTime
}

func BuildBuyOrder(p BuildBuyOrderParams) (*exhelp.Builder, error) {
	if p.Amount == 0 || p.Price == 0 || p.Deposit == 0 {
		return nil, fmt.Errorf("BuildBuyOrder: amount/price/deposit must be positive")
	}
	if p.TimeoutSlots < 12 {
		return nil, fmt.Errorf("BuildBuyOrder: TimeoutSlots %d below floor (12)", p.TimeoutSlots)
	}

	txb := exhelp.New()
	totalBase, _, err := txb.ConsumeOutputsUnlock(p.FundingInputs...)
	if err != nil {
		return nil, fmt.Errorf("BuildBuyOrder: consume inputs: %w", err)
	}
	if totalBase < p.Deposit {
		return nil, fmt.Errorf("BuildBuyOrder: insufficient base tokens: have %d, need %d", totalBase, p.Deposit)
	}

	lockBC, err := BuyOrderLockBytecode(p.Amount, p.Price, p.TimeoutSlots)
	if err != nil {
		return nil, err
	}
	buyer := HolderIDOf(p.BuyerPrivKey)
	indexEntry := orderIndexEntry(p.Tag, 0x00)

	order := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(p.Deposit))
		o.PutConstraint(
			ledger.IndexValuesTupleBytes([][]byte{buyer[:], indexEntry}),
			ledger.ConstraintIndexIndexValues,
		)
		o.PutConstraint(lockBC, ledger.ConstraintIndexLock)
	})
	if _, err := txb.ProduceOutput(order); err != nil {
		return nil, fmt.Errorf("BuildBuyOrder: produce order: %w", err)
	}

	if change := totalBase - p.Deposit; change > 0 {
		ret := ledger.OutputBasic(int64(change), p.BuyerSigLock)
		if _, err := txb.ProduceOutput(ret); err != nil {
			return nil, fmt.Errorf("BuildBuyOrder: produce change: %w", err)
		}
	}

	if err := pushRedeemScript(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.BuyerPrivKey)
	return txb, nil
}

// =============================================================================
// Order fills
// =============================================================================

// BuildFillSellOrderParams collects inputs for BuildFillSellOrder. The buyer
// signs the tx, consumes the sell order, and pays the seller via a receipt
// output of the prescribed shape.
type BuildFillSellOrderParams struct {
	BuyerPrivKey ed25519.PrivateKey
	BuyerSigLock ledger.SigLock // for change
	OrderUTXO    *ledger.OutputWithID
	// FundingInputs cover the receipt payment (originalBase + amount*price)
	// to the seller. The first becomes signature-bearing; the rest use
	// reference unlocks.
	FundingInputs []*ledger.OutputWithID
	TxTimestamp   base.LedgerTime
}

func BuildFillSellOrder(p BuildFillSellOrderParams) (*exhelp.Builder, error) {
	order := p.OrderUTXO.Output
	originalBase := order.TokenBalance()
	tokenAmt, price, err := parseSellOrder(order)
	if err != nil {
		return nil, fmt.Errorf("BuildFillSellOrder: parse order: %w", err)
	}
	receiptBase := originalBase + tokenAmt.Amount*price
	sellerHolder, err := orderIssuer(order)
	if err != nil {
		return nil, fmt.Errorf("BuildFillSellOrder: read issuer: %w", err)
	}

	txb := exhelp.New()

	// Order goes in first (input 0) so the literal-equals-input-index check
	// reads selfOutputIndex == 0.
	orderInIdx, err := txb.ConsumeOutput(order, p.OrderUTXO.ID)
	if err != nil {
		return nil, fmt.Errorf("BuildFillSellOrder: consume order: %w", err)
	}

	// Buyer funding inputs.
	fundingTotal := uint64(0)
	for i, in := range p.FundingInputs {
		idx, err := txb.ConsumeOutput(in.Output, in.ID)
		if err != nil {
			return nil, fmt.Errorf("BuildFillSellOrder: consume funding %d: %w", i, err)
		}
		if i == 0 {
			txb.PutSignatureUnlock(idx)
		} else {
			if err := txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, byte(int(orderInIdx)+1)); err != nil {
				return nil, fmt.Errorf("BuildFillSellOrder: put unlock reference: %w", err)
			}
		}
		fundingTotal += in.Output.TokenBalance()
	}
	if fundingTotal < tokenAmt.Amount*price {
		return nil, fmt.Errorf("BuildFillSellOrder: funding %d < required payment %d", fundingTotal, tokenAmt.Amount*price)
	}

	// Receipt to seller at output index 0 (4 constraints).
	receipt := buildReceiptOutputSell(receiptBase, ledger.SigLock(sellerHolder), byte(orderInIdx))
	receiptIdx, err := txb.ProduceOutput(receipt)
	if err != nil {
		return nil, fmt.Errorf("BuildFillSellOrder: produce receipt: %w", err)
	}
	// Unlock the order lock with K = receipt output index.
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	// Buyer takes the X native tokens into a sigLock(buyer) output. Use a
	// generous dust budget so we don't need to round-trip storage-deposit
	// computation against the output bytes; tests pick comfortable values.
	const dust = uint64(100_000_000) // 100M, well above sigLock min deposit (~14M)
	buyerTokens := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(dust))
		o.WithLock(p.BuyerSigLock)
		o.WithTokenAmount(tokenAmt.Tag, tokenAmt.Amount)
	})
	if _, err := txb.ProduceOutput(buyerTokens); err != nil {
		return nil, fmt.Errorf("BuildFillSellOrder: produce buyer-tokens: %w", err)
	}

	// Change.
	payment := tokenAmt.Amount * price
	if change := fundingTotal - payment - dust; change > 0 {
		ret := ledger.OutputBasic(int64(change), p.BuyerSigLock)
		if _, err := txb.ProduceOutput(ret); err != nil {
			return nil, fmt.Errorf("BuildFillSellOrder: produce change: %w", err)
		}
	}

	txb.DeclareTokenConservation(tokenAmt.Tag)
	if err := pushRedeemScript(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.BuyerPrivKey)
	return txb, nil
}

// BuildFillBuyOrderParams collects inputs for BuildFillBuyOrder. The seller
// signs, consumes the buy order, and delivers the native tokens to the buyer
// via a 5-constraint receipt output.
type BuildFillBuyOrderParams struct {
	SellerPrivKey ed25519.PrivateKey
	SellerSigLock ledger.SigLock
	OrderUTXO     *ledger.OutputWithID
	// TokenInputs hold the native tokens (tag T, total ≥ amount). At least
	// one must be sigLock-locked for the seller; the first becomes the
	// signature-bearing one.
	TokenInputs []*ledger.OutputWithID
	TxTimestamp base.LedgerTime
}

func BuildFillBuyOrder(p BuildFillBuyOrderParams) (*exhelp.Builder, error) {
	order := p.OrderUTXO.Output
	originalBase := order.TokenBalance()
	tag, amount, price, err := parseBuyOrder(order)
	if err != nil {
		return nil, fmt.Errorf("BuildFillBuyOrder: parse order: %w", err)
	}
	payment := amount * price
	if originalBase < payment {
		return nil, fmt.Errorf("BuildFillBuyOrder: order deposit %d < payment %d", originalBase, payment)
	}
	receiptBase := originalBase - payment
	buyerHolder, err := orderIssuer(order)
	if err != nil {
		return nil, fmt.Errorf("BuildFillBuyOrder: read issuer: %w", err)
	}

	txb := exhelp.New()

	orderInIdx, err := txb.ConsumeOutput(order, p.OrderUTXO.ID)
	if err != nil {
		return nil, fmt.Errorf("BuildFillBuyOrder: consume order: %w", err)
	}

	tokenSupplied := uint64(0)
	baseFromTokens := uint64(0)
	for i, in := range p.TokenInputs {
		idx, err := txb.ConsumeOutput(in.Output, in.ID)
		if err != nil {
			return nil, fmt.Errorf("BuildFillBuyOrder: consume token input %d: %w", i, err)
		}
		if i == 0 {
			txb.PutSignatureUnlock(idx)
		} else {
			if err := txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, byte(int(orderInIdx)+1)); err != nil {
				return nil, fmt.Errorf("BuildFillBuyOrder: put unlock reference: %w", err)
			}
		}
		baseFromTokens += in.Output.TokenBalance()
		tokenSupplied += sumTokenAmountByTag(in.Output, tag)
	}
	if tokenSupplied < amount {
		return nil, fmt.Errorf("BuildFillBuyOrder: token inputs supply %d < required %d", tokenSupplied, amount)
	}

	// Receipt to buyer at output index 0 (5 constraints: amounts,
	// indexValues, sigLock, 1-byte literal, tokenAmount).
	receipt := buildReceiptOutputBuy(receiptBase, ledger.SigLock(buyerHolder), byte(orderInIdx), tag, amount)
	receiptIdx, err := txb.ProduceOutput(receipt)
	if err != nil {
		return nil, fmt.Errorf("BuildFillBuyOrder: produce receipt: %w", err)
	}
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{byte(receiptIdx)})

	// Seller takes payment + any token change.
	sellerBase := payment + baseFromTokens
	tokenChange := tokenSupplied - amount
	if tokenChange > 0 {
		sellerOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(sellerBase))
			o.WithLock(p.SellerSigLock)
			o.WithTokenAmount(tag, tokenChange)
		})
		if _, err := txb.ProduceOutput(sellerOut); err != nil {
			return nil, fmt.Errorf("BuildFillBuyOrder: produce seller output: %w", err)
		}
	} else {
		sellerOut := ledger.OutputBasic(int64(sellerBase), p.SellerSigLock)
		if _, err := txb.ProduceOutput(sellerOut); err != nil {
			return nil, fmt.Errorf("BuildFillBuyOrder: produce seller output: %w", err)
		}
	}

	txb.DeclareTokenConservation(tag)
	if err := pushRedeemScript(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.SellerPrivKey)
	return txb, nil
}

// =============================================================================
// Order reclaim (after timeout)
// =============================================================================

// BuildReclaimOrderParams collects inputs for BuildReclaimOrder. Works for
// both sell and buy orders — the dex lock's reclaim window invokes sigLock,
// so the unlock is the standard signature unlock byte (0xff).
type BuildReclaimOrderParams struct {
	IssuerPrivKey ed25519.PrivateKey
	IssuerSigLock ledger.SigLock
	OrderUTXO     *ledger.OutputWithID
	// IsSellOrder is true for sell-order reclaim (tag conservation needed,
	// reclaimed tokens go to a sigLock(issuer) UTXO with tokenAmount).
	IsSellOrder bool
	TxTimestamp base.LedgerTime
}

func BuildReclaimOrder(p BuildReclaimOrderParams) (*exhelp.Builder, error) {
	txb := exhelp.New()
	orderInIdx, err := txb.ConsumeOutput(p.OrderUTXO.Output, p.OrderUTXO.ID)
	if err != nil {
		return nil, fmt.Errorf("BuildReclaimOrder: consume order: %w", err)
	}
	// sigLock unlock: 0xff = direct signature unlock.
	txb.PutUnlockParams(orderInIdx, ledger.ConstraintIndexLock, []byte{0xff})
	txb.PutSignatureUnlock(orderInIdx)

	base := p.OrderUTXO.Output.TokenBalance()
	if p.IsSellOrder {
		// Reclaim base + tokens; emit one output to issuer carrying both.
		tokenAmt, _, err := parseSellOrder(p.OrderUTXO.Output)
		if err != nil {
			return nil, fmt.Errorf("BuildReclaimOrder: parse sell order: %w", err)
		}
		issuerOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(base))
			o.WithLock(p.IssuerSigLock)
			o.WithTokenAmount(tokenAmt.Tag, tokenAmt.Amount)
		})
		if _, err := txb.ProduceOutput(issuerOut); err != nil {
			return nil, fmt.Errorf("BuildReclaimOrder: produce issuer output: %w", err)
		}
		txb.DeclareTokenConservation(tokenAmt.Tag)
	} else {
		issuerOut := ledger.OutputBasic(int64(base), p.IssuerSigLock)
		if _, err := txb.ProduceOutput(issuerOut); err != nil {
			return nil, fmt.Errorf("BuildReclaimOrder: produce issuer output: %w", err)
		}
	}

	if err := pushRedeemScript(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.IssuerPrivKey)
	return txb, nil
}

// =============================================================================
// Receipt output construction
// =============================================================================

// buildReceiptOutputSell builds the 4-constraint receipt: amounts(receiptBase),
// indexValues(sellerHolder), sigLock, inline-data literal of orderInputIdx.
func buildReceiptOutputSell(receiptBase uint64, recipient ledger.SigLock, orderInputIdx byte) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(receiptBase))
		o.WithLock(recipient)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{orderInputIdx}))
	})
}

// buildReceiptOutputBuy builds the 5-constraint receipt: amounts,
// indexValues(buyerHolder), sigLock, inline-data literal, tokenAmount(tag, amount).
func buildReceiptOutputBuy(receiptBase uint64, recipient ledger.SigLock, orderInputIdx byte, tag base.ChainID, amount uint64) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(receiptBase))
		o.WithLock(recipient)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{orderInputIdx}))
		o.WithTokenAmount(tag, amount)
	})
}

// =============================================================================
// Order parsing helpers
// =============================================================================

// orderIssuer reads the holder ID stored at index-values position 0.
func orderIssuer(o *ledger.Output) (base.HolderID, error) {
	ivBin, err := o.At(int(ledger.ConstraintIndexIndexValues))
	if err != nil {
		return base.HolderID{}, err
	}
	ivs, err := ledger.IndexValuesFromBytes(ivBin)
	if err != nil {
		return base.HolderID{}, err
	}
	if len(ivs) < 1 || len(ivs[0]) != 32 {
		return base.HolderID{}, fmt.Errorf("orderIssuer: position-0 entry must be a 32-byte holder ID")
	}
	var h base.HolderID
	copy(h[:], ivs[0])
	return h, nil
}

// parseSellOrder reads the tokenAmount at output position 3 and the price
// argument from the lock at position 2.
func parseSellOrder(o *ledger.Output) (*ledger.TokenAmount, uint64, error) {
	taBin, err := o.At(3)
	if err != nil {
		return nil, 0, fmt.Errorf("parseSellOrder: read tokenAmount at index 3: %w", err)
	}
	ta, err := ledger.TokenAmountFromBytes(taBin)
	if err != nil {
		return nil, 0, fmt.Errorf("parseSellOrder: tokenAmount: %w", err)
	}
	lockBin, err := o.At(int(ledger.ConstraintIndexLock))
	if err != nil {
		return nil, 0, fmt.Errorf("parseSellOrder: read lock: %w", err)
	}
	args, err := parseCallRedeemerArgs(lockBin, 4) // hash, fnIdx, price, timeoutSlots
	if err != nil {
		return nil, 0, fmt.Errorf("parseSellOrder: %w", err)
	}
	price, err := uint64FromZBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, 0, fmt.Errorf("parseSellOrder: price: %w", err)
	}
	return ta, price, nil
}

// parseBuyOrder reads (tag, amount, price) from a buy order UTXO. Tag comes
// from the slot-1 entry at position 1 (sliced out of "ORDR || tag || side").
func parseBuyOrder(o *ledger.Output) (base.ChainID, uint64, uint64, error) {
	ivBin, err := o.At(int(ledger.ConstraintIndexIndexValues))
	if err != nil {
		return base.ChainID{}, 0, 0, fmt.Errorf("parseBuyOrder: read indexValues: %w", err)
	}
	ivs, err := ledger.IndexValuesFromBytes(ivBin)
	if err != nil {
		return base.ChainID{}, 0, 0, err
	}
	entryLen := 4 + base.ChainIDLength + 1
	if len(ivs) < 2 || len(ivs[1]) != entryLen {
		return base.ChainID{}, 0, 0, fmt.Errorf("parseBuyOrder: position-1 entry must be %d bytes", entryLen)
	}
	var tag base.ChainID
	copy(tag[:], ivs[1][4:4+base.ChainIDLength])

	lockBin, err := o.At(int(ledger.ConstraintIndexLock))
	if err != nil {
		return base.ChainID{}, 0, 0, fmt.Errorf("parseBuyOrder: read lock: %w", err)
	}
	args, err := parseCallRedeemerArgs(lockBin, 5) // hash, fnIdx, amount, price, timeoutSlots
	if err != nil {
		return base.ChainID{}, 0, 0, fmt.Errorf("parseBuyOrder: %w", err)
	}
	amount, err := uint64FromZBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return base.ChainID{}, 0, 0, fmt.Errorf("parseBuyOrder: amount: %w", err)
	}
	price, err := uint64FromZBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return base.ChainID{}, 0, 0, fmt.Errorf("parseBuyOrder: price: %w", err)
	}
	return tag, amount, price, nil
}

// parseCallRedeemerArgs strips the call envelope from a callRedeemer
// bytecode and returns the raw bytes of each argument. Returns error if the
// argument count doesn't match wantArgs.
func parseCallRedeemerArgs(bytecode []byte, wantArgs int) ([][]byte, error) {
	args := make([][]byte, 0, wantArgs)
	lib := ledger.L(base.MaxSlot)
	sym, _, parsedArgs, err := lib.ParseBytecodeOneLevel(bytecode, wantArgs)
	if err != nil {
		return nil, fmt.Errorf("parseCallRedeemerArgs: %w", err)
	}
	if sym != "callRedeemer" {
		return nil, fmt.Errorf("parseCallRedeemerArgs: expected callRedeemer, got %s", sym)
	}
	args = append(args, parsedArgs...)
	return args, nil
}

// uint64FromZBytes decodes a z-encoded uint64 (1..8 bytes, BE).
func uint64FromZBytes(b []byte) (uint64, error) {
	if len(b) > 8 {
		return 0, fmt.Errorf("uint64FromZBytes: %d bytes > 8", len(b))
	}
	var v uint64
	for _, by := range b {
		v = (v << 8) | uint64(by)
	}
	return v, nil
}

// sumTokenAmountByTag iterates the output's constraints and returns the
// total amount across all tokenAmount(tag, _) instances matching the tag.
func sumTokenAmountByTag(o *ledger.Output, tag base.ChainID) uint64 {
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
