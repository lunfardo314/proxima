// Native-token (foundry / tokenAmount / token) end-to-end UTXODB tests.
//
// These tests exercise the runtime behaviour of:
//   - the foundry chain origin and the first transit (tag turns into the
//     real chain ID; supply grows; tokenAmount outputs minted)
//   - the `token(...)` tx-level balance equation in its sentinel
//     (pure-conservation) and foundry-transit forms
//   - the Phase D auditability check (any unmatched tokenAmount tag is
//     rejected)
//   - the two predefined policy scripts (foundryNonDestructible,
//     foundryMaxSupply) and the universal selfImmutableOnSuccessorIndex
//     helper that backs them
//
// See claude/native_token.md for the full spec.

package tests

import (
	"crypto/ed25519"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

type foundryTestEnv struct {
	u       *utxodb.UTXODB
	privKey ed25519.PrivateKey
	addr    ledger.SigLock
}

func newFoundryTestEnv(t *testing.T, amount uint64) *foundryTestEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, amount)
	require.NoError(t, err)
	return &foundryTestEnv{u: u, privKey: privKey, addr: addr}
}

// createFoundryOrigin builds and submits a foundry origin tx with
// `onChainAmount` PRXI on the foundry output and the optional `policy`
// bytecode at ConstraintIndexFoundryPolicy. Returns the future chain ID
// (= blake2b of the produced foundry's output ID).
func (e *foundryTestEnv) createFoundryOrigin(t *testing.T, onChainAmount uint64, policy []byte) base.ChainID {
	t.Helper()
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	txb := txbuilder.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)

	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			require.NoError(t, err)
		}
	}

	foundryOut := txbuilder.MakeFoundryOriginOutput(onChainAmount, e.addr, ts.Slot, 0, policy)
	require.NoError(t, foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)

	addRemainderIfNeeded(t, txb, e.addr)

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)

	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	require.NoError(t, err, "foundry-origin build/validation failed: %s", failedTx)

	require.NoError(t, e.u.AddTransaction(txBytes))

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	require.NoError(t, err)
	return base.MakeOriginChainID(foundryOid)
}

// foundryInputData fetches the current foundry chain output by chainID and
// wraps it as OutputDataWithChainID, ready for TransitFoundry.
func (e *foundryTestEnv) foundryInputData(t *testing.T, chainID base.ChainID) *ledger.OutputDataWithChainID {
	t.Helper()
	oData, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	return &ledger.OutputDataWithChainID{
		OutputDataWithID: *oData,
		ChainID:          chainID,
	}
}

// appendExtraFunding consumes the wallet's pure-PRXI sigLock UTXOs
// (those carrying NO tokenAmount constraint) that are not already in
// the builder's consumed-input set, appending them starting at the
// current input count and wiring each one to reference the signature
// at sigInputIdx. Does NOT touch the unlock data at sigInputIdx
// itself — the caller is responsible for
// `txb.PutSignatureUnlock(sigInputIdx)` separately.
//
// Two filters are essential:
//   - already-consumed outputs (tests that explicitly consumed a
//     tokenAmount UTXO as input 0 must not have it re-consumed here)
//   - tokenAmount-bearing UTXOs (re-consuming them in a mint/transit
//     would add their amount to the consumed-side balance and require
//     re-producing the tokens, breaking the simple flow we want here)
func (e *foundryTestEnv) appendExtraFunding(t *testing.T, txb *txbuilder.TxBuilder, sigInputIdx byte) base.LedgerTime {
	t.Helper()
	already := make(map[base.OutputID]struct{}, len(txb.TransactionData.InputIDs))
	for _, oid := range txb.TransactionData.InputIDs {
		already[*oid] = struct{}{}
	}
	outs := getSourceOutputs(t, e.u, e.addr)
	var maxTs base.LedgerTime
	for _, o := range outs {
		if _, dup := already[o.ID]; dup {
			continue
		}
		if outputCarriesTokenAmount(o.Output) {
			continue
		}
		idx, err := txb.ConsumeOutput(o.Output, o.ID)
		require.NoError(t, err)
		require.NoError(t, txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, sigInputIdx))
		maxTs = base.MaximumTime(maxTs, o.Timestamp())
	}
	return maxTs
}

// outputCarriesTokenAmount reports whether the output has any
// tokenAmount(...) constraint among its bytecode positions.
func outputCarriesTokenAmount(o *ledger.Output) bool {
	for _, raw := range o.ConstraintsRawBytes() {
		if _, err := ledger.TokenAmountFromBytes(raw); err == nil {
			return true
		}
	}
	return false
}

// finishAndSubmit signs the builder, validates and submits the tx.
// Returns the submission error, the tx ID and the validated tx bytes.
func (e *foundryTestEnv) finishAndSubmit(t *testing.T, txb *txbuilder.TxBuilder, ts base.LedgerTime) ([]byte, base.TransactionID, error) {
	t.Helper()
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)
	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	if err != nil {
		t.Logf("validation failed: %s", failedTx)
		return nil, txid, err
	}
	return txBytes, txid, e.u.AddTransaction(txBytes)
}

// addRemainderIfNeeded appends a wallet-locked sigLock output carrying
// any leftover PRXI (consumed - already-produced).
func addRemainderIfNeeded(t *testing.T, txb *txbuilder.TxBuilder, lock ledger.Lock) {
	t.Helper()
	totalConsumed := txb.ConsumedAmount()
	totalProduced, _ := txb.ProducedAmount()
	if totalConsumed <= totalProduced {
		return
	}
	remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(totalConsumed - totalProduced).WithLock(lock)
	})
	_, err := txb.ProduceOutput(remainder)
	require.NoError(t, err)
}

// --------------------------------------------------------------------------
// Sanity: foundry origin with various policy combinations
// --------------------------------------------------------------------------

// TestFoundryOriginNoPolicy verifies that a foundry origin output with no
// policy bytecode at index 5 builds, validates and lands in state.
func TestFoundryOriginNoPolicy(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)

	fBytes, err := parsed.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(fBytes)
	require.NoError(t, err)
	require.EqualValues(t, base.NilChainID, f.Tag, "origin foundry tag must be NilChainID")
	require.EqualValues(t, 0, f.Supply, "origin foundry supply must be 0")

	_, err = parsed.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy)
	require.Error(t, err, "origin with no policy must have no constraint at index 5")
}

// TestFoundryOriginWithNonDestructiblePolicy verifies the
// foundryNonDestructible bytecode embeds cleanly at origin and survives
// state round-trip (the policy is a no-op at origin's produced side).
func TestFoundryOriginWithNonDestructiblePolicy(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	policy := ledger.FoundryNonDestructibleBytecode()
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	embedded, err := parsed.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy)
	require.NoError(t, err)
	require.Equal(t, policy, embedded, "policy bytes must round-trip via the state")
}

// TestFoundryOriginWithMaxSupplyPolicy mirrors the above for foundryMaxSupply.
func TestFoundryOriginWithMaxSupplyPolicy(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	policy := ledger.FoundryMaxSupplyBytecode(1_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	embedded, err := parsed.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy)
	require.NoError(t, err)
	require.Equal(t, policy, embedded)
}

// --------------------------------------------------------------------------
// First transit (mint): tag becomes the real chain ID; tokens are produced
// --------------------------------------------------------------------------

// TestFoundryFirstMint covers the canonical mint flow: starting from a
// foundry origin with foundry(NilChainID, 0), the first transit produces
// foundry(realChainID, mintAmount) and a tokenAmount(realChainID,
// mintAmount) output on a sigLock to the test address. The
// `token(realChainID, foundryProducedIdx)` constraint pushed by
// TransitFoundry enforces the balance equation.
func TestFoundryFirstMint(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	mintToSelf(t, e, chainID, mintAmount)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsed.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	require.EqualValues(t, chainID, f.Tag, "post-transit foundry tag must equal chain ID")
	require.EqualValues(t, mintAmount, f.Supply)

	// And the tokenAmount-bearing UTXO must now exist on the wallet.
	_, tokenOut := findTokenOutput(t, e, chainID)
	ta, err := findTokenAmount(t, tokenOut.Output, chainID)
	require.NoError(t, err)
	require.EqualValues(t, mintAmount, ta.Amount)
}

// TestFoundryMintToOtherAddress exercises `proxi node foundry mint -t <other>`:
// the minted tokenAmount lands on a sigLock controlled by a key other than
// the wallet's. The wallet still signs the foundry transit; the recipient
// owns the new UTXO.
func TestFoundryMintToOtherAddress(t *testing.T) {
	const mintAmount = uint64(2_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	// Recipient is a fresh ED25519 keypair the test wallet does NOT control.
	_, _, recipient := e.u.GenerateAddress(42)

	require.NoError(t, tryMintTo(t, e, chainID, mintAmount, recipient),
		"mint to a separate sigLock target must validate")

	// Foundry supply grew to mintAmount.
	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsed.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	require.EqualValues(t, mintAmount, f.Supply)

	// The minted UTXO sits on `recipient`, not on e.addr.
	recipientOuts, err := e.u.SugaredStateReader().GetOutputsForAccount(recipient.ControllerID())
	require.NoError(t, err)
	var found *ledger.TokenAmount
	for _, o := range recipientOuts {
		if ta, err := findTokenAmount(t, o.Output, chainID); err == nil {
			found = ta
			break
		}
	}
	require.NotNil(t, found, "recipient must hold the tokenAmount UTXO")
	require.EqualValues(t, mintAmount, found.Amount)

	// And the wallet does NOT hold any tokenAmount for this tag.
	walletOuts := getSourceOutputs(t, e.u, e.addr)
	for _, o := range walletOuts {
		_, err := findTokenAmount(t, o.Output, chainID)
		require.Error(t, err, "wallet must not own a tokenAmount for tag %s after minting to a third party", chainID.StringShort())
	}
}

// TestFoundryMintMultipleTimes runs two back-to-back mints on the same
// foundry. After the second mint the foundry supply must equal the sum
// of the two mintAmounts, the wallet must hold both minted UTXOs
// independently, and the foundry's tag must be the real chain ID.
func TestFoundryMintMultipleTimes(t *testing.T) {
	const firstMint = uint64(1_000_000)
	const secondMint = uint64(750_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	mintToSelf(t, e, chainID, firstMint)
	mintToSelf(t, e, chainID, secondMint)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsed.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	require.EqualValues(t, chainID, f.Tag, "post-second-transit tag must still equal chain ID")
	require.EqualValues(t, firstMint+secondMint, f.Supply,
		"foundry supply must accumulate across mints")

	// Wallet now holds two independent tokenAmount UTXOs for this tag.
	walletOuts := getSourceOutputs(t, e.u, e.addr)
	var sum uint64
	for _, o := range walletOuts {
		if ta, err := findTokenAmount(t, o.Output, chainID); err == nil {
			sum += ta.Amount
		}
	}
	require.EqualValues(t, firstMint+secondMint, sum,
		"sum of tokenAmount UTXOs on the wallet must equal total minted")
}

// --------------------------------------------------------------------------
// Pure conservation transfer (`token(tag, 0x)` sentinel form)
// --------------------------------------------------------------------------

// TestFoundrySendTaggedPartialWithRemainder mirrors `proxi node send
// <amount> --tag <chainID>` with a single tokenAmount input that
// exceeds the transfer amount. Verifies the recipient gets a
// tokenAmount(tag, amount) UTXO, the wallet gets the (consumed - amount)
// remainder as a new tokenAmount UTXO, and supply on the foundry stays
// untouched.
func TestFoundrySendTaggedPartialWithRemainder(t *testing.T) {
	const mintAmount = uint64(1_000_000)
	const sendAmount = uint64(300_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	_, _, recipient := e.u.GenerateAddress(7)
	require.NoError(t, sendTagged(t, e, chainID, sendAmount, recipient),
		"tagged send must validate")

	// Recipient holds tokenAmount(tag, sendAmount).
	recipientOuts, err := e.u.SugaredStateReader().GetOutputsForAccount(recipient.ControllerID())
	require.NoError(t, err)
	var got *ledger.TokenAmount
	for _, o := range recipientOuts {
		if ta, err := findTokenAmount(t, o.Output, chainID); err == nil {
			got = ta
			break
		}
	}
	require.NotNil(t, got, "recipient must own the new tokenAmount UTXO")
	require.EqualValues(t, sendAmount, got.Amount)

	// Wallet now holds the remainder tokenAmount(tag, mintAmount - sendAmount).
	walletOuts := getSourceOutputs(t, e.u, e.addr)
	var walletTokenSum uint64
	for _, o := range walletOuts {
		if ta, err := findTokenAmount(t, o.Output, chainID); err == nil {
			walletTokenSum += ta.Amount
		}
	}
	require.EqualValues(t, mintAmount-sendAmount, walletTokenSum,
		"wallet must keep the partial-send remainder as a new tokenAmount UTXO")

	// Foundry supply unchanged (pure conservation: no transit happened).
	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsed.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	require.EqualValues(t, mintAmount, f.Supply,
		"foundry supply must not change on a pure conservation transfer")
}

// TestFoundrySendTaggedConsumesMultipleInputs mints twice so the wallet
// holds two independent tokenAmount UTXOs, then sends the full combined
// balance. Verifies both inputs are consumed and the recipient receives
// a single output for the total amount.
func TestFoundrySendTaggedConsumesMultipleInputs(t *testing.T) {
	const firstMint = uint64(400_000)
	const secondMint = uint64(600_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, firstMint)
	mintToSelf(t, e, chainID, secondMint)

	// Sanity: wallet has 2 tokenAmount UTXOs before the send.
	walletOuts := getSourceOutputs(t, e.u, e.addr)
	preCount := 0
	for _, o := range walletOuts {
		if _, err := findTokenAmount(t, o.Output, chainID); err == nil {
			preCount++
		}
	}
	require.Equal(t, 2, preCount, "wallet must hold 2 tokenAmount UTXOs for tag before the send")

	_, _, recipient := e.u.GenerateAddress(13)
	require.NoError(t, sendTagged(t, e, chainID, firstMint+secondMint, recipient),
		"send of the full balance must consume both inputs and validate")

	// Recipient gets one output for the total.
	recipientOuts, err := e.u.SugaredStateReader().GetOutputsForAccount(recipient.ControllerID())
	require.NoError(t, err)
	var got *ledger.TokenAmount
	for _, o := range recipientOuts {
		if ta, err := findTokenAmount(t, o.Output, chainID); err == nil {
			got = ta
			break
		}
	}
	require.NotNil(t, got)
	require.EqualValues(t, firstMint+secondMint, got.Amount)

	// Wallet no longer holds any tokenAmount for the tag.
	walletOuts = getSourceOutputs(t, e.u, e.addr)
	for _, o := range walletOuts {
		_, err := findTokenAmount(t, o.Output, chainID)
		require.Error(t, err, "wallet must hold no tokenAmount(tag, _) after a full-balance send")
	}
}

// TestFoundryConservationTransfer mints, then in a separate tx transfers
// half of the tokenAmount to a second address using the pure-conservation
// `token(tag, 0x)` sentinel (no foundry transit).
func TestFoundryConservationTransfer(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	tokenOutID, tokenOut := findTokenOutput(t, e, chainID)
	_, _, addr2 := e.u.GenerateAddress(2)

	// Input 0: the tokenAmount-bearing sigLock UTXO.
	txb := txbuilder.New()
	_, err := txb.ConsumeOutput(tokenOut.Output, tokenOutID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Inputs 1..N: additional sigLock funding for storage deposits.
	ts := tokenOutID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, 0))

	out1 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(50_000_000).WithLock(e.addr).WithTokenAmount(chainID, mintAmount/2)
	})
	require.NoError(t, out1.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(out1)
	require.NoError(t, err)

	out2 := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(50_000_000).WithLock(addr2).WithTokenAmount(chainID, mintAmount/2)
	})
	require.NoError(t, out2.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(out2)
	require.NoError(t, err)

	// Sentinel declaration — required for Phase D auditability.
	txb.DeclareTokenConservation(chainID)

	addRemainderIfNeeded(t, txb, e.addr)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	require.NoError(t, err, "conservation transfer must validate")
}

// --------------------------------------------------------------------------
// Auditability: an undeclared tokenAmount tag must be rejected
// --------------------------------------------------------------------------

// TestFoundryAuditabilityRejectsUndeclared verifies that a tx that consumes
// or produces a tokenAmount without a matching tx-level token(...)
// declaration is rejected.
func TestFoundryAuditabilityRejectsUndeclared(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)
	tokenOutID, tokenOut := findTokenOutput(t, e, chainID)

	_, _, addr2 := e.u.GenerateAddress(2)

	txb := txbuilder.New()
	_, err := txb.ConsumeOutput(tokenOut.Output, tokenOutID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	ts := tokenOutID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, 0))

	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(100_000_000).WithLock(addr2).WithTokenAmount(chainID, mintAmount)
	})
	require.NoError(t, out.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	addRemainderIfNeeded(t, txb, e.addr)

	// NOTE: deliberately no DeclareTokenConservation call.
	_, _, err = e.finishAndSubmit(t, txb, ts)
	require.Error(t, err, "tokenAmount without declaration must be rejected")
	require.NoError(t, util.MustErrorWith(err, "undeclared native token tag"))
	t.Logf("undeclared tag rejected: %v", err)
}

// --------------------------------------------------------------------------
// foundryMaxSupply policy: accept at cap, reject over cap
// --------------------------------------------------------------------------

// TestFoundryMaxSupplyAcceptAtCap verifies a first-mint that produces a
// foundry supply exactly equal to the cap is accepted.
func TestFoundryMaxSupplyAcceptAtCap(t *testing.T) {
	const cap = uint64(500_000)
	policy := ledger.FoundryMaxSupplyBytecode(cap)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	mintToSelf(t, e, chainID, cap)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsed.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	require.EqualValues(t, cap, f.Supply)
}

// TestFoundryMaxSupplyRejectsOverCap verifies that a mint that exceeds the
// cap by 1 is rejected by the foundryMaxSupply policy script.
func TestFoundryMaxSupplyRejectsOverCap(t *testing.T) {
	const cap = uint64(500_000)
	policy := ledger.FoundryMaxSupplyBytecode(cap)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	err := tryMintToSelf(t, e, chainID, cap+1)
	require.Error(t, err, "mint above the cap must be rejected by foundryMaxSupply")
	require.NoError(t, util.MustErrorWith(err, "foundry supply exceeds max supply"))
	t.Logf("over-cap mint rejected: %v", err)
}

// --------------------------------------------------------------------------
// foundryNonDestructible policy: reject retire while supply > 0;
// accept retire when supply == 0
// --------------------------------------------------------------------------

// TestFoundryNonDestructibleRejectsRetireWithSupply mints some tokens then
// attempts to discontinue the foundry chain while supply > 0. The policy
// must reject.
func TestFoundryNonDestructibleRejectsRetireWithSupply(t *testing.T) {
	const mintAmount = uint64(1_000_000)
	policy := ledger.FoundryNonDestructibleBytecode()

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	mintToSelf(t, e, chainID, mintAmount)

	err := tryRetireFoundry(t, e, chainID)
	require.Error(t, err, "retire with non-zero supply must be rejected")
	require.NoError(t, util.MustErrorWith(err, "cannot destroy foundry with non zero supply"))
	t.Logf("retire-with-supply rejected: %v", err)
}

// TestFoundryNonDestructibleAcceptsRetireAtZero burns all minted tokens
// back into the foundry, then discontinues the chain. Should validate.
func TestFoundryNonDestructibleAcceptsRetireAtZero(t *testing.T) {
	const mintAmount = uint64(1_000_000)
	policy := ledger.FoundryNonDestructibleBytecode()

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	mintToSelf(t, e, chainID, mintAmount)
	burnAll(t, e, chainID, mintAmount)

	require.NoError(t, tryRetireFoundry(t, e, chainID), "retire at zero supply must validate")
	_, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.Error(t, err, "retired foundry chain must not be in state")
}

// --------------------------------------------------------------------------
// selfImmutableOnSuccessorIndex: policy bytes must be byte-equal across
// transit. A transit that drops the policy at index 5 is rejected.
// --------------------------------------------------------------------------

// TestFoundryPolicyImmutabilityCarryOverOK is the positive control:
// a normal mint transit (where TransitFoundry's Clone preserves index 5
// byte-equal) must validate.
func TestFoundryPolicyImmutabilityCarryOverOK(t *testing.T) {
	policy := ledger.FoundryNonDestructibleBytecode()

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	mintToSelf(t, e, chainID, 1_000_000)

	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	embedded, err := parsed.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy)
	require.NoError(t, err)
	require.Equal(t, policy, embedded, "policy bytes must survive transit byte-equal")
}

// TestFoundryPolicyImmutabilityRejectsRemoval verifies that a transit
// which drops the policy bytecode at index 5 is rejected by
// selfImmutableOnSuccessorIndex on the consumed predecessor.
//
// We do this on a NON-origin foundry: first complete a mint transit
// (which keeps the policy), then on a second transit hand-build a
// successor that omits index 5. The consumed predecessor's policy
// script will run and find the successor's index 5 missing, failing
// the byte-equality check.
func TestFoundryPolicyImmutabilityRejectsRemoval(t *testing.T) {
	policy := ledger.FoundryNonDestructibleBytecode()

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, policy)

	// First mint so the foundry's tag is the real chain ID and the
	// policy fires on the consumed side at the next transit.
	mintToSelf(t, e, chainID, 1_000_000)

	in := e.foundryInputData(t, chainID)
	parsedIn := parseOutput(t, in)

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(parsedIn, in.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	// Hand-build a successor: same amounts, lock, transited chain,
	// transited foundry; DROP index 5.
	cc := parsedIn.ChainConstraint()
	successorCC := ledger.NewChainConstraint(
		chainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	successorOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(parsedIn.TokenBalance()).WithLock(parsedIn.Lock())
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
		// Keep supply at current so token() balance is identity.
		fIn, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsedIn, ledger.ConstraintIndexFoundry))
		require.NoError(t, err)
		o.PutConstraint(ledger.NewFoundry(chainID, fIn.Supply).Bytes(), ledger.ConstraintIndexFoundry)
		// deliberately NO constraint at ConstraintIndexFoundryPolicy
	})
	succIdx, err := txb.ProduceOutput(successorOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	// Pair the chain transit with token(chainID, succIdx). Foundry supply
	// is unchanged, but the tokenAmount inputs that fund balance must be
	// consumed and re-produced or burned -- since we are not changing supply,
	// nothing to do for token() here other than declaring the foundry
	// transit. Phase D auditability will scan for any tokenAmount UTXOs
	// consumed; this tx consumes only the foundry, so no extras.
	txb.PushTxConstraint(ledger.TokenFoundryBytecode(chainID, succIdx))

	_, _, err = e.finishAndSubmit(t, txb, ts)
	require.Error(t, err, "transit that drops the policy at index 5 must be rejected")
	t.Logf("policy-removal transit rejected: %v", err)
}

// --------------------------------------------------------------------------
// Test-internal helpers
// --------------------------------------------------------------------------

// parseOutput parses raw output data into a *ledger.Output using the
// library active at the input's slot.
func parseOutput(t *testing.T, in *ledger.OutputDataWithChainID) *ledger.Output {
	t.Helper()
	o, err := ledger.OutputFromBytesWithLib(in.Data, ledger.L(in.ID.Slot()))
	require.NoError(t, err)
	return o
}

// mintToSelf runs a foundry transit that grows supply by mintAmount and
// produces the matching tokenAmount output on the test address's sigLock.
// The foundry becomes input 0; signature unlock is on input 0; any
// additional wallet sigLock funding inputs are appended afterwards with
// reference unlocks to input 0.
func mintToSelf(t *testing.T, e *foundryTestEnv, chainID base.ChainID, mintAmount uint64) {
	t.Helper()
	require.NoError(t, tryMintToSelf(t, e, chainID, mintAmount), "mintToSelf must validate")
}

// tryMintToSelf is the policy-test variant: it does NOT require success.
// Returns the validation/submission error (nil on success). Mints to the
// test address.
func tryMintToSelf(t *testing.T, e *foundryTestEnv, chainID base.ChainID, mintAmount uint64) error {
	t.Helper()
	return tryMintTo(t, e, chainID, mintAmount, e.addr)
}

// tryMintTo runs the same flow as the proxi `foundry mint` command:
// TransitFoundry as input 0, signature unlock on input 0, wallet
// sig-lock funding appended, tokenAmount(chainID, mintAmount) output to
// `target`. Returns the validation/submission error (nil on success).
func tryMintTo(t *testing.T, e *foundryTestEnv, chainID base.ChainID, mintAmount uint64, target ledger.Lock) error {
	t.Helper()
	in := e.foundryInputData(t, chainID)
	fIn, err := ledger.FoundryFromBytes(mustConstraintAt(t, parseOutput(t, in), ledger.ConstraintIndexFoundry))
	require.NoError(t, err)

	txb := txbuilder.New()
	// Foundry becomes input 0, wired with chain unlock by TransitFoundry.
	_, err = txb.TransitFoundry(in, fIn.Supply+mintAmount)
	require.NoError(t, err)
	// Signature unlock at input 0 (the foundry's sigLock lives at index 2).
	txb.PutSignatureUnlock(0)

	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, 0))

	tokOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(100_000_000).WithLock(target).WithTokenAmount(chainID, mintAmount)
	})
	require.NoError(t, tokOut.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(tokOut)
	require.NoError(t, err)

	addRemainderIfNeeded(t, txb, e.addr)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	return err
}

// sendTagged mirrors `proxi node send <amount> --tag <chainID>`: it
// consumes wallet's tokenAmount(chainID, _) UTXOs totaling >= amount,
// produces a sigLock-locked tokenAmount(chainID, amount) to `target`,
// produces a tokenAmount remainder back to the wallet if needed, and
// pushes a token(chainID, 0x) sentinel for conservation. Returns the
// validation/submission error.
//
// Pure-PRXI funding inputs are appended via appendExtraFunding (which
// already filters out tokenAmount-bearing UTXOs).
func sendTagged(t *testing.T, e *foundryTestEnv, chainID base.ChainID, amount uint64, target ledger.Lock) error {
	t.Helper()
	require.NotZero(t, amount)

	// Find wallet's tokenAmount(chainID, _) UTXOs.
	walletOuts := getSourceOutputs(t, e.u, e.addr)
	var (
		tokenInputs []*ledger.OutputWithID
		consumed    uint64
	)
	for _, o := range walletOuts {
		ta, err := findTokenAmount(t, o.Output, chainID)
		if err != nil {
			continue
		}
		tokenInputs = append(tokenInputs, o)
		consumed += ta.Amount
		if consumed >= amount {
			break
		}
	}
	require.GreaterOrEqualf(t, consumed, amount,
		"insufficient tokenAmount(%s, _) UTXOs on wallet: have %d, need %d",
		chainID.StringShort(), consumed, amount)

	txb := txbuilder.New()
	// Consume tokenAmount inputs first (input 0..N-1).
	_, inTs, err := txb.ConsumeOutputsNoUnlock(tokenInputs...)
	require.NoError(t, err)
	for i := range tokenInputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	// Append pure-PRXI funding (appendExtraFunding skips
	// already-consumed and tokenAmount-bearing UTXOs).
	ts := inTs.AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, 0))

	// Recipient output: sigLock to target + tokenAmount(chainID, amount).
	recipientOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(100_000_000).WithLock(target).WithTokenAmount(chainID, amount)
	})
	require.NoError(t, recipientOut.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(recipientOut)
	require.NoError(t, err)

	// Optional remainder back to the wallet.
	if consumed > amount {
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(100_000_000).WithLock(e.addr).WithTokenAmount(chainID, consumed-amount)
		})
		require.NoError(t, remainderOut.EnoughAmountForStorageDeposit())
		_, err = txb.ProduceOutput(remainderOut)
		require.NoError(t, err)
	}

	addRemainderIfNeeded(t, txb, e.addr)

	// Phase D auditability + Σ-conservation.
	txb.DeclareTokenConservation(chainID)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	return err
}

// burnAll consumes the wallet's tokenAmount(chainID, burnAmount) output
// and transits the foundry reducing supply by burnAmount.
func burnAll(t *testing.T, e *foundryTestEnv, chainID base.ChainID, burnAmount uint64) {
	t.Helper()
	in := e.foundryInputData(t, chainID)
	fIn, err := ledger.FoundryFromBytes(mustConstraintAt(t, parseOutput(t, in), ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	require.GreaterOrEqual(t, fIn.Supply, burnAmount, "burn cannot exceed current foundry supply")

	tokenOutID, tokenOut := findTokenOutput(t, e, chainID)

	txb := txbuilder.New()
	// Input 0: foundry (via TransitFoundry, with chain unlock wired).
	_, err = txb.TransitFoundry(in, fIn.Supply-burnAmount)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Input 1: tokenAmount-bearing sigLock UTXO to burn.
	tokInIdx, err := txb.ConsumeOutput(tokenOut.Output, tokenOutID)
	require.NoError(t, err)
	require.NoError(t, txb.PutUnlockReference(tokInIdx, ledger.ConstraintIndexLock, 0))

	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, 0))
	ts = base.MaximumTime(ts, tokenOutID.Timestamp())

	// No tokenAmount outputs produced — the burned tokens vanish, the
	// foundry's supply field has already been reduced by `burnAmount`.
	addRemainderIfNeeded(t, txb, e.addr)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	require.NoError(t, err, "burnAll must validate")
}

// tryRetireFoundry attempts to discontinue the foundry chain (no produced
// successor). Returns the validation/submission error (nil on success).
func tryRetireFoundry(t *testing.T, e *foundryTestEnv, chainID base.ChainID) error {
	t.Helper()
	in := e.foundryInputData(t, chainID)
	parsedIn := parseOutput(t, in)

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(parsedIn, in.ID)
	require.NoError(t, err)

	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	// Move the foundry's PRXI to a plain sigLock output.
	nonChainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(parsedIn.TokenBalance()).WithLock(e.addr)
	})
	_, err = txb.ProduceOutput(nonChainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)
	txb.PutSignatureUnlock(predIdx)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	return err
}

// findTokenOutput returns the test address's tokenAmount-bearing sigLock
// UTXO carrying the given chainID tag. Asserts exactly one exists.
func findTokenOutput(t *testing.T, e *foundryTestEnv, chainID base.ChainID) (base.OutputID, *ledger.OutputWithID) {
	t.Helper()
	outs := getSourceOutputs(t, e.u, e.addr)
	for _, o := range outs {
		if _, err := findTokenAmount(t, o.Output, chainID); err == nil {
			return o.ID, o
		}
	}
	t.Fatalf("no tokenAmount output for tag %s", chainID.StringShort())
	return base.OutputID{}, nil
}

// findTokenAmount scans the output's constraints for a tokenAmount(tag, _)
// matching the given chainID. Returns the parsed wrapper or an error.
func findTokenAmount(t *testing.T, o *ledger.Output, chainID base.ChainID) (*ledger.TokenAmount, error) {
	t.Helper()
	for _, raw := range o.ConstraintsRawBytes() {
		ta, err := ledger.TokenAmountFromBytes(raw)
		if err == nil && ta.Tag == chainID {
			return ta, nil
		}
	}
	return nil, fmt.Errorf("no tokenAmount for tag %s", chainID.StringShort())
}

// mustConstraintAt returns the constraint bytes at the given index or fails.
func mustConstraintAt(t *testing.T, o *ledger.Output, idx byte) []byte {
	t.Helper()
	b, err := o.ConstraintAt(idx)
	require.NoError(t, err)
	return b
}
